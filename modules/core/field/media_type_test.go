package field_test

import (
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"testing"
)

func TestMediaFieldReferenceStorage(t *testing.T) {
	schema, err := field.Compile([]field.Definition{{Key: "image", Type: field.TypeMedia, Label: "Media", Options: field.MediaOptions{Disk: "public", VirtualPath: "images", SettingsCode: "image"}}}, standardResolver())
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []any{-1, 0, 1.5, "bad", map[string]any{"file_id": 1}} {
		if _, err := schema.Validate(map[string]any{"image": invalid}); err == nil {
			t.Fatalf("accepted invalid Media ID: %v", invalid)
		}
	}
	normalized, err := schema.Validate(map[string]any{"image": float64(42)})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := schema.StoredValues(normalized)
	if err != nil || len(rows) != 1 || rows[0].Kind != field.StorageReference || rows[0].ReferenceTarget != field.ReferenceMedia || rows[0].Value != int64(42) {
		t.Fatalf("invalid storage: %+v, %v", rows, err)
	}
	refs, err := schema.FileReferences(normalized)
	if err != nil || len(refs) != 1 || refs[0].Options.Disk != "public" {
		t.Fatal("Media constraints were not collected", err)
	}
	values, err := schema.Validate(map[string]any{"image": nil})
	if err != nil || len(values) != 0 {
		t.Fatal("optional Media cannot be cleared", err)
	}
}

func TestMediaIsTheOnlyBuiltInFileField(t *testing.T) {
	types := field.StandardTypes()
	if _, exists := types.FieldType("file"); exists {
		t.Fatal("removed file field remains registered")
	}
	for _, typ := range []field.Type{field.MediaType(nil), field.MediaType(map[string][]field.Definition{"image": nil})} {
		metadata := field.DescribeType(typ)
		if metadata.Editor != "media" {
			t.Fatalf("editor=%s", metadata.Editor)
		}
		multiple := 0
		for _, option := range metadata.Options {
			if option.Key == "multiple" {
				multiple++
			}
		}
		if multiple != 1 {
			t.Fatalf("multiple options=%d", multiple)
		}
	}
}
