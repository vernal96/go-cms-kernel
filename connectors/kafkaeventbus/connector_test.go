package kafkaeventbus

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	commits  atomic.Int32
	releases atomic.Int32
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

func (c *fakeConsumer) Commit(context.Context) error {
	c.commits.Add(1)
	return nil
}

func (c *fakeConsumer) Release() {
	c.releases.Add(1)
}

func (c *fakeConsumer) Close() {
	c.closed.Store(true)
}

func validConfig() Config {
	return Config{
		Brokers:            []string{"localhost:9092"},
		ClientID:           "cms-test",
		DialTimeout:        time.Second,
		ConsumerRetryDelay: time.Millisecond,
		ShutdownTimeout:    time.Second,
	}
}

func TestConnectorPublishesConfirmedClonedMessage(t *testing.T) {
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
	message.Key[0] = 'x'
	message.Body[0] = 'x'
	message.Headers["content-type"][0] = 'x'

	producer.mu.Lock()
	published := producer.messages[0]
	producer.mu.Unlock()
	if string(published.Key) != "42" ||
		string(published.Body) != "payload" ||
		!bytes.Equal(
			published.Headers["content-type"],
			[]byte("application/json"),
		) {
		t.Fatalf("published message = %#v", published)
	}

	producer.sendErr = errors.New("broker rejected record")
	if err := connector.Publish(
		context.Background(),
		eventbus.Message{Topic: "topic"},
	); err == nil || !strings.Contains(err.Error(), "broker rejected") {
		t.Fatalf("publish error = %v", err)
	}
}

func TestConnectorConsumesRetriesAndCommitsAfterSuccess(t *testing.T) {
	producer := &fakeProducer{}
	consumer := &fakeConsumer{messages: []eventbus.Message{{
		Topic: "cms.resource.created",
		Body:  []byte("payload"),
	}}}
	connector := newConnector(
		validConfig(),
		producer,
		func(eventbus.Subscription) (consumerBackend, error) {
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
	for consumer.commits.Load() != 1 && time.Now().Before(deadline) {
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
		consumer.commits.Load() != 1 ||
		consumer.releases.Load() != 2 ||
		!consumer.closed.Load() {
		t.Fatalf(
			"attempts=%d commits=%d releases=%d closed=%t",
			attempts.Load(),
			consumer.commits.Load(),
			consumer.releases.Load(),
			consumer.closed.Load(),
		)
	}
}

func TestConnectorDoesNotCommitWhenCanceledDuringHandling(t *testing.T) {
	consumer := &fakeConsumer{messages: []eventbus.Message{{
		Topic: "cms.resource.created",
	}}}
	connector := newConnector(
		validConfig(),
		&fakeProducer{},
		func(eventbus.Subscription) (consumerBackend, error) {
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
	if consumer.commits.Load() != 0 || consumer.releases.Load() != 1 {
		t.Fatalf(
			"commits=%d releases=%d",
			consumer.commits.Load(),
			consumer.releases.Load(),
		)
	}
}

func TestConnectorReopensConsumerAfterBrokerError(t *testing.T) {
	failed := &fakeConsumer{nextErr: errors.New("broker fetch failed")}
	recovered := &fakeConsumer{messages: []eventbus.Message{{Topic: "topic", Body: []byte("recovered")}}}
	var opened atomic.Int32
	connector := newConnector(
		validConfig(),
		&fakeProducer{},
		func(eventbus.Subscription) (consumerBackend, error) {
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
	if opened.Load() != 2 || failed.releases.Load() != 1 || !failed.closed.Load() || recovered.releases.Load() != 1 || !recovered.closed.Load() {
		t.Fatalf(
			"opened=%d failed releases=%d closed=%t recovered releases=%d closed=%t",
			opened.Load(), failed.releases.Load(), failed.closed.Load(), recovered.releases.Load(), recovered.closed.Load(),
		)
	}
}

func TestConnectorCloseStopsConsumersAndRejectsOperations(t *testing.T) {
	producer := &fakeProducer{}
	consumer := &fakeConsumer{}
	connector := newConnector(
		validConfig(),
		producer,
		func(eventbus.Subscription) (consumerBackend, error) {
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
		connector.Publish(
			context.Background(),
			eventbus.Message{Topic: "topic"},
		),
		eventbus.ErrClosed,
	) {
		t.Fatal("closed connector accepted publish")
	}
	if !errors.Is(
		connector.Ping(context.Background()),
		eventbus.ErrClosed,
	) {
		t.Fatal("closed connector accepted ping")
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
			name: "empty brokers",
			mutate: func(config *Config) {
				config.Brokers = nil
			},
		},
		{
			name: "empty client ID",
			mutate: func(config *Config) {
				config.ClientID = ""
			},
		},
		{
			name: "partial credentials",
			mutate: func(config *Config) {
				config.Username = "cms"
			},
		},
		{
			name: "invalid retry delay",
			mutate: func(config *Config) {
				config.ConsumerRetryDelay = 0
			},
		},
		{
			name: "TLS options without TLS",
			mutate: func(config *Config) {
				config.TLSServerName = "broker"
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
