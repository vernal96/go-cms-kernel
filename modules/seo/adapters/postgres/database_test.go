package postgres

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/seo"
)

func TestMigrationSourceDefinesScopedCascadingMetadata(t *testing.T) {
	t.Parallel()
	sources := (&Database{}).MigrationSources()
	if len(sources) != 1 || sources[0].ID != "seo" || sources[0].Schema != "seo" {
		t.Fatalf("migration sources = %#v", sources)
	}
	entries, err := fs.ReadDir(sources[0].FS, sources[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Fatalf("migration files = %#v", entries)
	}
	identity, err := fs.ReadFile(sources[0].FS, sources[0].Path+"/000002_resource_entity_identity.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(identity), "REFERENCES core.resource_entities (id, site_id)") {
		t.Fatal("SEO identity migration does not reference stable resource entities")
	}
	up, err := fs.ReadFile(
		sources[0].FS,
		sources[0].Path+"/000001_resource_metadata.up.sql",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"CREATE TABLE seo.resource_metadata",
		"FOREIGN KEY (resource_id, site_id)",
		"REFERENCES core.resources (id, site_id)",
		"ON DELETE CASCADE",
	} {
		if !strings.Contains(string(up), required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
	if (&Database{}).ModuleCode() != seo.ModuleCode {
		t.Fatal("database module code mismatch")
	}
}
