package rabbiteventbus

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/vernal96/go-cms-kernel/eventbus"
)

type fakeProducer struct {
	mu       sync.Mutex
	messages []eventbus.Message
	pingErr  error
	sendErr  error
	closed   atomic.Bool
}

func (p *fakeProducer) Ping(context.Context) error {
	return p.pingErr
}

func (p *fakeProducer) Publish(
	_ context.Context,
	message eventbus.Message,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, message)
	return p.sendErr
}

func (p *fakeProducer) Close() error {
	p.closed.Store(true)
	return nil
}

type fakeConsumer struct {
	messages []eventbus.Message
	index    int
	nextErr  error
	acks     atomic.Int32
	requeues atomic.Int32
	closed   atomic.Bool
}

func (c *fakeConsumer) Next(
	ctx context.Context,
) (eventbus.Message, error) {
	if c.nextErr != nil {
		err := c.nextErr
		c.nextErr = nil
		return eventbus.Message{}, err
	}
	if c.index < len(c.messages) {
		message := c.messages[c.index]
		c.index++
		return message, nil
	}
	<-ctx.Done()
	return eventbus.Message{}, ctx.Err()
}

func (c *fakeConsumer) Ack() error {
	c.acks.Add(1)
	return nil
}

func (c *fakeConsumer) Requeue() error {
	c.requeues.Add(1)
	return nil
}

func (c *fakeConsumer) Close() error {
	c.closed.Store(true)
	return nil
}

func validConfig() Config {
	return Config{
		URL:                "amqp://cms:secret@localhost:5672/cms",
		Exchange:           "cms.events",
		DialTimeout:        time.Second,
		ConsumerRetryDelay: time.Millisecond,
		ShutdownTimeout:    time.Second,
		ReconnectAttempts:  3,
		ReconnectDelay:     time.Millisecond,
	}
}

func TestConnectorPublishesClonedMessageAndReservesKeyHeader(t *testing.T) {
	producer := &fakeProducer{}
	connector := newConnector(validConfig(), producer, nil)
	message := eventbus.Message{
		Topic: "cms.resource.created",
		Key:   []byte("42"),
		Body:  []byte("payload"),
		Headers: map[string][]byte{
			"content-type": []byte("application/json"),
		},
	}
	if err := connector.Publish(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	message.Body[0] = 'x'
	producer.mu.Lock()
	published := producer.messages[0]
	producer.mu.Unlock()
	if string(published.Body) != "payload" {
		t.Fatalf("published message = %#v", published)
	}

	err := connector.Publish(context.Background(), eventbus.Message{
		Topic:   "topic",
		Headers: map[string][]byte{KeyHeader: []byte("forbidden")},
	})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved header error = %v", err)
	}

	producer.sendErr = errors.New("publisher confirm failed")
	if err := connector.Publish(
		context.Background(),
		eventbus.Message{Topic: "topic"},
	); err == nil || !strings.Contains(err.Error(), "publisher confirm failed") {
		t.Fatalf("publish confirmation error = %v", err)
	}
}

func TestConnectorRetriesAndAcknowledgesOnlySuccessfulHandler(
	t *testing.T,
) {
	producer := &fakeProducer{}
	consumer := &fakeConsumer{messages: []eventbus.Message{{
		Topic: "cms.resource.created",
		Body:  []byte("payload"),
	}}}
	connector := newConnector(
		validConfig(),
		producer,
		func(
			context.Context,
			eventbus.Subscription,
		) (consumerBackend, error) {
			return consumer, nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	attempts := atomic.Int32{}
	handled := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		result <- connector.Consume(
			ctx,
			eventbus.Subscription{
				Topics: []string{"cms.resource.created"},
				Group:  "cms-indexer",
			},
			func(_ context.Context, message eventbus.Message) error {
				attempt := attempts.Add(1)
				message.Body[0] = 'x'
				if attempt < 3 {
					return errors.New("retry")
				}
				handled <- struct{}{}
				return nil
			},
		)
	}()

	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("handler did not succeed")
	}
	deadline := time.Now().Add(time.Second)
	for consumer.acks.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("consume did not stop")
	}
	if attempts.Load() != 3 ||
		consumer.acks.Load() != 1 ||
		consumer.requeues.Load() != 1 ||
		!consumer.closed.Load() {
		t.Fatalf(
			"attempts=%d acks=%d requeues=%d closed=%t",
			attempts.Load(),
			consumer.acks.Load(),
			consumer.requeues.Load(),
			consumer.closed.Load(),
		)
	}
}

