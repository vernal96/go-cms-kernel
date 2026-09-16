package rabbiteventbus

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/vernal96/go-cms-kernel/connectors/support/eventbusutil"
	"github.com/vernal96/go-cms-kernel/eventbus"
)

const KeyHeader = "x-cms-event-key"

type Config struct {
	URL                string
	Exchange           string
	DialTimeout        time.Duration
	ConsumerRetryDelay time.Duration
	ShutdownTimeout    time.Duration
	ReconnectAttempts  int
	ReconnectDelay     time.Duration
}

type Factory struct {
	Config Config
}

func (f Factory) Open(ctx context.Context) (eventbus.Connector, error) {
	return New(ctx, f.Config)
}

type producerBackend interface {
	Ping(context.Context) error
	Publish(context.Context, eventbus.Message) error
	Close() error
}

type consumerBackend interface {
	Next(context.Context) (eventbus.Message, error)
	Ack() error
	Requeue() error
	Close() error
}

type consumerFactory func(
	context.Context,
	eventbus.Subscription,
) (consumerBackend, error)

type Connector struct {
	config       Config
	producer     producerBackend
	openConsumer consumerFactory
	rootContext  context.Context
	cancel       context.CancelFunc

	lifecycleMu sync.Mutex
	consumers   sync.WaitGroup
	closed      atomic.Bool
	closeOnce   sync.Once
	closeErr    error
}

