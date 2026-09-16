package domainevent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/domainevent"
)

func TestEnvelopeMessageRoundTrip(t *testing.T) {
	occurredAt := time.Date(2026, 8, 27, 10, 0, 0, 0, time.FixedZone("test", 3*60*60))
	event, err := domainevent.New("resource.updated", 2, occurredAt, struct {
		ResourceID int64 `json:"resource_id"`
	}{42})
	if err != nil {
		t.Fatal(err)
	}
	message, err := domainevent.Message(event, []byte("42"))
	if err != nil {
		t.Fatal(err)
	}
	if message.Topic != event.Name || string(message.Key) != "42" || string(message.Headers[domainevent.HeaderMessageID]) != string(event.ID) {
		t.Fatalf("message = %#v", message)
	}
	decoded, err := domainevent.Decode(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != event.ID || decoded.Name != event.Name || !decoded.OccurredAt.Equal(occurredAt) {
		t.Fatalf("decoded event = %#v", decoded)
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["resource_id"] != float64(42) {
		t.Fatalf("payload = %#v", payload)
	}
}
