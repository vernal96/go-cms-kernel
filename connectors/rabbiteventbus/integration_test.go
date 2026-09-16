package rabbiteventbus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/vernal96/go-cms-kernel/eventbus"
)

func TestRabbitMQIntegrationPublishConsume(t *testing.T) {
	rabbitURL := os.Getenv("CMS_TEST_RABBITMQ_URL")
	if rabbitURL == "" {
		t.Skip(
			"set CMS_TEST_RABBITMQ_URL to run RabbitMQ integration test",
		)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	exchange := "cms.integration." + suffix
	queues := []string{
		"cms-integration-" + suffix + "-a",
		"cms-integration-" + suffix + "-b",
	}
	topic := "cms.resource.created"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fixtureConnection, err := amqp.Dial(rabbitURL)
	if err != nil {
		t.Fatal(err)
	}
	fixtureChannel, err := fixtureConnection.Channel()
	if err != nil {
		_ = fixtureConnection.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, queue := range queues {
			_, _ = fixtureChannel.QueueDelete(queue, false, false, false)
		}
		_ = fixtureChannel.ExchangeDelete(exchange, false, false)
		_ = fixtureChannel.Close()
		_ = fixtureConnection.Close()
	})
	baseConfig := Config{
		URL:                rabbitURL,
		DialTimeout:        5 * time.Second,
		ConsumerRetryDelay: 10 * time.Millisecond,
		ShutdownTimeout:    5 * time.Second,
		ReconnectAttempts:  5,
		ReconnectDelay:     time.Second,
	}
	missingExchangeConfig := baseConfig
	missingExchangeConfig.Exchange = exchange + ".missing"
	if missingConnector, err := New(ctx, missingExchangeConfig); err == nil {
		_ = missingConnector.Close()
		t.Fatal("connector accepted a missing exchange")
	}
	if err := fixtureChannel.ExchangeDeclare(
		exchange,
		"topic",
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	for _, queue := range queues {
		if _, err := fixtureChannel.QueueDeclare(
			queue,
			true,
			false,
			false,
			false,
			nil,
		); err != nil {
			t.Fatal(err)
		}
		if err := fixtureChannel.QueueBind(
			queue,
			topic,
			exchange,
			false,
			nil,
		); err != nil {
			t.Fatal(err)
		}
	}

	config := baseConfig
	config.Exchange = exchange
	connector, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connector.Close() }()
	if err := connector.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := connector.Consume(
		ctx,
		eventbus.Subscription{
			Topics: []string{topic},
			Group:  queues[0] + ".missing",
		},
		func(context.Context, eventbus.Message) error {
			return nil
		},
	); err == nil {
		t.Fatal("connector accepted a missing queue")
	}

	expected := eventbus.Message{
		Topic: topic,
		Key:   []byte("42"),
		Body:  []byte("payload"),
		Headers: map[string][]byte{
			"content-type": []byte("application/json"),
		},
	}
	consumerContext, stopConsumers := context.WithCancel(ctx)
	defer stopConsumers()
	consumerResults := make(chan error, len(queues))
	received := []chan eventbus.Message{
		make(chan eventbus.Message, 1),
		make(chan eventbus.Message, 1),
	}
	ackProof := []chan struct{}{
		make(chan struct{}),
		make(chan struct{}),
	}
	var proofOnce [2]sync.Once
	var retryAttempts atomic.Int32
	for index, queue := range queues {
		index := index
		queue := queue
		go func() {
			consumerResults <- connector.Consume(
				consumerContext,
				eventbus.Subscription{
					Topics: []string{topic},
					Group:  queue,
				},
				func(
					_ context.Context,
					message eventbus.Message,
				) error {
					if string(message.Body) == "ack-probe" {
						proofOnce[index].Do(func() {
							close(ackProof[index])
						})
						return errors.New("hold probe without ack")
					}
					if index == 0 && retryAttempts.Add(1) == 1 {
						return errors.New("retry first delivery")
					}
					received[index] <- message
					return nil
				},
			)
		}()
	}

	if err := connector.Publish(ctx, expected); err != nil {
		t.Fatal(err)
	}
	for index := range queues {
		var actual eventbus.Message
		select {
		case actual = <-received[index]:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if actual.Topic != expected.Topic ||
			string(actual.Key) != string(expected.Key) ||
			string(actual.Body) != string(expected.Body) ||
			string(actual.Headers["content-type"]) !=
				string(expected.Headers["content-type"]) {
			t.Fatalf("received = %#v", actual)
		}
	}
	if retryAttempts.Load() != 2 {
		t.Fatalf("retry attempts = %d", retryAttempts.Load())
	}

	if err := connector.Publish(ctx, eventbus.Message{
		Topic: topic,
		Body:  []byte("ack-probe"),
	}); err != nil {
		t.Fatal(err)
	}
	for index := range queues {
		select {
		case <-ackProof[index]:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}

	stopConsumers()
	for range queues {
		if err := <-consumerResults; err != nil {
			t.Fatal(err)
		}
	}
}
