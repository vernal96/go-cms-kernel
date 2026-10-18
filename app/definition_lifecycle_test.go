package app_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	appkernel "github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user/adapters/argon2id"
)

type countingDefinitionModule struct{ calls *atomic.Int32 }

func (countingDefinitionModule) Code() kernel.ModuleCode { return "counting" }
func (countingDefinitionModule) Validate(context.Context, kernel.ModuleValidationContext) error {
	return nil
}
func (countingDefinitionModule) NewDefinitions(context.Context, kernel.DefinitionContext) (kernel.ModuleDefinitions, error) {
	return &countingDefinitions{}, nil
}
func (m countingDefinitionModule) DefinitionContributions() []kernel.DefinitionContribution {
	return []kernel.DefinitionContribution{kernel.DefineFor[*countingDefinitions](m.Code(), func(_ context.Context, definitions *countingDefinitions) error {
		if definitions.sealed {
			return errors.New("definition contribution executed after seal")
		}
		m.calls.Add(1)
		return nil
	})}
}
func (m countingDefinitionModule) Build(_ context.Context, ctx kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	definitions, err := kernel.ModuleDefinitionsFrom[*countingDefinitions](ctx, m.Code())
	if err != nil {
		return nil, err
	}
	if !definitions.sealed {
		return nil, errors.New("definitions were not sealed before runtime build")
	}
	return countingRuntime{}, nil
}

type countingDefinitions struct{ sealed bool }

func (*countingDefinitions) ModuleCode() kernel.ModuleCode { return "counting" }
func (d *countingDefinitions) FinalizeDefinitionBuild(context.Context) error {
	d.sealed = true
	return nil
}

type countingRuntime struct{}

func (countingRuntime) ModuleCode() kernel.ModuleCode { return "counting" }

func TestAppReusesDefinitionsFromNewThroughBootAndSiteReload(t *testing.T) {
	var calls atomic.Int32
	application, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger: fakeLoggerFactory{}, PasswordHasher: argon2id.Factory{}, SiteAccessPolicy: admin.AllowAllSitesPolicy{}, EventBus: fakeEventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: &fakeConnectorFactory{connector: newFakeConnector("main")},
			Adapters:  []kernel.ModuleDatabaseFactory{&fakeDatabaseFactory{code: core.ModuleCode, database: &fakeCoreDatabase{repository: &fakeSiteRepository{sites: []site.Site{{ID: 1, ProfileCode: "dev", Name: "Test", Domain: "example.test", Locale: "en"}}}}}},
		},
		Profiles: []kernel.Profile{{Code: "dev", Modules: []kernel.Module{core.New(core.Config{}), admin.New(), countingDefinitionModule{calls: &calls}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Error(err)
		}
	})
	if calls.Load() != 1 {
		t.Fatalf("definition contributions after New = %d", calls.Load())
	}
	if err := application.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := application.Sites().Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("definition contributions repeated during Boot or site rebuild: %d", calls.Load())
	}
}
