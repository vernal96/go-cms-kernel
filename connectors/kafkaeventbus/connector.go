package kafkaeventbus

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/vernal96/go-cms-kernel/connectors/support/eventbusutil"
	"github.com/vernal96/go-cms-kernel/eventbus"
)

type Config struct {
	Brokers            []string
	ClientID           string
	Username           string
	Password           string
	TLSEnabled         bool
	TLSServerName      string
	TLSInsecure        bool
	DialTimeout        time.Duration
	ConsumerRetryDelay time.Duration
	ShutdownTimeout    time.Duration
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
	Commit(context.Context) error
	Release()
	Close()
}

type consumerFactory func(eventbus.Subscription) (consumerBackend, error)

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
		return nil, errors.New("Kafka event bus context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	producerClient, err := kgo.NewClient(clientOptions(normalized)...)
	if err != nil {
		return nil, fmt.Errorf("open Kafka event bus producer: %w", err)
	}

	producer := &kafkaProducer{client: producerClient}
	return newConnector(
		normalized,
		producer,
		func(subscription eventbus.Subscription) (consumerBackend, error) {
			options := append(
				clientOptions(normalized),
				kgo.ConsumerGroup(subscription.Group),
				kgo.ConsumeTopics(subscription.Topics...),
				kgo.DisableAutoCommit(),
				kgo.BlockRebalanceOnPoll(),
			)
			client, err := kgo.NewClient(options...)
			if err != nil {
				return nil, err
			}
			return &kafkaConsumer{client: client}, nil
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
	brokers := make([]string, 0, len(config.Brokers))
	seenBrokers := make(map[string]struct{}, len(config.Brokers))
	for index, broker := range config.Brokers {
		broker = strings.TrimSpace(broker)
		if broker == "" {
			return Config{}, fmt.Errorf(
				"Kafka event bus broker at index %d is empty",
				index,
			)
		}
		if _, exists := seenBrokers[broker]; exists {
			return Config{}, fmt.Errorf(
				"Kafka event bus broker %q is duplicated",
				broker,
			)
		}
		seenBrokers[broker] = struct{}{}
		brokers = append(brokers, broker)
	}
	if len(brokers) == 0 {
		return Config{}, errors.New("Kafka event bus brokers are empty")
	}

	config.Brokers = brokers
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.Username = strings.TrimSpace(config.Username)
	config.TLSServerName = strings.TrimSpace(config.TLSServerName)
	if config.ClientID == "" {
		return Config{}, errors.New("Kafka event bus client ID is empty")
	}
	if (config.Username == "") != (config.Password == "") {
		return Config{}, errors.New(
			"Kafka event bus username and password must be configured together",
		)
	}
	if config.DialTimeout <= 0 {
		return Config{}, errors.New(
			"Kafka event bus dial timeout must be positive",
		)
	}
	if config.ConsumerRetryDelay <= 0 {
		return Config{}, errors.New(
			"Kafka event bus consumer retry delay must be positive",
		)
	}
	if config.ShutdownTimeout <= 0 {
		return Config{}, errors.New(
			"Kafka event bus shutdown timeout must be positive",
		)
	}
	if !config.TLSEnabled &&
		(config.TLSServerName != "" || config.TLSInsecure) {
		return Config{}, errors.New(
			"Kafka event bus TLS options require TLS to be enabled",
		)
	}
	return config, nil
}

func clientOptions(config Config) []kgo.Opt {
	options := []kgo.Opt{
		kgo.SeedBrokers(config.Brokers...),
		kgo.ClientID(config.ClientID),
		kgo.DialTimeout(config.DialTimeout),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.AllowAutoTopicCreation(),
	}
	if config.Username != "" {
		options = append(options, kgo.SASL(plain.Auth{
			User: config.Username,
			Pass: config.Password,
		}.AsMechanism()))
	}
	if config.TLSEnabled {
		options = append(options, kgo.DialTLSConfig(&tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         config.TLSServerName,
			InsecureSkipVerify: config.TLSInsecure,
		}))
	}
	return options
}

func (c *Connector) Ping(ctx context.Context) error {
	if ctx == nil {
		return errors.New("Kafka event bus ping context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.producer == nil || c.closed.Load() {
		return eventbus.ErrClosed
	}
	if err := c.producer.Ping(ctx); err != nil {
		return fmt.Errorf("ping Kafka event bus: %w", err)
	}
	return nil
}

func (c *Connector) Publish(
	ctx context.Context,
	message eventbus.Message,
) error {
	if ctx == nil {
		return errors.New("Kafka event bus publish context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := eventbusutil.ValidateMessage(message, ""); err != nil {
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
			"publish Kafka event to topic %q: %w",
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
		return errors.New("Kafka event bus consume context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if handler == nil {
		return errors.New("Kafka event bus handler is nil")
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
	for {
		consumer, err := c.openConsumer(subscription)
		if err != nil {
			if consumeContext.Err() != nil {
				return nil
			}
		} else {
			err = consumeKafkaSession(
				consumeContext,
				consumer,
				subscription,
				handler,
				c.config.ConsumerRetryDelay,
			)
			consumer.Close()
			if consumeContext.Err() != nil {
				return nil
			}
		}
		if !waitForKafkaRetry(consumeContext, c.config.ConsumerRetryDelay) {
			return nil
		}
	}
}

func consumeKafkaSession(
	ctx context.Context,
	consumer consumerBackend,
	subscription eventbus.Subscription,
	handler eventbus.Handler,
	retryDelay time.Duration,
) error {
	for {
		message, err := consumer.Next(ctx)
		if err != nil {
			consumer.Release()
			return fmt.Errorf(
				"consume Kafka event for group %q: %w",
				subscription.Group,
				err,
			)
		}
		handled := eventbusutil.HandleWithRetry(ctx, retryDelay, message, handler)
		if !handled {
			consumer.Release()
			return ctx.Err()
		}
		if ctx.Err() != nil {
			consumer.Release()
			return ctx.Err()
		}
		if err := consumer.Commit(ctx); err != nil {
			consumer.Release()
			return fmt.Errorf(
				"commit Kafka event for group %q: %w",
				subscription.Group,
				err,
			)
		}
		consumer.Release()
	}
}

func waitForKafkaRetry(ctx context.Context, delay time.Duration) bool {
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
				"Kafka event bus shutdown timed out",
			))
		}
		if c.producer != nil {
			closeErrors = append(closeErrors, c.producer.Close())
		}
		c.closeErr = errors.Join(closeErrors...)
	})
	return c.closeErr
}

