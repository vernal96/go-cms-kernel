package eventbusutil_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/connectors/support/eventbusutil"
	"github.com/vernal96/go-cms-kernel/eventbus"
)

func TestValidationAndCloning(t *testing.T) {
	message := eventbus.Message{
		Topic: "cms.resource.created",
		Key:   []byte("42"),
		Body:  []byte("payload"),
		Headers: map[string][]byte{
			"content-type": []byte("application/json"),
		},
	}
	if err := eventbusutil.ValidateMessage(message, "reserved"); err != nil {
		t.Fatal(err)
	}
	if err := eventbusutil.ValidateSubscription(eventbus.Subscription{
		Topics: []string{"cms.resource.created"},
		Group:  "cms-indexer",
	}); err != nil {
		t.Fatal(err)
	}

	clone := eventbusutil.CloneMessage(message)
	clone.Key[0] = 'x'
	clone.Body[0] = 'x'
	clone.Headers["content-type"][0] = 'x'
	if bytes.Equal(clone.Key, message.Key) ||
		bytes.Equal(clone.Body, message.Body) ||
		bytes.Equal(
			clone.Headers["content-type"],
			message.Headers["content-type"],
		) {
		t.Fatal("message clone shares mutable storage")
	}
}

func TestValidationRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "empty topic",
			err: eventbusutil.ValidateMessage(
				eventbus.Message{},
				"",
			),
		},
		{
			name: "reserved header",
			err: eventbusutil.ValidateMessage(eventbus.Message{
				Topic:   "topic",
				Headers: map[string][]byte{"reserved": nil},
			}, "reserved"),
		},
		{
			name: "empty group",
			err: eventbusutil.ValidateSubscription(
				eventbus.Subscription{Topics: []string{"topic"}},
			),
		},
		{
			name: "duplicate topic",
			err: eventbusutil.ValidateSubscription(
				eventbus.Subscription{
					Topics: []string{"topic", "topic"},
					Group:  "group",
				},
			),
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
}

func TestHandleWithRetryClonesEveryAttemptAndStopsOnCancel(
	t *testing.T,
) {
	message := eventbus.Message{Topic: "topic", Body: []byte("body")}
	attempts := 0
	handled := eventbusutil.HandleWithRetry(
		context.Background(),
		time.Millisecond,
		message,
		func(_ context.Context, candidate eventbus.Message) error {
			attempts++
			candidate.Body[0] = 'x'
			if attempts < 3 {
				return errors.New("retry")
			}
			if string(message.Body) != "body" {
				t.Fatal("handler mutated source message")
			}
			return nil
		},
	)
	if !handled || attempts != 3 {
		t.Fatalf("handled = %t, attempts = %d", handled, attempts)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if eventbusutil.HandleWithRetry(
		ctx,
		time.Hour,
		message,
		func(context.Context, eventbus.Message) error {
			return errors.New("retry")
		},
	) {
		t.Fatal("canceled handler retry succeeded")
	}
}
