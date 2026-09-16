package entityhooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vernal96/go-cms-kernel/domainevent"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/messageid"
)

const SourceHeader = "x-cms-entity-hooks-source"

type Call struct {
	Delivery
	Completed bool
}
type PendingTarget struct {
	Name   string
	Target Target
}

// Source belongs to the physical transaction domain of its entity writes.
// Implementations use their database clock for leases and cleanup.
type Source interface {
	Name() string
	Calls(context.Context, messageid.ID) ([]Call, error)
	Claim(context.Context, Delivery, string, time.Duration) (bool, error)
	Complete(context.Context, Delivery, string) error
	Fail(context.Context, Delivery, string, string) error
	Pending(context.Context) ([]PendingTarget, error)
	Cleanup(context.Context, time.Duration, int) (int64, error)
}
type Provider interface{ EntityHookSources() []Source }

type Resolver func(Target) (*Registry, bool)

type Runner struct {
	sources map[string]Source
	resolve Resolver
	logger  *slog.Logger
}

func NewRunner(sources []Source, resolve Resolver, logger *slog.Logger) (*Runner, error) {
	if resolve == nil || logger == nil {
		return nil, errors.New("entity hook runner dependencies are nil")
	}
	r := &Runner{sources: map[string]Source{}, resolve: resolve, logger: logger}
	for _, source := range sources {
		if source == nil || source.Name() == "" {
			return nil, errors.New("invalid entity hook source")
		}
		if _, exists := r.sources[source.Name()]; exists {
			return nil, fmt.Errorf("duplicate entity hook source %q", source.Name())
		}
		r.sources[source.Name()] = source
	}
	return r, nil
}

func (r *Runner) ValidatePending(ctx context.Context) error {
	for _, source := range r.sources {
		pending, err := source.Pending(ctx)
		if err != nil {
			return err
		}
		for _, item := range pending {
			registry, exists := r.resolve(item.Target)
			if !exists || !registry.Has(item.Name, item.Target) {
				return fmt.Errorf("%w: pending %s in source %s for %s/%s (%s %s)", ErrUnavailable, item.Name, source.Name(), item.Target.Module, item.Target.Handler, item.Target.Scope, item.Target.ScopeID)
			}
		}
	}
	return nil
}

func (r *Runner) Handle(ctx context.Context, message eventbus.Message) error {
	sourceName := string(message.Headers[SourceHeader])
	if sourceName == "" {
		return nil
	} // An ordinary domain event without hook recipients.
	source, exists := r.sources[sourceName]
	if !exists {
		return fmt.Errorf("entity hook source %q is unavailable", sourceName)
	}
	event, err := domainevent.Decode(ctx, message)
	if err != nil {
		return err
	}
	calls, err := source.Calls(ctx, event.ID)
	if err != nil {
		return err
	}
	var failures []error
	for _, call := range calls {
		if call.Completed {
			continue
		}
		if err := r.execute(ctx, source, call.Delivery); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (r *Runner) execute(ctx context.Context, source Source, delivery Delivery) error {
	registry, exists := r.resolve(delivery.Target)
	if !exists || !registry.Has(delivery.Event.Name, delivery.Target) {
		return ErrUnavailable
	}
	release, err := registry.Acquire()
	if err != nil {
		return err
	}
	defer release()
	owner, err := messageid.New()
	if err != nil {
		return err
	}
	// Handlers must honor their context. A lease longer than the invocation timeout
	// prevents ordinary retries from executing a live handler concurrently.
	claimed, err := source.Claim(ctx, delivery, string(owner), 2*time.Minute)
	if err != nil {
		return err
	}
	if !claimed {
		return ErrBusy
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Minute)
	err = invoke(callCtx, registry, delivery)
	cancel()
	if err != nil {
		r.logger.ErrorContext(ctx, "entity hook failed", "source", source.Name(), "event_id", delivery.Event.ID, "module", delivery.Target.Module, "handler", delivery.Target.Handler, "scope_id", delivery.Target.ScopeID, "error", err)
		return errors.Join(err, source.Fail(ctx, delivery, string(owner), err.Error()))
	}
	return source.Complete(ctx, delivery, string(owner))
}

func invoke(ctx context.Context, registry *Registry, delivery Delivery) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("entity hook panic: %v", value)
		}
	}()
	return registry.Handle(ctx, delivery)
}

// PrepareRemoval is used while the registry is drained. It does not publish or
// execute work, and it never drops recipients that are no longer configured.
func PendingForScope(ctx context.Context, sources []Source, scope Scope, id string) error {
	for _, source := range sources {
		pending, err := source.Pending(ctx)
		if err != nil {
			return err
		}
		for _, item := range pending {
			if item.Target.Scope == scope && item.Target.ScopeID == id {
				return ErrBusy
			}
		}
	}
	return nil
}

// Cleanup bounds both batch size and work per tick, preserving all pending calls.
func (r *Runner) Cleanup(ctx context.Context) error {
	for _, source := range r.sources {
		for batch := 0; batch < 10; batch++ {
			count, err := source.Cleanup(ctx, 7*24*time.Hour, 500)
			if err != nil {
				return err
			}
			if count < 500 {
				break
			}
		}
	}
	return nil
}
