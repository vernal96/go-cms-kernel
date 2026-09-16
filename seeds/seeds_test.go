package seeds_test

import (
	"context"
	"testing"
	"testing/fstest"

	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/stub"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/seeds"
)

type target struct {
	drivers map[string]*stub.Stub
}

func newTarget() *target {
	return &target{drivers: make(map[string]*stub.Stub)}
}

func (t *target) OpenMigrationDriver(
	_ context.Context,
	_ string,
	historyTable string,
) (migratedb.Driver, error) {
	if driver, exists := t.drivers[historyTable]; exists {
		return driver, nil
	}

	driver, err := stub.WithInstance(nil, &stub.Config{})
	if err != nil {
		return nil, err
	}

	t.drivers[historyTable] = driver.(*stub.Stub)
	return driver, nil
}

func seedPlan(
	target seeds.Target,
	id string,
	tag seeds.Tag,
) seeds.Plan {
	return seeds.Plan{
		Connection: "main",
		Module:     kernel.ModuleCode("core"),
		Target:     target,
		Source: seeds.Source{
			ID:     id,
			Tags:   []seeds.Tag{tag},
			Schema: "core",
			FS: fstest.MapFS{
				"000001_defaults.up.sql": {
					Data: []byte("INSERT DEFAULTS"),
				},
				"000001_defaults.down.sql": {
					Data: []byte("DELETE DEFAULTS"),
				},
			},
			Path: ".",
		},
	}
}

func TestSeedSourcesUseIndependentHistory(t *testing.T) {
	target := newTarget()
	devPlan := seedPlan(target, "sites_dev", "dev")
	prodPlan := seedPlan(target, "sites_prod", "prod")
	manager := seeds.NewManager()
	ctx := context.Background()

	if err := manager.Up(ctx, devPlan); err != nil {
		t.Fatal(err)
	}
	if _, exists := target.drivers[seeds.HistoryTable("sites_dev")]; !exists {
		t.Fatalf("dev history tables = %#v", target.drivers)
	}
	if _, exists := target.drivers[migrations.DefaultHistoryTable]; exists {
		t.Fatal("seed manager used migration history")
	}

	_, hasVersion, _, err := manager.Version(ctx, prodPlan)
	if err != nil {
		t.Fatal(err)
	}
	if hasVersion {
		t.Fatal("dev source marked prod source as applied")
	}

	if err := manager.Up(ctx, prodPlan); err != nil {
		t.Fatal(err)
	}
	if _, exists := target.drivers[seeds.HistoryTable("sites_prod")]; !exists {
		t.Fatalf("prod history tables = %#v", target.drivers)
	}

	statuses, err := manager.StatusAll(ctx, []seeds.Plan{devPlan, prodPlan})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 ||
		!statuses[0].HasCurrentVersion ||
		!statuses[1].HasCurrentVersion {
		t.Fatalf("statuses = %#v", statuses)
	}
	if statuses[0].Module != "core" ||
		statuses[0].Source != "sites_dev" ||
		len(statuses[0].Tags) != 1 ||
		statuses[0].Tags[0] != "dev" {
		t.Fatalf("dev status = %#v", statuses[0])
	}

	if err := manager.Down(ctx, prodPlan, 1); err != nil {
		t.Fatal(err)
	}
	if err := manager.Down(ctx, devPlan, 1); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSourceRejectsInvalidIdentityAndTags(t *testing.T) {
	valid := seedPlan(newTarget(), "sites_dev", "dev").Source

	tests := []struct {
		name   string
		mutate func(*seeds.Source)
	}{
		{
			name: "non lower snake id",
			mutate: func(source *seeds.Source) {
				source.ID = "sites-dev"
			},
		},
		{
			name: "no tags",
			mutate: func(source *seeds.Source) {
				source.Tags = nil
			},
		},
		{
			name: "empty tag",
			mutate: func(source *seeds.Source) {
				source.Tags = []seeds.Tag{""}
			},
		},
		{
			name: "duplicate tag",
			mutate: func(source *seeds.Source) {
				source.Tags = []seeds.Tag{"dev", "dev"}
			},
		},
		{
			name: "tag containing separator",
			mutate: func(source *seeds.Source) {
				source.Tags = []seeds.Tag{"dev,prod"}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := valid
			source.Tags = append([]seeds.Tag(nil), valid.Tags...)
			test.mutate(&source)

			if err := seeds.ValidateSource(source); err == nil {
				t.Fatalf("ValidateSource(%#v) succeeded", source)
			}
		})
	}
}
