package migrations

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/stub"
)

type stubTarget struct {
	driver       *stub.Stub
	openedSchema string
	openedTable  string
}

func (t *stubTarget) OpenMigrationDriver(
	_ context.Context,
	schema string,
	historyTable string,
) (migratedb.Driver, error) {
	t.openedSchema = schema
	t.openedTable = historyTable
	return t.driver, nil
}

func TestManagerLifecycle(t *testing.T) {
	driver, err := stub.WithInstance(nil, &stub.Config{})
	if err != nil {
		t.Fatal(err)
	}

	target := &stubTarget{driver: driver.(*stub.Stub)}
	plan := Plan{
		Connection: "main",
		Target:     target,
		Source: Source{
			ID:     "core",
			Schema: "core",
			FS: fstest.MapFS{
				"000001_sites.up.sql":   &fstest.MapFile{Data: []byte("CREATE SITES")},
				"000001_sites.down.sql": &fstest.MapFile{Data: []byte("DROP SITES")},
			},
			Path: ".",
		},
	}

	manager := NewManager()
	ctx := context.Background()

	if err := manager.Up(ctx, plan); err != nil {
		t.Fatalf("up: %v", err)
	}

	if target.openedSchema != "core" {
		t.Fatalf("schema = %q", target.openedSchema)
	}
	if target.openedTable != DefaultHistoryTable {
		t.Fatalf("history table = %q", target.openedTable)
	}

	version, hasVersion, dirty, err := manager.Version(ctx, plan)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != 1 || !hasVersion || dirty {
		t.Fatalf(
			"version = %d, hasVersion = %t, dirty = %t",
			version,
			hasVersion,
			dirty,
		)
	}

	status, err := manager.Status(ctx, plan)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(status.Migrations) != 1 || !status.Migrations[0].Applied {
		t.Fatalf("unexpected status: %#v", status)
	}

	if err := manager.Down(ctx, plan, 1); err != nil {
		t.Fatalf("down: %v", err)
	}

	_, hasVersion, dirty, err = manager.Version(ctx, plan)
	if err != nil {
		t.Fatalf("version after down: %v", err)
	}
	if hasVersion || dirty {
		t.Fatalf(
			"after down hasVersion = %t, dirty = %t",
			hasVersion,
			dirty,
		)
	}

	if err := manager.Force(ctx, plan, 7); err != nil {
		t.Fatalf("force: %v", err)
	}

	version, hasVersion, dirty, err = manager.Version(ctx, plan)
	if err != nil {
		t.Fatalf("version after force: %v", err)
	}
	if version != 7 || !hasVersion || dirty {
		t.Fatalf(
			"forced version = %d, hasVersion = %t, dirty = %t",
			version,
			hasVersion,
			dirty,
		)
	}
}

func TestMigrationFilesRequireUpAndDown(t *testing.T) {
	tests := []struct {
		name    string
		files   fs.FS
		wantErr bool
	}{
		{
			name: "pair",
			files: fstest.MapFS{
				"000002_second.up.sql":   &fstest.MapFile{},
				"000002_second.down.sql": &fstest.MapFile{},
				"000001_first.up.sql":    &fstest.MapFile{},
				"000001_first.down.sql":  &fstest.MapFile{},
			},
		},
		{
			name: "missing down",
			files: fstest.MapFS{
				"000001_first.up.sql": &fstest.MapFile{},
			},
			wantErr: true,
		},
		{
			name: "different identifiers",
			files: fstest.MapFS{
				"000001_first.up.sql":       &fstest.MapFile{},
				"000001_different.down.sql": &fstest.MapFile{},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files, err := migrationFiles(Source{FS: test.files, Path: "."})
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error, got files %#v", files)
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if len(files) != 2 || files[0].Version != 1 || files[1].Version != 2 {
				t.Fatalf("files are not sorted: %#v", files)
			}
		})
	}
}

func TestManagerRejectsSharedHistoryBeforeAnySQL(t *testing.T) {
	driver, _ := stub.WithInstance(nil, &stub.Config{})
	target := &stubTarget{driver: driver.(*stub.Stub)}
	plans := []Plan{}
	for _, name := range []string{"core", "custom"} {
		plans = append(plans, Plan{Connection: "main", Target: target, Source: Source{ID: name, Schema: "core", Path: ".", FS: fstest.MapFS{
			"000001_create.up.sql": {Data: []byte("CREATE " + name)}, "000001_create.down.sql": {Data: []byte("DROP " + name)},
		}}})
	}
	if err := NewManager().UpAll(context.Background(), plans); err == nil {
		t.Fatal("shared history accepted")
	}
	if len(target.driver.MigrationSequence) != 0 {
		t.Fatal("executed SQL before validation")
	}
	if err := NewManager().DownAll(context.Background(), plans, 1); err == nil {
		t.Fatal("shared history accepted on rollback")
	}
	plans[1].Source.Schema = "custom"
	if err := ValidateHistories(plans); err != nil {
		t.Fatal(err)
	}
	plans[1].Source.Schema = "core"
	plans[1].Connection = "another"
	if err := ValidateHistories(plans); err != nil {
		t.Fatal(err)
	}
}
