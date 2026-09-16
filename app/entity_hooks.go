package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

func (a *App) prepareEntityHooks(ctx context.Context, catalog *site.Catalog) (*entityhooks.Runner, []string, error) {
	resolve := func(target entityhooks.Target) (*entityhooks.Registry, bool) {
		if target.Scope == entityhooks.Application {
			return a.applicationHooks, a.applicationHooks != nil
		}
		id, err := strconv.ParseInt(target.ScopeID, 10, 64)
		if err != nil {
			return nil, false
		}
		runtime, ok := catalog.RuntimeByID(site.ID(id))
		if !ok {
			return nil, false
		}
		return runtime.Profile().EntityHooks(), true
	}
	runner, err := entityhooks.NewRunner(a.hookSources, resolve, a.logger)
	if err != nil {
		return nil, nil, err
	}
	if err := runner.ValidatePending(ctx); err != nil {
		return nil, nil, err
	}
	names := map[string]bool{}
	for _, profile := range a.definition.Profiles {
		for _, item := range profile.Modules {
			if provider, ok := item.Module.(entityhooks.NamesProvider); ok {
				for _, name := range provider.EntityHookEventNames() {
					names[name] = true
				}
			}
		}
	}
	for _, name := range a.applicationHooks.Names() {
		names[name] = true
	}
	preparer := func(ctx context.Context, plan site.RuntimePlan) (site.RuntimePreparation, error) {
		next := map[site.ID]*site.Runtime{}
		for _, runtime := range plan.Next() {
			next[runtime.Site().ID] = runtime
			for _, name := range runtime.Profile().EntityHooks().Names() {
				if !names[name] {
					return site.RuntimePreparation{}, fmt.Errorf("entity event %q must be declared through EntityHookEventNames", name)
				}
			}
		}
		var aborts []func()
		abort := func() {
			for i := len(aborts) - 1; i >= 0; i-- {
				aborts[i]()
			}
		}
		for _, current := range plan.Current() {
			candidate := next[current.Site().ID]
			if candidate == current {
				continue
			}
			undo, err := current.Profile().EntityHooks().Drain()
			if err != nil {
				abort()
				return site.RuntimePreparation{}, errors.Join(kernel.ErrRuntimeTransitionBlocked, err)
			}
			aborts = append(aborts, undo)
			for _, source := range a.hookSources {
				pending, err := source.Pending(ctx)
				if err != nil {
					abort()
					return site.RuntimePreparation{}, err
				}
				for _, item := range pending {
					if item.Target.Scope != entityhooks.Site || item.Target.ScopeID != fmt.Sprint(current.Site().ID) {
						continue
					}
					if candidate == nil || candidate.Site().ProfileCode != current.Site().ProfileCode || !candidate.Profile().EntityHooks().Has(item.Name, item.Target) {
						abort()
						return site.RuntimePreparation{}, errors.Join(kernel.ErrRuntimeTransitionBlocked, entityhooks.ErrBusy)
					}
				}
			}
		}
		return site.RuntimePreparation{Publish: func() {}, Abort: abort}, nil
	}
	if err := catalog.AddRuntimePreparer(ctx, preparer); err != nil {
		return nil, nil, err
	}
	topics := make([]string, 0, len(names))
	for name := range names {
		topics = append(topics, name)
	}
	sort.Strings(topics)
	return runner, topics, nil
}

func (a *App) startEntityHooks(ctx context.Context, runner *entityhooks.Runner, topics []string) {
	if len(topics) == 0 || len(a.hookSources) == 0 {
		return
	}
	a.workers.Add(2)
	go func() {
		defer a.workers.Done()
		for ctx.Err() == nil {
			err := a.eventBus.Consume(ctx, eventbus.Subscription{Topics: topics, Group: "go-cms-entityhooks"}, runner.Handle)
			if ctx.Err() != nil {
				return
			}
			a.logger.ErrorContext(ctx, "entity hook consumer stopped; retrying", "error", err)
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	go func() {
		defer a.workers.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := runner.Cleanup(ctx); err != nil {
					a.logger.ErrorContext(ctx, "entity hook cleanup failed", "error", err)
				}
			}
		}
	}()
}
