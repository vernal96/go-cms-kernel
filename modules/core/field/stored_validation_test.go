package field_test

import (
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

func TestStoredFileRemovalDoesNotRelaxInteractiveOrOtherRequiredFields(t *testing.T) {
	schema, err := field.CompilePersistent([]field.Definition{{Key: "rows", Type: field.TypeRepeater, Label: "Rows", Options: field.RepeaterOptions{Fields: []field.Definition{
		{Key: "title", Type: field.TypeString, Label: "Title", Required: true},
		{Key: "icon", Type: field.TypeFile, Label: "Icon", Required: true, Options: field.FileOptions{Disk: "public", VirtualPath: "icons", SettingsCode: "icon"}},
	}}}}, standardResolver())
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{"rows": []any{map[string]any{"title": "kept"}, map[string]any{"title": "other", "icon": int64(7)}}}
	if _, err := schema.Validate(values); err == nil {
		t.Fatal("interactive required file validation relaxed")
	}
	stored, err := schema.ValidateStored(values)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := schema.StoredReferences(stored)
	if err != nil || len(refs) != 1 || refs[0].ID != 7 || refs[0].Key != "rows[1].icon" {
		t.Fatalf("refs=%v error=%v", refs, err)
	}
	if _, err := schema.StoredValues(stored); err != nil {
		t.Fatal(err)
	}
	if _, err := schema.ValidateStored(map[string]any{"rows": []any{map[string]any{}}}); err == nil {
		t.Fatal("unrelated required nested field relaxed")
	}
}