type kafkaProducer struct {
	client *kgo.Client
}

func (p *kafkaProducer) Ping(ctx context.Context) error {
	return p.client.Ping(ctx)
}

func (p *kafkaProducer) Publish(
	ctx context.Context,
	message eventbus.Message,
) error {
	headers := make([]kgo.RecordHeader, 0, len(message.Headers))
	for key, value := range message.Headers {
		headers = append(headers, kgo.RecordHeader{
			Key:   key,
			Value: append([]byte(nil), value...),
		})
	}
	return p.client.ProduceSync(ctx, &kgo.Record{
		Topic:   message.Topic,
		Key:     append([]byte(nil), message.Key...),
		Value:   append([]byte(nil), message.Body...),
		Headers: headers,
	}).FirstErr()
}

func (p *kafkaProducer) Close() error {
	p.client.Close()
	return nil
}

type kafkaConsumer struct {
	client  *kgo.Client
	current *kgo.Record
}

func (c *kafkaConsumer) Next(
	ctx context.Context,
) (eventbus.Message, error) {
	for {
		fetches := c.client.PollRecords(ctx, 1)
		if fetchErrors := fetches.Errors(); len(fetchErrors) > 0 {
			errorsList := make([]error, 0, len(fetchErrors))
			for _, fetchError := range fetchErrors {
				errorsList = append(errorsList, fmt.Errorf(
					"fetch topic %q partition %d: %w",
					fetchError.Topic,
					fetchError.Partition,
					fetchError.Err,
				))
			}
			return eventbus.Message{}, errors.Join(errorsList...)
		}
		records := fetches.Records()
		if len(records) == 0 {
			if err := ctx.Err(); err != nil {
				return eventbus.Message{}, err
			}
			continue
		}

		c.current = records[0]
		headers := make(
			map[string][]byte,
			len(c.current.Headers),
		)
		for _, header := range c.current.Headers {
			headers[header.Key] = append([]byte(nil), header.Value...)
		}
		return eventbus.Message{
			Topic:   c.current.Topic,
			Key:     append([]byte(nil), c.current.Key...),
			Body:    append([]byte(nil), c.current.Value...),
			Headers: headers,
		}, nil
	}
}

func (c *kafkaConsumer) Commit(ctx context.Context) error {
	if c.current == nil {
		return errors.New("Kafka event bus consumer has no current record")
	}
	return c.client.CommitRecords(ctx, c.current)
}

func (c *kafkaConsumer) Release() {
	c.current = nil
	c.client.AllowRebalance()
}

func (c *kafkaConsumer) Close() {
	c.client.CloseAllowingRebalance()
}

var _ eventbus.Factory = Factory{}
var _ eventbus.Connector = (*Connector)(nil)
