package app_test

import (
	"context"
	"github.com/vernal96/go-cms-kernel"
	appkernel "github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/user/adapters/argon2id"
	"strings"
	"testing"
)

type collidingMigrationDatabase struct{ fakeFeatureDatabase }

func (*collidingMigrationDatabase) MigrationSources() []migrations.Source {
	source := versionedSource("custom")
	source.ID = string(featureModuleCode)
	return []migrations.Source{source}
}
func TestAppRejectsMigrationHistoryCollision(t *testing.T) {
	connector := newFakeConnector("main")
	_, err := appkernel.New(context.Background(), appkernel.Definition{Logger: fakeLoggerFactory{}, EventBus: fakeEventBusFactory{}, PasswordHasher: argon2id.Factory{}, MainDatabase: appkernel.DatabaseDefinition{
		Connector: &fakeConnectorFactory{connector: connector}, Adapters: []kernel.ModuleDatabaseFactory{
			&fakeDatabaseFactory{code: core.ModuleCode, database: &fakeCoreDatabase{repository: &fakeSiteRepository{}}},
			&fakeDatabaseFactory{code: featureModuleCode, database: &collidingMigrationDatabase{}},
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "share history") {
		t.Fatalf("error=%v", err)
	}
	if _, exists := connector.version(migrations.DefaultHistoryTable); exists {
		t.Fatal("migration ran before validation")
	}
}
