package postgres

import (
	"github.com/vernal96/go-cms-kernel/seeds"
	"io/fs"
	"strings"
	"testing"
)

func TestOnlySystemSeedSources(t *testing.T) {
	sources := (&Database{}).SeedSources()
	if len(sources) != 1 || sources[0].ID != "identity_shared" {
		t.Fatalf("sources=%v", sources)
	}
	if err := seeds.ValidateSource(sources[0]); err != nil {
		t.Fatal(err)
	}
}
func TestMigrationSourceIncludesIdentityAndPermissions(t *testing.T) {
	t.Parallel()

	sources := (&Database{}).MigrationSources()
	if len(sources) != 1 {
		t.Fatalf("migration sources = %#v", sources)
	}
	entries, err := fs.ReadDir(sources[0].FS, sources[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 44 {
		t.Fatalf("migration files = %#v", entries)
	}
	expected := map[string]bool{
		"000022_resource_media_fields.up.sql":                false,
		"000022_resource_media_fields.down.sql":              false,
		"000021_resource_search.up.sql":                      false,
		"000021_resource_search.down.sql":                    false,
		"000020_entity_hook_calls.up.sql":                    false,
		"000020_entity_hook_calls.down.sql":                  false,
		"000019_resource_site_transfer.up.sql":               false,
		"000019_resource_site_transfer.down.sql":             false,
		"000018_reconcile_resource_field_schema.up.sql":      false,
		"000018_reconcile_resource_field_schema.down.sql":    false,
		"000017_outbox_messages.up.sql":                      false,
		"000017_outbox_messages.down.sql":                    false,
		"000016_resource_revisions.up.sql":                   false,
		"000016_resource_revisions.down.sql":                 false,
		"000005_identity.up.sql":                             false,
		"000005_identity.down.sql":                           false,
		"000006_permissions.up.sql":                          false,
		"000006_permissions.down.sql":                        false,
		"000007_resource_widgets.up.sql":                     false,
		"000007_resource_widgets.down.sql":                   false,
		"000008_user_blocking.up.sql":                        false,
		"000008_user_blocking.down.sql":                      false,
		"000009_file_field_references.up.sql":                false,
		"000009_file_field_references.down.sql":              false,
		"000010_user_preferences.up.sql":                     false,
		"000010_user_preferences.down.sql":                   false,
		"000011_user_accent_color.up.sql":                    false,
		"000011_user_accent_color.down.sql":                  false,
		"000012_resource_editor_tree.up.sql":                 false,
		"000012_resource_editor_tree.down.sql":               false,
		"000013_resource_widget_bindings.up.sql":             false,
		"000013_resource_widget_bindings.down.sql":           false,
		"000014_group_site_access.up.sql":                    false,
		"000014_group_site_access.down.sql":                  false,
		"000015_resource_entities_fields_libraries.up.sql":   false,
		"000015_resource_entities_fields_libraries.down.sql": false,
	}
	for _, entry := range entries {
		if _, exists := expected[entry.Name()]; exists {
			expected[entry.Name()] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Fatalf("migration %q is not embedded", name)
		}
	}
	accentMigration, err := fs.ReadFile(
		sources[0].FS,
		sources[0].Path+"/000011_user_accent_color.up.sql",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, expectedSQL := range []string{
		"DEFAULT 'blue'",
		"'blue', 'violet', 'indigo', 'emerald', 'amber', 'rose'",
	} {
		if !strings.Contains(string(accentMigration), expectedSQL) {
			t.Fatalf("accent migration does not contain %q", expectedSQL)
		}
	}
}
