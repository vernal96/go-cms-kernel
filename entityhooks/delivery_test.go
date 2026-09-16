package entityhooks

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/domainevent"
)

type executionSource struct {
	Source
	completed bool
}

func (*executionSource) Name() string { return "test" }
func (*executionSource) Claim(context.Context, Delivery, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *executionSource) Complete(context.Context, Delivery, string) error {
	s.completed = true
	return nil
}

func TestDeliveryPinsRuntimeThroughCompletion(t *testing.T) {
	registry := NewRegistry(Site, "1")
	key := NewKey[struct{}]("catalog", "catalog.created", Site)
	if err := RegisterAfter(registry.ForModule("catalog", nil), key, "notify", func(context.Context, Delivery, struct{}) error {
		if _, err := registry.Drain(); !errors.Is(err, ErrBusy) {
			t.Fatalf("runtime changed while handler was running: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	source := &executionSource{}
	runner, err := NewRunner([]Source{source}, func(Target) (*Registry, bool) { return registry, true }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	event, err := domainevent.New(key.Name(), 1, time.Now(), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.execute(context.Background(), source, Delivery{Source: source.Name(), Event: event, Target: registry.Targets(key.Name())[0]}); err != nil {
		t.Fatal(err)
	}
	if !source.completed {
		t.Fatal("successful call was not recorded")
	}
	abort, err := registry.Drain()
	if err != nil {
		t.Fatalf("completed delivery retained runtime: %v", err)
	}
	abort()
}
