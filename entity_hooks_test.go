package kernel_test

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
)

type globalHookApplication struct{ registrations, calls *int }

func (globalHookApplication) ModuleCode() kernel.ModuleCode     { return "global_listener" }
func (globalHookApplication) Dependencies() []kernel.ModuleCode { return []kernel.ModuleCode{"core"} }
func (a globalHookApplication) RegisterEntityHooks(_ context.Context, r entityhooks.Registrar) error {
	*a.registrations++
	return entityhooks.RegisterBefore(r, user.BeforeCreate, "defaults", func(_ context.Context, c *user.Change) error { *a.calls++; c.Data.Name = "global"; return nil })
}

func TestGlobalEntityHooksAreIndependentOfSiteBuilds(t *testing.T) {
	registrations, calls := 0, 0
	application := globalHookApplication{&registrations, &calls}
	hooks, err := kernel.BuildApplicationEntityHooks(context.Background(), []kernel.ModuleApplication{application}, []kernel.ModuleCode{"core"})
	if err != nil {
		t.Fatal(err)
	}
	factory, err := kernel.NewProfileRuntimeFactory(emptyDatabaseResolver{}, kernel.RuntimeServices{EventBus: testRuntimeServices().EventBus, Logger: testRuntimeServices().Logger, ModuleApplications: []kernel.ModuleApplication{application}})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(context.Background(), kernel.Profile{Code: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if _, err := blueprint.Build(context.Background(), kernel.NewRuntimeScope(id, "site.test", "en-US", nil)); err != nil {
			t.Fatal(err)
		}
	}
	draft := user.Change{}
	if err := entityhooks.Before(context.Background(), hooks, user.BeforeCreate, &draft); err != nil {
		t.Fatal(err)
	}
	if registrations != 1 || calls != 1 || draft.Data.Name != "global" {
		t.Fatalf("registrations=%d calls=%d name=%q", registrations, calls, draft.Data.Name)
	}
}