func TestConnectorRequeuesWhenCanceledDuringHandling(t *testing.T) {
	consumer := &fakeConsumer{messages: []eventbus.Message{{
		Topic: "cms.resource.created",
	}}}
	connector := newConnector(
		validConfig(),
		&fakeProducer{},
		func(
			context.Context,
			eventbus.Subscription,
		) (consumerBackend, error) {
			return consumer, nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	err := connector.Consume(
		ctx,
		eventbus.Subscription{
			Topics: []string{"cms.resource.created"},
			Group:  "cms-indexer",
		},
		func(context.Context, eventbus.Message) error {
			cancel()
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if consumer.acks.Load() != 0 || consumer.requeues.Load() != 1 {
		t.Fatalf(
			"acks=%d requeues=%d",
			consumer.acks.Load(),
			consumer.requeues.Load(),
		)
	}
}

func TestConnectorReopensConsumerAfterBrokerError(t *testing.T) {
	failed := &fakeConsumer{nextErr: errors.New("broker delivery failed")}
	recovered := &fakeConsumer{messages: []eventbus.Message{{Topic: "topic", Body: []byte("recovered")}}}
	var opened atomic.Int32
	connector := newConnector(
		validConfig(),
		&fakeProducer{},
		func(
			context.Context,
			eventbus.Subscription,
		) (consumerBackend, error) {
			if opened.Add(1) == 1 {
				return failed, nil
			}
			return recovered, nil
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	err := connector.Consume(ctx, eventbus.Subscription{Topics: []string{"topic"}, Group: "group"}, func(_ context.Context, message eventbus.Message) error {
		if string(message.Body) != "recovered" {
			t.Fatalf("message body = %q", message.Body)
		}
		cancel()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 2 || failed.requeues.Load() != 1 || !failed.closed.Load() || recovered.requeues.Load() != 1 || !recovered.closed.Load() {
		t.Fatalf(
			"opened=%d failed requeues=%d closed=%t recovered requeues=%d closed=%t",
			opened.Load(), failed.requeues.Load(), failed.closed.Load(), recovered.requeues.Load(), recovered.closed.Load(),
		)
	}
}

func TestRabbitMessageMapping(t *testing.T) {
	message := eventbus.Message{
		Topic: "cms.resource.created",
		Key:   []byte("42"),
		Body:  []byte("payload"),
		Headers: map[string][]byte{
			"content-type": []byte("application/json"),
		},
	}
	publishing, err := messageToPublishing(message)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := deliveryToMessage(amqp.Delivery{
		RoutingKey: message.Topic,
		Headers:    publishing.Headers,
		Body:       publishing.Body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if actual.Topic != message.Topic ||
		!bytes.Equal(actual.Key, message.Key) ||
		!bytes.Equal(actual.Body, message.Body) ||
		!bytes.Equal(
			actual.Headers["content-type"],
			message.Headers["content-type"],
		) {
		t.Fatalf("mapped message = %#v", actual)
	}

	if _, err := deliveryToMessage(amqp.Delivery{
		Headers: amqp.Table{"unsupported": int32(1)},
	}); err == nil {
		t.Fatal("unsupported RabbitMQ header was accepted")
	}

	deliveries := make(chan amqp.Delivery, 1)
	deliveries <- amqp.Delivery{RoutingKey: "unexpected.topic"}
	consumer := &rabbitConsumer{
		deliveries: deliveries,
		topics:     map[string]struct{}{"expected.topic": {}},
	}
	if _, err := consumer.Next(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "unexpected routing key") {
		t.Fatalf("unexpected routing key error = %v", err)
	}
}

func TestConnectorCloseStopsConsumers(t *testing.T) {
	producer := &fakeProducer{}
	consumer := &fakeConsumer{}
	connector := newConnector(
		validConfig(),
		producer,
		func(
			context.Context,
			eventbus.Subscription,
		) (consumerBackend, error) {
			return consumer, nil
		},
	)
	result := make(chan error, 1)
	go func() {
		result <- connector.Consume(
			context.Background(),
			eventbus.Subscription{
				Topics: []string{"topic"},
				Group:  "group",
			},
			func(context.Context, eventbus.Message) error { return nil },
		)
	}()
	time.Sleep(10 * time.Millisecond)
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if !producer.closed.Load() || !consumer.closed.Load() {
		t.Fatal("connector dependencies were not closed")
	}
	if !errors.Is(
		connector.Ping(context.Background()),
		eventbus.ErrClosed,
	) {
		t.Fatal("closed connector accepted ping")
	}
	if !errors.Is(
		connector.Publish(
			context.Background(),
			eventbus.Message{Topic: "topic"},
		),
		eventbus.ErrClosed,
	) {
		t.Fatal("closed connector accepted publish")
	}
	if !errors.Is(
		connector.Consume(
			context.Background(),
			eventbus.Subscription{
				Topics: []string{"topic"},
				Group:  "group",
			},
			func(context.Context, eventbus.Message) error { return nil },
		),
		eventbus.ErrClosed,
	) {
		t.Fatal("closed connector accepted consume")
	}
}

func TestConnectorValidatesAPIInputs(t *testing.T) {
	connector := newConnector(validConfig(), &fakeProducer{}, nil)
	validSubscription := eventbus.Subscription{
		Topics: []string{"topic"},
		Group:  "group",
	}
	handler := func(context.Context, eventbus.Message) error { return nil }

	if err := connector.Ping(nil); err == nil {
		t.Fatal("nil ping context was accepted")
	}
	if err := connector.Publish(
		nil,
		eventbus.Message{Topic: "topic"},
	); err == nil {
		t.Fatal("nil publish context was accepted")
	}
	if err := connector.Consume(
		nil,
		validSubscription,
		handler,
	); err == nil {
		t.Fatal("nil consume context was accepted")
	}
	if err := connector.Publish(
		context.Background(),
		eventbus.Message{},
	); err == nil {
		t.Fatal("empty publish topic was accepted")
	}
	if err := connector.Consume(
		context.Background(),
		validSubscription,
		nil,
	); err == nil {
		t.Fatal("nil handler was accepted")
	}
	for _, subscription := range []eventbus.Subscription{
		{Group: "group"},
		{Topics: []string{"topic"}},
		{Topics: []string{"topic", "topic"}, Group: "group"},
	} {
		if err := connector.Consume(
			context.Background(),
			subscription,
			handler,
		); err == nil {
			t.Fatalf("invalid subscription was accepted: %#v", subscription)
		}
	}

	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if err := connector.Consume(
		canceledContext,
		validSubscription,
		handler,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled context error = %v", err)
	}
}

func TestConnectorSupportsConcurrentPublishAndClose(t *testing.T) {
	connector := newConnector(validConfig(), &fakeProducer{}, nil)
	start := make(chan struct{})
	results := make(chan error, 32)
	var calls sync.WaitGroup
	for range 32 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			<-start
			results <- connector.Publish(
				context.Background(),
				eventbus.Message{Topic: "topic"},
			)
		}()
	}
	close(start)
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}
	calls.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, eventbus.ErrClosed) {
			t.Fatalf("concurrent publish error = %v", err)
		}
	}
	if err := connector.Close(); err != nil {
		t.Fatalf("second close error = %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "invalid URL",
			mutate: func(config *Config) {
				config.URL = "http://localhost"
			},
		},
		{
			name: "empty exchange",
			mutate: func(config *Config) {
				config.Exchange = ""
			},
		},
		{
			name: "invalid retry delay",
			mutate: func(config *Config) {
				config.ConsumerRetryDelay = 0
			},
		},
		{
			name: "invalid reconnect attempts",
			mutate: func(config *Config) {
				config.ReconnectAttempts = 0
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			config := validConfig()
			testCase.mutate(&config)
			if _, err := normalizeConfig(config); err == nil {
				t.Fatal("invalid config was accepted")
			}
		})
	}
}
