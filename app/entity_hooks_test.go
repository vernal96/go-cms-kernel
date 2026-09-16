package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

var transitionTestKey = entityhooks.NewKey[string]("hook_test", "hook_test.updated", entityhooks.Site)

type transitionHookModule struct{}

func (transitionHookModule) Code() kernel.ModuleCode { return "hook_test" }
func (transitionHookModule) EntityHookEventNames() []string {
	return []string{transitionTestKey.Name()}
}
func (transitionHookModule) Build(_ context.Context, ctx kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	if err := entityhooks.RegisterAfter(ctx.EntityHooks(), transitionTestKey, "listener", func(context.Context, entityhooks.Delivery, string) error { return nil }); err != nil {
		return nil, err
	}
	return transitionHookRuntime{}, nil
}

type transitionHookRuntime struct{}

func (transitionHookRuntime) ModuleCode() kernel.ModuleCode { return "hook_test" }

type pendingHookSource struct {
	entityhooks.Source
	items []entityhooks.PendingTarget
}

func (*pendingHookSource) Name() string { return "test" }
func (s *pendingHookSource) Pending(context.Context) ([]entityhooks.PendingTarget, error) {
	return s.items, nil
}

func TestEntityHooksBlockRemovalAndAbortFailedPublication(t *testing.T) {
	ctx := context.Background()
	profiles := []kernel.Profile{{Code: "with", Modules: []kernel.ProfileModule{{Module: transitionHookModule{}}}}, {Code: "without"}}
	repository := &jobsTestRepository{items: []site.Site{{ID: 1, ProfileCode: "with", Domain: "hooks.test", Locale: "en-US"}}}
	catalog, err := site.NewCatalog(repository, jobsTestProfiles{"with": compileJobsTestProfile(t, profiles[0]), "without": compileJobsTestProfile(t, profiles[1])}, jobsTestAccess{})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	source := &pendingHookSource{items: []entityhooks.PendingTarget{{Name: transitionTestKey.Name(), Target: entityhooks.Target{Module: "hook_test", Handler: "listener", Scope: entityhooks.Site, ScopeID: "1"}}}}
	a := &App{definition: Definition{Profiles: profiles}, hookSources: []entityhooks.Source{source}, applicationHooks: entityhooks.EmptyRegistry(entityhooks.Application, ""), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, _, err := a.prepareEntityHooks(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	original, _ := catalog.RuntimeByID(1)
	repository.items[0].ProfileCode = "without"
	if err := catalog.Reload(ctx); !errors.Is(err, kernel.ErrRuntimeTransitionBlocked) {
		t.Fatalf("profile change=%v", err)
	}
	current, _ := catalog.RuntimeByID(1)
	if current != original {
		t.Fatal("failed transition published a new runtime")
	}
	release, err := original.Profile().EntityHooks().Acquire()
	if err != nil {
		t.Fatal("drain was not aborted:", err)
	}
	release()
	repository.items = nil
	if err := catalog.Reload(ctx); !errors.Is(err, kernel.ErrRuntimeTransitionBlocked) {
		t.Fatalf("site deletion=%v", err)
	}
	repository.items = []site.Site{{ID: 1, ProfileCode: "with", Domain: "hooks.test", Locale: "en-US"}}
	// A later transport preparation failure must also release our drain.
	fail := false
	if err := catalog.AddRuntimePreparer(ctx, func(context.Context, site.RuntimePlan) (site.RuntimePreparation, error) {
		if fail {
			return site.RuntimePreparation{}, errors.New("transport failed")
		}
		return site.RuntimePreparation{Publish: func() {}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := catalog.Reload(ctx); err == nil {
		t.Fatal("transport failure ignored")
	}
	release, err = original.Profile().EntityHooks().Acquire()
	if err != nil {
		t.Fatal(err)
	}
	fail = false
	if err := catalog.Reload(ctx); !errors.Is(err, kernel.ErrRuntimeTransitionBlocked) {
		t.Fatalf("active mutation not protected: %v", err)
	}
	release()
	source.items = nil
	if err := catalog.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := original.Profile().EntityHooks().Acquire(); !errors.Is(err, entityhooks.ErrBusy) {
		t.Fatalf("retired runtime accepted a write: %v", err)
	}
}

func TestEntityHookStartupRejectsMissingPendingHandler(t *testing.T) {
	source := &pendingHookSource{items: []entityhooks.PendingTarget{{Name: "removed.event", Target: entityhooks.Target{Module: "removed", Handler: "listener", Scope: entityhooks.Application}}}}
	runner, err := entityhooks.NewRunner([]entityhooks.Source{source}, func(entityhooks.Target) (*entityhooks.Registry, bool) {
		return entityhooks.EmptyRegistry(entityhooks.Application, ""), true
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ValidatePending(context.Background()); !errors.Is(err, entityhooks.ErrUnavailable) {
		t.Fatalf("startup accepted lost work: %v", err)
	}
}
