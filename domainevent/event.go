package domainevent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/messageid"
)

const (
	HeaderContentType   = "content-type"
	HeaderMessageID     = "x-cms-message-id"
	HeaderEventName     = "x-cms-event-name"
	HeaderSchemaVersion = "x-cms-schema-version"
)

type Envelope struct {
	ID            messageid.ID    `json:"id"`
	Name          string          `json:"name"`
	SchemaVersion int             `json:"schema_version"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload"`
}

func New(name string, schemaVersion int, occurredAt time.Time, payload any) (Envelope, error) {
	id, err := messageid.New()
	if err != nil {
		return Envelope{}, fmt.Errorf("create domain event ID: %w", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("encode domain event payload: %w", err)
	}
	event := Envelope{ID: id, Name: name, SchemaVersion: schemaVersion, OccurredAt: occurredAt.UTC(), Payload: raw}
	if err := event.Validate(); err != nil {
		return Envelope{}, err
	}
	return event, nil
}

func (e Envelope) Validate() error {
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(e.Name) == "" || e.Name != strings.TrimSpace(e.Name) {
		return errors.New("domain event name is invalid")
	}
	if e.SchemaVersion < 1 {
		return errors.New("domain event schema version is invalid")
	}
	if e.OccurredAt.IsZero() {
		return errors.New("domain event occurrence time is empty")
	}
	if len(e.Payload) == 0 || !json.Valid(e.Payload) {
		return errors.New("domain event payload is invalid JSON")
	}
	return nil
}

func Message(e Envelope, key []byte) (eventbus.Message, error) {
	if err := e.Validate(); err != nil {
		return eventbus.Message{}, err
	}
	body, err := json.Marshal(e)
	if err != nil {
		return eventbus.Message{}, fmt.Errorf("encode domain event envelope: %w", err)
	}
	return eventbus.Message{
		Topic: e.Name,
		Key:   append([]byte(nil), key...),
		Body:  body,
		Headers: map[string][]byte{
			HeaderContentType:   []byte("application/json"),
			HeaderMessageID:     []byte(e.ID),
			HeaderEventName:     []byte(e.Name),
			HeaderSchemaVersion: []byte(strconv.Itoa(e.SchemaVersion)),
		},
	}, nil
}

func Decode(_ context.Context, message eventbus.Message) (Envelope, error) {
	var event Envelope
	if err := json.Unmarshal(message.Body, &event); err != nil {
		return Envelope{}, fmt.Errorf("decode domain event envelope: %w", err)
	}
	if err := event.Validate(); err != nil {
		return Envelope{}, err
	}
	if message.Topic != event.Name {
		return Envelope{}, errors.New("domain event topic and name do not match")
	}
	return event, nil
}
