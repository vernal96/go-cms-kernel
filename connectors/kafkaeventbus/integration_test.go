package kafkaeventbus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"github.com/vernal96/go-cms-kernel/eventbus"
)

func TestKafkaIntegrationPublishConsume(t *testing.T) {
	brokersValue := os.Getenv("CMS_TEST_KAFKA_BROKERS")
	if brokersValue == "" {
		t.Skip("set CMS_TEST_KAFKA_BROKERS to run Kafka integration test")
	}
	brokers := strings.Split(brokersValue, ",")
	topic := fmt.Sprintf("cms-integration-%d", time.Now().UnixNano())
	groups := []string{topic + "-consumer-a", topic + "-consumer-b"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	t.Cleanup(func() {
		deleteRequest := kmsg.NewPtrDeleteTopicsRequest()
		deleteRequest.TopicNames = []string{topic}
		_, _ = deleteRequest.RequestWith(context.Background(), admin)
	})

	connector, err := New(ctx, Config{
		Brokers:            brokers,
		ClientID:           "cms-integration",
		DialTimeout:        5 * time.Second,
		ConsumerRetryDelay: 10 * time.Millisecond,
		ShutdownTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connector.Close() }()
	if err := connector.Ping(ctx); err != nil {
		t.Fatal(err)
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
	consumerResults := make(chan error, len(groups))
	received := []chan eventbus.Message{
		make(chan eventbus.Message, 1),
		make(chan eventbus.Message, 1),
	}
	commitProof := []chan struct{}{
		make(chan struct{}),
		make(chan struct{}),
	}
	var proofOnce [2]sync.Once
	var retryAttempts atomic.Int32
	for index, group := range groups {
		index := index
		group := group
		go func() {
			consumerResults <- connector.Consume(
				consumerContext,
				eventbus.Subscription{
					Topics: []string{topic},
					Group:  group,
				},
				func(
					_ context.Context,
					message eventbus.Message,
				) error {
					if string(message.Body) == "commit-probe" {
						proofOnce[index].Do(func() {
							close(commitProof[index])
						})
						return errors.New("hold probe without commit")
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
	for index := range groups {
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
		Body:  []byte("commit-probe"),
	}); err != nil {
		t.Fatal(err)
	}
	for index := range groups {
		select {
		case <-commitProof[index]:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}

	stopConsumers()
	for range groups {
		if err := <-consumerResults; err != nil {
			t.Fatal(err)
		}
	}
}
