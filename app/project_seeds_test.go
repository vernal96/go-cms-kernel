package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	appkernel "github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/user/adapters/argon2id"
	"github.com/vernal96/go-cms-kernel/seeds"
)

func TestProjectSeedsShareAdapterValidation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		module           kernel.ModuleCode
		id, schema, want string
	}{
		{"explicit", core.ModuleCode, "project_dev", "core", ""},
		{"duplicate", core.ModuleCode, "system", "core", "duplicate seed source"},
		{"history", featureModuleCode, "system", "core", "share history"},
		{"missing", "missing", "project_dev", "core", "unavailable module"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connector := newFakeConnector("main")
			system := seedSource("system", "prod", "system")
			system.Schema = "core"
			project := seedSource(tc.id, "dev", "project")
			project.Schema = tc.schema
			definition := appkernel.Definition{Logger: fakeLoggerFactory{}, EventBus: fakeEventBusFactory{}, PasswordHasher: argon2id.Factory{}, MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{connector: connector},
				Adapters: []kernel.ModuleDatabaseFactory{
					&fakeDatabaseFactory{code: core.ModuleCode, database: &fakeCoreDatabase{repository: &fakeSiteRepository{}, seedSources: []seeds.Source{system}}},
					&fakeDatabaseFactory{code: featureModuleCode, database: &fakeFeatureDatabase{}},
				},
				Seeds: []appkernel.ModuleSeedSource{{Module: tc.module, Source: project}},
			}}
			application, err := appkernel.New(context.Background(), definition)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer application.Close()
			definition.MainDatabase.Seeds[0].Source.Tags[0] = "changed"
			plans := application.SeedPlans()
			if len(plans) != 2 || plans[1].Module != core.ModuleCode || plans[1].Source.Tags[0] != "dev" {
				t.Fatalf("plans=%v", plans)
			}
			plans[1].Source.Tags[0] = "also changed"
			if application.SeedPlans()[1].Source.Tags[0] != "dev" {
				t.Fatal("mutable tags escaped")
			}
		})
	}
}
