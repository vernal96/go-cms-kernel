package kernel_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

// These contracts intentionally belong to a feature unknown to Kernel.
type ruleRegistrar interface {
	kernel.ModuleDefinitions
	RegisterRule(string) error
}

type ruleDefinitions struct {
	code   kernel.ModuleCode
	rules  map[string]struct{}
	sealed bool
}

func (d *ruleDefinitions) ModuleCode() kernel.ModuleCode { return d.code }
func (d *ruleDefinitions) RegisterRule(code string) error {
	if d.sealed {
		return errors.New("rule definitions are sealed")
	}
	if _, duplicate := d.rules[code]; duplicate {
		return fmt.Errorf("rule %q already exists", code)
	}
	d.rules[code] = struct{}{}
	return nil
}
func (d *ruleDefinitions) FinalizeDefinitionBuild(context.Context) error {
	if d.sealed {
		return errors.New("rule definitions already finalized")
	}
	d.sealed = true
	return nil
}

type ruleOwner struct {
	registryModule
	creations *atomic.Int32
}

func (m ruleOwner) NewDefinitions(context.Context, kernel.DefinitionContext) (kernel.ModuleDefinitions, error) {
	if m.creations != nil {
		m.creations.Add(1)
	}
	return &ruleDefinitions{code: m.code, rules: make(map[string]struct{})}, nil
}
func (m ruleOwner) Build(_ context.Context, ctx kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	definitions, err := kernel.ModuleDefinitionsFrom[*ruleDefinitions](ctx, m.code)
	if err != nil {
		return nil, err
	}
	if !definitions.sealed {
		return nil, errors.New("module saw unsealed definitions")
	}
	return &ruleRuntime{code: m.code, definitions: definitions, rules: make(map[string]struct{})}, nil
}

type contributionModule struct {
	registryModule
	dependencies []kernel.ModuleCode
	definitions  []kernel.DefinitionContribution
	runtime      func(kernel.ModuleContext) []kernel.RuntimeContribution
}

func (m contributionModule) Dependencies() []kernel.ModuleCode { return m.dependencies }
func (m contributionModule) DefinitionContributions() []kernel.DefinitionContribution {
	return m.definitions
}
func (m contributionModule) Build(_ context.Context, ctx kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	var contributions []kernel.RuntimeContribution
	if m.runtime != nil {
		contributions = m.runtime(ctx)
	}
	return &ruleRuntime{code: m.code, contributions: contributions, rules: make(map[string]struct{})}, nil
}

type runtimeRuleRegistrar interface {
	kernel.ModuleRuntime
	RegisterRule(string) error
}

type ruleRuntime struct {
	code          kernel.ModuleCode
	definitions   *ruleDefinitions
	contributions []kernel.RuntimeContribution
	rules         map[string]struct{}
	sealed        bool
	widgets       []widget.Widget
}

func (r *ruleRuntime) ModuleCode() kernel.ModuleCode                      { return r.code }
func (r *ruleRuntime) RuntimeContributions() []kernel.RuntimeContribution { return r.contributions }
func (r *ruleRuntime) RegisterRule(code string) error {
	if r.sealed {
		return errors.New("runtime rule registry is sealed")
	}
	if _, duplicate := r.rules[code]; duplicate {
		return fmt.Errorf("runtime rule %q already exists", code)
	}
	r.rules[code] = struct{}{}
	r.widgets = append(r.widgets, runtimeWidget{definition: widget.Definition{Reference: widget.NewRef(widget.Code(code)), Label: code, Description: "Contributed rule widget"}})
	return nil
}
func (r *ruleRuntime) FinalizeRuntimeBuild(context.Context) error { r.sealed = true; return nil }
func (r *ruleRuntime) Widgets() []widget.Widget                   { return r.widgets }

func definitionRule(target kernel.ModuleCode, code string) kernel.DefinitionContribution {
	return kernel.DefineFor[ruleRegistrar](target, func(_ context.Context, registrar ruleRegistrar) error { return registrar.RegisterRule(code) })
}
func runtimeRule(target kernel.ModuleCode, code string) kernel.RuntimeContribution {
	return kernel.ContributeTo[runtimeRuleRegistrar](target, func(_ context.Context, registrar runtimeRuleRegistrar) error { return registrar.RegisterRule(code) })
}

