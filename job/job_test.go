package job_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/job"
)

type fakeBus struct {
	published    []eventbus.Message
	subscription eventbus.Subscription
	handle       []eventbus.Message
}

func (b *fakeBus) Publish(_ context.Context, message eventbus.Message) error {
	b.published = append(b.published, message)
	return nil
}
func (b *fakeBus) Consume(ctx context.Context, subscription eventbus.Subscription, handler eventbus.Handler) error {
	b.subscription = subscription
	for _, message := range b.handle {
		if err := handler(ctx, message); err != nil {
			return err
		}
	}
	return nil
}

func TestRegistryDispatcherAndRunner(t *testing.T) {
	registry := job.NewRegistry()
	wantErr := errors.New("handler failed")
	seen := make([]string, 0, 2)
	if err := registry.Register("test.execute", func(_ context.Context, item job.Envelope) error {
		seen = append(seen, string(item.ID))
		return wantErr
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("test.execute", func(context.Context, job.Envelope) error { return nil }); err == nil {
		t.Fatal("duplicate handler registration succeeded")
	}
	item, err := job.New("test.execute", 1, struct {
		Value string `json:"value"`
	}{"explicit"})
	if err != nil {
		t.Fatal(err)
	}
	bus := &fakeBus{}
	dispatcher, err := job.NewDispatcher(bus)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if len(bus.published) != 1 || bus.published[0].Topic != "job.test.execute" || string(bus.published[0].Key) != string(item.ID) {
		t.Fatalf("published = %#v", bus.published)
	}
	var decoded job.Envelope
	if err := json.Unmarshal(bus.published[0].Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ID != item.ID {
		t.Fatalf("stable job ID = %q, want %q", decoded.ID, item.ID)
	}
	runner, err := job.NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Handle(context.Background(), bus.published[0]); !errors.Is(err, wantErr) {
		t.Fatalf("handler error = %v", err)
	}
	if err := registry.Handle(context.Background(), mustJob(t, "unknown.execute")); err == nil {
		t.Fatal("unknown job was accepted")
	}
	bus.handle = []eventbus.Message{bus.published[0], bus.published[0]}
	if err := runner.Run(context.Background(), bus, "test-workers"); !errors.Is(err, wantErr) {
		t.Fatalf("consumer handler error = %v", err)
	}
	if len(seen) != 2 || seen[0] != seen[1] || seen[0] != string(item.ID) {
		t.Fatalf("duplicate delivery IDs = %#v", seen)
	}
}

func TestScopedJobPreservesAndValidatesScope(t *testing.T) {
	t.Parallel()
	item, err := job.NewScoped("test.scoped", 1, "42", struct{}{})
	if err != nil || item.ScopeID != "42" {
		t.Fatalf("scoped job = %#v, %v", item, err)
	}
	item.ScopeID = " 42"
	if err := item.Validate(); err == nil {
		t.Fatal("invalid scoped job was accepted")
	}
}

func TestRunnerAcknowledgesOnlyExplicitlyObsoleteJobs(t *testing.T) {
	registry := job.NewRegistry()
	if err := registry.Register("test.obsolete", func(context.Context, job.Envelope) error {
		return fmt.Errorf("scope disappeared: %w", job.ErrObsolete)
	}); err != nil {
		t.Fatal(err)
	}
	runner, err := job.NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	item, _ := job.NewScoped("test.obsolete", 1, "42", struct{}{})
	body, _ := json.Marshal(item)
	if err := runner.Handle(context.Background(), eventbus.Message{Topic: job.Topic(item.Name), Body: body}); err != nil {
		t.Fatalf("obsolete job was not acknowledged: %v", err)
	}
	if err := runner.Handle(context.Background(), eventbus.Message{Topic: job.Topic(item.Name), Body: []byte("not-json")}); err == nil {
		t.Fatal("malformed job was acknowledged")
	}
}

func mustJob(t *testing.T, name string) job.Envelope {
	t.Helper()
	item, err := job.New(name, 1, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	return item
}
