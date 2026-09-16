package field_test

import (
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"testing"
)

func TestMediaFieldReferenceStorage(t *testing.T) {
	schema, err := field.Compile([]field.Definition{{Key: "image", Type: field.TypeMedia, Label: "Media"}}, standardResolver())
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
	if err != nil || len(refs) != 0 {
		t.Fatal("Media became a File reference", err)
	}
	values, err := schema.Validate(map[string]any{"image": nil})
	if err != nil || len(values) != 0 {
		t.Fatal("optional Media cannot be cleared", err)
	}
}
