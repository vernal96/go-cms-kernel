package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type jobsTestBus struct{}

func (jobsTestBus) Publish(context.Context, eventbus.Message) error { return nil }
func (jobsTestBus) Consume(context.Context, eventbus.Subscription, eventbus.Handler) error {
	return nil
}

type jobsTestResolver struct{}

func (jobsTestResolver) MainModuleDatabase(kernel.ModuleCode) (kernel.ModuleDatabase, bool) {
	return nil, false
}
func (jobsTestResolver) ModuleDatabase(kernel.ConnectionCode, kernel.ModuleCode) (kernel.ModuleDatabase, bool) {
	return nil, false
}

type jobsTestModule struct {
	called    *int
	ambiguous bool
}

func (jobsTestModule) Code() kernel.ModuleCode { return "jobs_test" }
func (jobsTestModule) JobNames() []string      { return []string{"test.execute"} }
func (m jobsTestModule) Build(_ context.Context, ctx kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return jobsTestRuntime{scope: ctx.Scope().SiteID(), called: m.called, ambiguous: m.ambiguous}, nil
}

type jobsTestRuntime struct {
	scope     string
	called    *int
	ambiguous bool
}

func (jobsTestRuntime) ModuleCode() kernel.ModuleCode { return "jobs_test" }
func (r jobsTestRuntime) Jobs() []job.Definition {
	handler := func(_ context.Context, item job.Envelope) error {
		if item.ScopeID != r.scope {
			return errors.New("wrong scope")
		}
		*r.called++
		return nil
	}
	result := []job.Definition{{Name: "test.execute", ScopeID: r.scope, Handler: handler}}
	if r.ambiguous {
		result = append(result, job.Definition{Name: "test.execute", ScopeID: r.scope, Handler: handler})
	}
	return result
}

type jobsTestProfiles map[kernel.ProfileCode]*kernel.ProfileBlueprint

func (p jobsTestProfiles) ProfileBlueprint(code kernel.ProfileCode) (*kernel.ProfileBlueprint, bool) {
	result, exists := p[code]
	return result, exists
}

type jobsTestRepository struct{ items []site.Site }

func (r *jobsTestRepository) List(context.Context) ([]site.Site, error) {
	return append([]site.Site(nil), r.items...), nil
}
func (*jobsTestRepository) Update(context.Context, *security.UserID, site.Site) (site.Site, error) {
	return site.Site{}, errors.New("not implemented")
}

type jobsTestAccess struct{}

func (jobsTestAccess) Check(context.Context, security.Actor, permission.Code) error { return nil }
func (jobsTestAccess) IsGuestSubject(context.Context, security.Actor) (bool, error) {
	return false, nil
}

func compileJobsTestProfile(t *testing.T, profile kernel.Profile) *kernel.ProfileBlueprint {
	t.Helper()
	factory, err := kernel.NewProfileRuntimeFactory(jobsTestResolver{}, kernel.RuntimeServices{
		EventBus: jobsTestBus{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := factory.Compile(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func jobsTestMessage(t *testing.T, scope string) eventbus.Message {
	t.Helper()
	item, err := job.NewScoped("test.execute", 1, scope, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return eventbus.Message{Topic: job.Topic(item.Name), Body: body}
}

func TestRuntimeJobRunnerAcknowledgesDeletedAndIntentionallyAbsentScopes(t *testing.T) {
	called := 0
	module := jobsTestModule{called: &called}
	profiles := []kernel.Profile{
		{Code: "without"},
		{Code: "with", Modules: []kernel.ProfileModule{{Module: module}}},
	}
	catalog, err := site.NewCatalog(&jobsTestRepository{items: []site.Site{
		{ID: 1, ProfileCode: "without", Domain: "without.test", Locale: "en-US"},
		{ID: 2, ProfileCode: "with", Domain: "with.test", Locale: "en-US"},
	}}, jobsTestProfiles{
		"without": compileJobsTestProfile(t, profiles[0]),
		"with":    compileJobsTestProfile(t, profiles[1]),
	}, jobsTestAccess{})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner, err := jobRunnerFromProfiles(profiles, catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"999", "1"} {
		if err := runner.Handle(context.Background(), jobsTestMessage(t, scope)); err != nil {
			t.Fatalf("obsolete scope %s retried: %v", scope, err)
		}
	}
	if err := runner.Handle(context.Background(), jobsTestMessage(t, "2")); err != nil || called != 1 {
		t.Fatalf("valid scoped job = called:%d error:%v", called, err)
	}
}

func TestRuntimeJobRunnerKeepsAmbiguousHandlersAsErrors(t *testing.T) {
	called := 0
	module := jobsTestModule{called: &called, ambiguous: true}
	profile := kernel.Profile{Code: "with", Modules: []kernel.ProfileModule{{Module: module}}}
	catalog, err := site.NewCatalog(&jobsTestRepository{items: []site.Site{{ID: 2, ProfileCode: "with", Domain: "with.test", Locale: "en-US"}}}, jobsTestProfiles{"with": compileJobsTestProfile(t, profile)}, jobsTestAccess{})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner, err := jobRunnerFromProfiles([]kernel.Profile{profile}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Handle(context.Background(), jobsTestMessage(t, "2"))
	if err == nil || errors.Is(err, job.ErrObsolete) || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous handler error = %v", err)
	}
	malformed := jobsTestMessage(t, "2")
	malformed.Body = []byte(fmt.Sprintf(`{"name":%q}`, "test.execute"))
	if err := runner.Handle(context.Background(), malformed); err == nil {
		t.Fatal("malformed job was acknowledged")
	}
}

var _ job.NamesProvider = jobsTestModule{}
var _ job.Provider = jobsTestRuntime{}
