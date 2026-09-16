package field_test

import (
	"testing"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

func TestFileTypeNormalizesReferencesAndClonesOptions(t *testing.T) {
	required := true
	options := field.FileOptions{
		Storages:  []filesystem.Code{"public"},
		MIMETypes: []string{"image/*", "application/pdf"},
	}
	schema, err := field.Compile([]field.Definition{{
		Key: "asset", Type: field.TypeFile, Label: "Asset",
		Required: &required, Options: options,
	}}, standardResolver())
	if err != nil {
		t.Fatal(err)
	}
	values, err := schema.Validate(map[string]any{"asset": float64(42)})
	if err != nil {
		t.Fatal(err)
	}
	if values["asset"] != int64(42) {
		t.Fatalf("normalized file id = %#v", values["asset"])
	}
	references, err := schema.FileReferences(values)
	if err != nil || len(references) != 1 || references[0].ID != 42 || references[0].Key != "asset" {
		t.Fatalf("file references = %#v, err = %v", references, err)
	}
	if !field.FileMatches(references[0].Options, "public", "image/png") ||
		field.FileMatches(references[0].Options, "private", "image/png") ||
		field.FileMatches(references[0].Options, "public", "text/plain") {
		t.Fatal("file option matching is invalid")
	}
	options.Storages[0] = "private"
	if references[0].Options.Storages[0] != "public" {
		t.Fatal("file options were not cloned")
	}
}

func TestFileTypeRejectsInvalidOptionsAndIDs(t *testing.T) {
	for _, options := range []field.FileOptions{
		{Storages: []filesystem.Code{"public", "public"}},
		{MIMETypes: []string{"*/*"}},
	} {
		if _, err := field.Compile([]field.Definition{{
			Key: "asset", Type: field.TypeFile, Label: "Asset", Options: options,
		}}, standardResolver()); err == nil {
			t.Fatalf("invalid options were accepted: %#v", options)
		}
	}
	schema, err := field.Compile([]field.Definition{{
		Key: "asset", Type: field.TypeFile, Label: "Asset",
	}}, standardResolver())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schema.Validate(map[string]any{"asset": 0}); err == nil {
		t.Fatal("zero file id was accepted")
	}
}