func New(ctx context.Context, config Config) (*Connector, error) {
	if ctx == nil {
		return nil, errors.New("RabbitMQ event bus context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	properties := amqp.NewConnectionProperties()
	properties.SetClientConnectionName("go-cms-eventbus")
	connection, err := amqp.DialConfig(normalized.URL, amqp.Config{
		Dial:       amqp.DefaultDial(normalized.DialTimeout),
		Properties: properties,
		Recovery: &amqp.Recovery{
			ReconnectionConfig: &amqp.ReconnectionConfig{
				MaxRetryCount: normalized.ReconnectAttempts,
				RetryInterval: normalized.ReconnectDelay,
			},
			TopologyRecoveryMode: amqp.TopologyRecoveryOnlyTransient,
			OnTopologyEntityError: func(
				*amqp.Connection,
				amqp.TopologyRecoveryEntity,
			) bool {
				return false
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open RabbitMQ event bus connection: %w", err)
	}

	backend, err := newRabbitConnection(connection, normalized.Exchange)
	if err != nil {
		return nil, errors.Join(err, connection.Close())
	}
	return newConnector(
		normalized,
		backend,
		func(
			consumeContext context.Context,
			subscription eventbus.Subscription,
		) (consumerBackend, error) {
			return backend.OpenConsumer(consumeContext, subscription)
		},
	), nil
}

func newConnector(
	config Config,
	producer producerBackend,
	openConsumer consumerFactory,
) *Connector {
	rootContext, cancel := context.WithCancel(context.Background())
	return &Connector{
		config:       config,
		producer:     producer,
		openConsumer: openConsumer,
		rootContext:  rootContext,
		cancel:       cancel,
	}
}

func normalizeConfig(config Config) (Config, error) {
	config.URL = strings.TrimSpace(config.URL)
	config.Exchange = strings.TrimSpace(config.Exchange)
	if config.URL == "" {
		return Config{}, errors.New("RabbitMQ event bus URL is empty")
	}
	parsedURL, err := url.Parse(config.URL)
	if err != nil {
		return Config{}, fmt.Errorf("parse RabbitMQ event bus URL: %w", err)
	}
	if (parsedURL.Scheme != "amqp" && parsedURL.Scheme != "amqps") ||
		parsedURL.Host == "" {
		return Config{}, errors.New(
			"RabbitMQ event bus URL must use amqp or amqps and include a host",
		)
	}
	if config.Exchange == "" {
		return Config{}, errors.New("RabbitMQ event bus exchange is empty")
	}
	if config.DialTimeout <= 0 {
		return Config{}, errors.New(
			"RabbitMQ event bus dial timeout must be positive",
		)
	}
	if config.ConsumerRetryDelay <= 0 {
		return Config{}, errors.New(
			"RabbitMQ event bus consumer retry delay must be positive",
		)
	}
	if config.ShutdownTimeout <= 0 {
		return Config{}, errors.New(
			"RabbitMQ event bus shutdown timeout must be positive",
		)
	}
	if config.ReconnectAttempts <= 0 {
		return Config{}, errors.New(
			"RabbitMQ event bus reconnect attempts must be positive",
		)
	}
	if config.ReconnectDelay <= 0 {
		return Config{}, errors.New(
			"RabbitMQ event bus reconnect delay must be positive",
		)
	}
	return config, nil
}

func (c *Connector) Ping(ctx context.Context) error {
	if ctx == nil {
		return errors.New("RabbitMQ event bus ping context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.producer == nil || c.closed.Load() {
		return eventbus.ErrClosed
	}
	if err := c.producer.Ping(ctx); err != nil {
		return fmt.Errorf("ping RabbitMQ event bus: %w", err)
	}
	return nil
}

func (c *Connector) Publish(
	ctx context.Context,
	message eventbus.Message,
) error {
	if ctx == nil {
		return errors.New("RabbitMQ event bus publish context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := eventbusutil.ValidateMessage(message, KeyHeader); err != nil {
		return err
	}
	if c == nil || c.producer == nil || c.closed.Load() {
		return eventbus.ErrClosed
	}
	if err := c.producer.Publish(
		ctx,
		eventbusutil.CloneMessage(message),
	); err != nil {
		return fmt.Errorf(
			"publish RabbitMQ event with routing key %q: %w",
			message.Topic,
			err,
		)
	}
	return nil
}

func (c *Connector) Consume(
	ctx context.Context,
	subscription eventbus.Subscription,
	handler eventbus.Handler,
) error {
	if ctx == nil {
		return errors.New("RabbitMQ event bus consume context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if handler == nil {
		return errors.New("RabbitMQ event bus handler is nil")
	}
	if err := eventbusutil.ValidateSubscription(subscription); err != nil {
		return err
	}
	if c == nil || c.openConsumer == nil {
		return eventbus.ErrClosed
	}

	c.lifecycleMu.Lock()
	if c.closed.Load() {
		c.lifecycleMu.Unlock()
		return eventbus.ErrClosed
	}
	c.consumers.Add(1)
	c.lifecycleMu.Unlock()
	defer c.consumers.Done()

	consumeContext, cancel := context.WithCancel(ctx)
	stopRootCancel := context.AfterFunc(c.rootContext, cancel)
	defer func() {
		stopRootCancel()
		cancel()
	}()

	subscription = eventbusutil.CloneSubscription(subscription)
	consecutiveFailures := 0
	for {
		consumer, err := c.openConsumer(consumeContext, subscription)
		if err != nil {
			if consumeContext.Err() != nil {
				return nil
			}
			err = fmt.Errorf(
				"open RabbitMQ event bus consumer group %q: %w",
				subscription.Group,
				err,
			)
		} else {
			var processed bool
			processed, err = consumeRabbitSession(
				consumeContext,
				consumer,
				subscription,
				handler,
				c.config.ConsumerRetryDelay,
			)
			_ = consumer.Close()
			if consumeContext.Err() != nil {
				return nil
			}
			if processed {
				consecutiveFailures = 0
			}
		}
		consecutiveFailures++
		if consecutiveFailures >= c.config.ReconnectAttempts {
			return err
		}
		if !waitForRabbitRetry(consumeContext, c.config.ReconnectDelay) {
			return nil
		}
	}
}

func consumeRabbitSession(
	ctx context.Context,
	consumer consumerBackend,
	subscription eventbus.Subscription,
	handler eventbus.Handler,
	retryDelay time.Duration,
) (bool, error) {
	processed := false
	for {
		message, err := consumer.Next(ctx)
		if err != nil {
			return processed, errors.Join(
				fmt.Errorf("consume RabbitMQ event for group %q: %w", subscription.Group, err),
				consumer.Requeue(),
			)
		}
		handled := eventbusutil.HandleWithRetry(ctx, retryDelay, message, handler)
		if !handled || ctx.Err() != nil {
			return processed, errors.Join(ctx.Err(), consumer.Requeue())
		}
		if err := consumer.Ack(); err != nil {
			return processed, fmt.Errorf(
				"ack RabbitMQ event for group %q: %w",
				subscription.Group,
				err,
			)
		}
		processed = true
	}
}

func waitForRabbitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Connector) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.lifecycleMu.Lock()
		c.closed.Store(true)
		if c.cancel != nil {
			c.cancel()
		}
		c.lifecycleMu.Unlock()

		waited := make(chan struct{})
		go func() {
			c.consumers.Wait()
			close(waited)
		}()

		var closeErrors []error
		timer := time.NewTimer(c.config.ShutdownTimeout)
		select {
		case <-waited:
			timer.Stop()
		case <-timer.C:
			closeErrors = append(closeErrors, errors.New(
				"RabbitMQ event bus shutdown timed out",
			))
		}
		if c.producer != nil {
			closeErrors = append(closeErrors, c.producer.Close())
		}
		c.closeErr = errors.Join(closeErrors...)
	})
	return c.closeErr
}

type rabbitConnection struct {
	connection *amqp.Connection
	publisher  *amqp.Channel
	exchange   string
	publishMu  sync.Mutex
	closeOnce  sync.Once
	closeErr   error
}

func newRabbitConnection(
	connection *amqp.Connection,
	exchange string,
) (*rabbitConnection, error) {
	publisher, err := connection.Channel()
	if err != nil {
		return nil, fmt.Errorf("open RabbitMQ publisher channel: %w", err)
	}
	if err := inspectExchange(publisher, exchange); err != nil {
		return nil, errors.Join(
			fmt.Errorf("inspect RabbitMQ event exchange: %w", err),
			publisher.Close(),
		)
	}
	if err := publisher.Confirm(false); err != nil {
		return nil, errors.Join(
			fmt.Errorf("enable RabbitMQ publisher confirms: %w", err),
			publisher.Close(),
		)
	}
	return &rabbitConnection{
		connection: connection,
		publisher:  publisher,
		exchange:   exchange,
	}, nil
}

func inspectExchange(channel *amqp.Channel, exchange string) error {
	return channel.ExchangeDeclarePassive(
		exchange,
		"topic",
		true,
		false,
		false,
		false,
		nil,
	)
}

func (c *rabbitConnection) Ping(context.Context) error {
	channel, err := c.connection.Channel()
	if err != nil {
		return err
	}
	if err := inspectExchange(channel, c.exchange); err != nil {
		return errors.Join(err, channel.Close())
	}
	return channel.Close()
}

func (c *rabbitConnection) Publish(
	ctx context.Context,
	message eventbus.Message,
) error {
	publishing, err := messageToPublishing(message)
	if err != nil {
		return err
	}

	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	confirmation, err := c.publisher.PublishWithDeferredConfirmWithContext(
		ctx,
		c.exchange,
		message.Topic,
		false,
		false,
		publishing,
	)
	if err != nil {
		return err
	}
	if confirmation == nil {
		return errors.New("RabbitMQ publisher confirm is unavailable")
	}
	acknowledged, err := confirmation.WaitContext(ctx)
	if err != nil {
		return err
	}
	if !acknowledged {
		return errors.New("RabbitMQ broker rejected publishing")
	}
	return nil
}

func messageToPublishing(
	message eventbus.Message,
) (amqp.Publishing, error) {
	headers := make(amqp.Table, len(message.Headers)+1)
	for key, value := range message.Headers {
		if key == KeyHeader {
			return amqp.Publishing{}, fmt.Errorf(
				"RabbitMQ header %q is reserved",
				KeyHeader,
			)
		}
		headers[key] = append([]byte(nil), value...)
	}
	if len(message.Key) > 0 {
		headers[KeyHeader] = append([]byte(nil), message.Key...)
	}
	return amqp.Publishing{
		Headers:      headers,
		ContentType:  "application/octet-stream",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now().UTC(),
		Body:         append([]byte(nil), message.Body...),
	}, nil
}

func (c *rabbitConnection) OpenConsumer(
	ctx context.Context,
	subscription eventbus.Subscription,
) (consumerBackend, error) {
	channel, err := c.connection.Channel()
	if err != nil {
		return nil, err
	}
	if _, err := channel.QueueInspect(subscription.Group); err != nil {
		return nil, errors.Join(err, channel.Close())
	}
	if err := channel.Qos(1, 0, false); err != nil {
		return nil, errors.Join(err, channel.Close())
	}
	deliveries, err := channel.ConsumeWithContext(
		ctx,
		subscription.Group,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return nil, errors.Join(err, channel.Close())
	}
	topics := make(map[string]struct{}, len(subscription.Topics))
	for _, topic := range subscription.Topics {
		topics[topic] = struct{}{}
	}
	return &rabbitConsumer{
		channel:    channel,
		deliveries: deliveries,
		topics:     topics,
	}, nil
}

func (c *rabbitConnection) Close() error {
	c.closeOnce.Do(func() {
		c.publishMu.Lock()
		defer c.publishMu.Unlock()
		c.closeErr = errors.Join(
			c.publisher.Close(),
			c.connection.Close(),
		)
	})
	return c.closeErr
}

type rabbitConsumer struct {
	channel    *amqp.Channel
	deliveries <-chan amqp.Delivery
	topics     map[string]struct{}
	current    *amqp.Delivery
}

func (c *rabbitConsumer) Next(
	ctx context.Context,
) (eventbus.Message, error) {
	select {
	case <-ctx.Done():
		return eventbus.Message{}, ctx.Err()
	case delivery, open := <-c.deliveries:
		if !open {
			if err := ctx.Err(); err != nil {
				return eventbus.Message{}, err
			}
			return eventbus.Message{}, errors.New(
				"RabbitMQ delivery channel was closed",
			)
		}
		c.current = &delivery
		if _, exists := c.topics[delivery.RoutingKey]; !exists {
			return eventbus.Message{}, fmt.Errorf(
				"RabbitMQ queue delivered unexpected routing key %q",
				delivery.RoutingKey,
			)
		}
		return deliveryToMessage(delivery)
	}
}

func deliveryToMessage(
	delivery amqp.Delivery,
) (eventbus.Message, error) {
	message := eventbus.Message{
		Topic: delivery.RoutingKey,
		Body:  append([]byte(nil), delivery.Body...),
	}
	if len(delivery.Headers) > 0 {
		message.Headers = make(
			map[string][]byte,
			len(delivery.Headers),
		)
	}
	for key, rawValue := range delivery.Headers {
		value, ok := rawValue.([]byte)
		if !ok {
			return eventbus.Message{}, fmt.Errorf(
				"RabbitMQ event header %q has unsupported type %T",
				key,
				rawValue,
			)
		}
		if key == KeyHeader {
			message.Key = append([]byte(nil), value...)
			continue
		}
		message.Headers[key] = append([]byte(nil), value...)
	}
	return message, nil
}

func (c *rabbitConsumer) Ack() error {
	if c.current == nil {
		return errors.New("RabbitMQ event bus consumer has no current delivery")
	}
	err := c.current.Ack(false)
	c.current = nil
	return err
}

func (c *rabbitConsumer) Requeue() error {
	if c.current == nil {
		return nil
	}
	err := c.current.Nack(false, true)
	c.current = nil
	return err
}

func (c *rabbitConsumer) Close() error {
	return c.channel.Close()
}

var _ eventbus.Factory = Factory{}
var _ eventbus.Connector = (*Connector)(nil)
