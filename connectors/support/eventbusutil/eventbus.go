package eventbusutil

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/eventbus"
)

func ValidateMessage(message eventbus.Message, reservedHeader string) error {
	if strings.TrimSpace(message.Topic) == "" {
		return errors.New("event bus message topic is empty")
	}
	for key := range message.Headers {
		if strings.TrimSpace(key) == "" {
			return errors.New("event bus message contains empty header name")
		}
		if reservedHeader != "" && key == reservedHeader {
			return fmt.Errorf(
				"event bus message header %q is reserved",
				reservedHeader,
			)
		}
	}
	return nil
}

func ValidateSubscription(subscription eventbus.Subscription) error {
	if strings.TrimSpace(subscription.Group) == "" {
		return errors.New("event bus subscription group is empty")
	}
	if len(subscription.Topics) == 0 {
		return errors.New("event bus subscription topics are empty")
	}

	topics := make(map[string]struct{}, len(subscription.Topics))
	for index, topic := range subscription.Topics {
		topic = strings.TrimSpace(topic)
		if topic == "" {
			return fmt.Errorf(
				"event bus subscription topic at index %d is empty",
				index,
			)
		}
		if _, exists := topics[topic]; exists {
			return fmt.Errorf(
				"event bus subscription topic %q is duplicated",
				topic,
			)
		}
		topics[topic] = struct{}{}
	}
	return nil
}

func CloneMessage(message eventbus.Message) eventbus.Message {
	clone := eventbus.Message{
		Topic: message.Topic,
		Key:   append([]byte(nil), message.Key...),
		Body:  append([]byte(nil), message.Body...),
	}
	if message.Headers != nil {
		clone.Headers = make(map[string][]byte, len(message.Headers))
		for key, value := range message.Headers {
			clone.Headers[key] = append([]byte(nil), value...)
		}
	}
	return clone
}

func CloneSubscription(
	subscription eventbus.Subscription,
) eventbus.Subscription {
	return eventbus.Subscription{
		Topics: append([]string(nil), subscription.Topics...),
		Group:  subscription.Group,
	}
}

func HandleWithRetry(
	ctx context.Context,
	retryDelay time.Duration,
	message eventbus.Message,
	handler eventbus.Handler,
) bool {
	for {
		if err := handler(ctx, CloneMessage(message)); err == nil {
			return true
		}

		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}