func TestDefinitionContributionsUseOwnerContractsAndReuseFrozenSnapshot(t *testing.T) {
	ctx := context.Background()
	var creations, applications atomic.Int32
	owner := ruleOwner{registryModule: registryModule{code: "rules"}, creations: &creations}
	contributor := contributionModule{registryModule: registryModule{code: "custom"}, dependencies: []kernel.ModuleCode{"rules"}, definitions: []kernel.DefinitionContribution{
		definitionRule("rules", "first"),
		kernel.DefineFor[ruleRegistrar]("rules", func(_ context.Context, registrar ruleRegistrar) error {
			applications.Add(1)
			return registrar.RegisterRule("second")
		}),
	}}
	profile := kernel.Profile{Code: "with-rules", Modules: []kernel.Module{owner, contributor}}
	snapshot, err := kernel.CompileProfileDefinitions(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := kernel.NewProfileRuntimeFactory(emptyDatabaseResolver{}, testRuntimeServices())
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.CompileWithDefinitions(ctx, profile, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var previous *ruleRuntime
	for _, id := range []string{"one", "two"} {
		built, err := blueprint.Build(ctx, kernel.NewRuntimeScope(id, "", "", nil))
		if err != nil {
			t.Fatal(err)
		}
		runtime, _ := built.Registry().Module("rules")
		rules := runtime.(*ruleRuntime)
		if len(rules.definitions.rules) != 2 {
			t.Fatalf("definitions = %#v", rules.definitions.rules)
		}
		if previous != nil && (previous == rules || previous.definitions != rules.definitions) {
			t.Fatal("expected separate site runtimes sharing sealed profile definitions")
		}
		if err := rules.definitions.RegisterRule("late"); err == nil {
			t.Fatal("late definition registration accepted")
		}
		previous = rules
	}
	if creations.Load() != 1 || applications.Load() != 1 {
		t.Fatalf("definitions executed %d creations / %d contributions", creations.Load(), applications.Load())
	}
	// A new profile definition build receives a separate mutable target.
	other, err := factory.Compile(ctx, kernel.Profile{Code: "without-rules", Modules: []kernel.Module{owner}})
	if err != nil {
		t.Fatal(err)
	}
	built, err := other.Build(ctx, kernel.NewRuntimeScope("three", "", "", nil))
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := built.Registry().Module("rules")
	if len(runtime.(*ruleRuntime).definitions.rules) != 0 {
		t.Fatal("definitions leaked into a profile without the contributor")
	}
}

func TestDefinitionContributionsRejectInvalidTargetsAndRegistrations(t *testing.T) {
	owner := ruleOwner{registryModule: registryModule{code: "rules"}}
	contributor := func(dependencies []kernel.ModuleCode, contributions ...kernel.DefinitionContribution) contributionModule {
		return contributionModule{registryModule: registryModule{code: "custom"}, dependencies: dependencies, definitions: contributions}
	}
	for _, test := range []struct {
		name    string
		modules []kernel.Module
		want    string
	}{
		{"absent target", []kernel.Module{contributor(nil, definitionRule("missing", "one"))}, "absent"},
		{"empty target", []kernel.Module{owner, contributor(nil, definitionRule("", "one"))}, "empty"},
		{"undeclared target", []kernel.Module{owner, contributor(nil, definitionRule("rules", "one"))}, "declared dependency"},
		{"misordered target", []kernel.Module{contributor([]kernel.ModuleCode{"rules"}, definitionRule("rules", "one")), owner}, "earlier"},
		{"owner lacks definitions", []kernel.Module{registryModule{code: "rules"}, contributor([]kernel.ModuleCode{"rules"}, definitionRule("rules", "one"))}, "no definition target"},
		{"duplicate", []kernel.Module{owner, contributor([]kernel.ModuleCode{"rules"}, definitionRule("rules", "same"), definitionRule("rules", "same"))}, "already exists"},
		{"wrong typed contract", []kernel.Module{owner, contributor([]kernel.ModuleCode{"rules"}, kernel.DefineFor[interface {
			kernel.ModuleDefinitions
			MissingCapability()
		}]("rules", func(context.Context, interface {
			kernel.ModuleDefinitions
			MissingCapability()
		}) error {
			return nil
		}))}, "incompatible type"},
		{"nil contribution", []kernel.Module{owner, contributor(nil, nil)}, "nil"},
		{"nil callback", []kernel.Module{owner, contributor([]kernel.ModuleCode{"rules"}, kernel.DefineFor[ruleRegistrar]("rules", nil))}, "callback is nil"},
		{"callback error", []kernel.Module{owner, contributor([]kernel.ModuleCode{"rules"}, kernel.DefineFor[ruleRegistrar]("rules", func(context.Context, ruleRegistrar) error { return errors.New("registration failure") }))}, "registration failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := kernel.CompileProfileDefinitions(context.Background(), kernel.Profile{Code: "failure", Modules: test.modules})
			if err == nil || !strings.Contains(err.Error(), test.want) || snapshot != nil {
				t.Fatalf("snapshot = %v, error = %v; want %q", snapshot, err, test.want)
			}
		})
	}
}

func TestRuntimeContributionsFinishBeforeFinalizationAndWidgetCollection(t *testing.T) {
	factory, err := kernel.NewProfileRuntimeFactory(emptyDatabaseResolver{}, testRuntimeServices())
	if err != nil {
		t.Fatal(err)
	}
	profile := kernel.Profile{Code: "runtime-rules", Modules: []kernel.Module{
		ruleOwner{registryModule: registryModule{code: "rules"}},
		contributionModule{registryModule: registryModule{code: "custom"}, dependencies: []kernel.ModuleCode{"rules"}, runtime: func(ctx kernel.ModuleContext) []kernel.RuntimeContribution {
			return []kernel.RuntimeContribution{runtimeRule("rules", ctx.Scope().SiteID())}
		}},
	}}
	blueprint, err := factory.Compile(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for _, id := range []string{"first", "second"} {
		group.Add(1)
		go func() {
			defer group.Done()
			built, err := blueprint.Build(context.Background(), kernel.NewRuntimeScope(id, "", "", nil))
			if err != nil {
				t.Error(err)
				return
			}
			runtime, _ := built.Registry().Module("rules")
			rules := runtime.(*ruleRuntime)
			if len(rules.rules) != 1 {
				t.Errorf("site %q rules = %#v", id, rules.rules)
			}
			if _, exists := rules.rules[id]; !exists {
				t.Errorf("site %q rule is absent", id)
			}
			if _, exists := built.Widget(widget.Code("rules_" + id)); !exists {
				t.Errorf("contributed widget unavailable for %q", id)
			}
			if err := rules.RegisterRule("late"); err == nil {
				t.Error("late runtime registration accepted")
			}
		}()
	}
	group.Wait()
}

func TestRuntimeContributionFailureRejectsUnfinalizedCandidate(t *testing.T) {
	for _, test := range []struct {
		name         string
		dependencies []kernel.ModuleCode
		target       kernel.ModuleCode
		failure      bool
		want         string
	}{
		{name: "absent", target: "missing", want: "absent"},
		{name: "undeclared", target: "rules", want: "declared dependency"},
		{name: "apply error", dependencies: []kernel.ModuleCode{"rules"}, target: "rules", failure: true, want: "runtime registration failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			factory, err := kernel.NewProfileRuntimeFactory(emptyDatabaseResolver{}, testRuntimeServices())
			if err != nil {
				t.Fatal(err)
			}
			var target *ruleRuntime
			profile := kernel.Profile{Code: "runtime-failure", Modules: []kernel.Module{
				ruleOwner{registryModule: registryModule{code: "rules"}},
				contributionModule{registryModule: registryModule{code: "custom"}, dependencies: test.dependencies, runtime: func(ctx kernel.ModuleContext) []kernel.RuntimeContribution {
					if dependency, exists := ctx.Dependency("rules"); exists {
						target = dependency.(*ruleRuntime)
					}
					if test.failure {
						return []kernel.RuntimeContribution{kernel.ContributeTo[runtimeRuleRegistrar](test.target, func(context.Context, runtimeRuleRegistrar) error { return errors.New("runtime registration failure") })}
					}
					return []kernel.RuntimeContribution{runtimeRule(test.target, "one")}
				}},
			}}
			built, err := buildProfileRuntime(factory, context.Background(), profile)
			if built != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("runtime = %v, error = %v", built, err)
			}
			if target != nil && target.sealed {
				t.Fatal("failed candidate was finalized")
			}
		})
	}
}

func TestDefinitionSnapshotRejectsDifferentProfileModules(t *testing.T) {
	ctx := context.Background()
	owner := ruleOwner{registryModule: registryModule{code: "rules"}}
	profile := kernel.Profile{Code: "snapshot", Modules: []kernel.Module{owner}}
	snapshot, err := kernel.CompileProfileDefinitions(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := kernel.NewProfileRuntimeFactory(emptyDatabaseResolver{}, testRuntimeServices())
	if err != nil {
		t.Fatal(err)
	}
	profile.Modules = append(profile.Modules, registryModule{code: "new"})
	if _, err := factory.CompileWithDefinitions(ctx, profile, snapshot); err == nil {
		t.Fatal("reused definitions with different module selection")
	}
}
