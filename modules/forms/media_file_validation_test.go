package forms

import (
	"context"
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
)

type occurrenceReaderRepository struct {
	Repository
	values media.FileOccurrenceValues
}

func (r occurrenceReaderRepository) ReadFileOccurrence(context.Context, media.FileOccurrence) (media.FileOccurrenceValues, error) {
	return r.values, nil
}

func TestFormsMediaSwitchUsesCurrentElementSchemaAndHistoricalResultSnapshot(t *testing.T) {
	ctx := context.Background()
	options := testImageFileOptions()
	options.MIMETypes = []string{"image/png"}
	catalog, err := newElementCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{service: &Service{siteID: 1, elements: catalog, fieldTypes: formsFieldResolver()}}
	ref := media.FileOccurrence{OwnerKind: "forms.element", OwnerID: 2, SiteID: 1, Container: "config", Path: []string{"file_id"}, Target: field.ReferenceFile, MediaID: 7}
	runtime.service.repository = occurrenceReaderRepository{values: media.FileOccurrenceValues{Code: string(ElementImage), Values: map[string]any{"file_id": int64(7)}}}
	if err := runtime.ValidateMediaFileOccurrence(ctx, ref, file.File{Storage: "public", MIMEType: "image/png"}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []file.File{{Storage: "private", MIMEType: "image/png"}, {Storage: "public", MIMEType: "image/jpeg"}} {
		var validation field.ValidationErrors
		if err := runtime.ValidateMediaFileOccurrence(ctx, ref, target); !errors.As(err, &validation) {
			t.Fatalf("Forms element guard: %v", err)
		}
	}
	ref.OwnerKind, ref.Container, ref.Target, ref.Path = "forms.result", "result_values", field.ReferenceMedia, []string{"11", "0"}
	snapshot := field.Reference{ID: 7, Target: field.ReferenceFile, Path: ref.Path, Options: field.FileOptions{Disk: "public", MIMETypes: []string{"application/pdf"}}}
	runtime.service.repository = occurrenceReaderRepository{values: media.FileOccurrenceValues{References: []field.Reference{snapshot}}}
	if err := runtime.ValidateMediaFileOccurrence(ctx, ref, file.File{Storage: "public", MIMEType: "application/pdf"}); err != nil {
		t.Fatalf("historical PDF misclassified as image: %v", err)
	}
	for _, target := range []file.File{{Storage: "private", MIMEType: "application/pdf"}, {Storage: "public", MIMEType: "image/png"}} {
		var validation field.ValidationErrors
		if err := runtime.ValidateMediaFileOccurrence(ctx, ref, target); !errors.As(err, &validation) {
			t.Fatalf("Forms result snapshot guard: %v", err)
		}
	}
}

func TestSubmissionSnapshotsScalarMultipleAndNestedFileConstraints(t *testing.T) {
	options := field.FileOptions{Disk: "public", VirtualPath: "uploads", SettingsCode: "document", MIMETypes: []string{"application/pdf"}}
	multiple := options
	multiple.Multiple = true
	definitions := []field.Definition{
		{Key: "document", Label: "Document", Type: field.TypeFile, Options: options},
		{Key: "documents", Label: "Documents", Type: field.TypeFile, Options: multiple},
		{Key: "rows", Label: "Rows", Type: field.TypeRepeater, Options: field.RepeaterOptions{Fields: []field.Definition{{Key: "file", Label: "File", Type: field.TypeFile, Options: options}}}},
	}
	schema, err := field.CompilePersistent(definitions, formsFieldResolver())
	if err != nil {
		t.Fatal(err)
	}
	values, err := schema.Validate(map[string]any{"document": int64(7), "documents": []any{int64(8), int64(9)}, "rows": []any{map[string]any{"file": int64(10)}}})
	if err != nil {
		t.Fatal(err)
	}
	active := make([]FormField, len(definitions))
	for index, definition := range definitions {
		active[index] = FormField{ID: FieldID(index + 1), Code: definition.Key, Label: definition.Label, Type: definition.Type}
	}
	snapshots, err := submissionResultValues(validatedSubmission{schema: schema, values: values, active: active})
	if err != nil || len(snapshots) != 4 {
		t.Fatalf("result snapshots: %#v, %v", snapshots, err)
	}
	for _, value := range snapshots {
		if len(value.FileReferences) != 1 || value.FileReferences[0].Options.Disk != "public" || value.FileReferences[0].Options.MIMETypes[0] != "application/pdf" {
			t.Fatalf("missing normalized constraints: %#v", value)
		}
		if value.FieldCode == "rows" && field.ReferenceKey(value.FileReferences[0].Path) != "0.file" {
			t.Fatalf("nested relative snapshot path: %v", value.FileReferences[0].Path)
		}
		if value.FieldCode != "rows" && len(value.FileReferences[0].Path) != 0 {
			t.Fatalf("scalar/list row snapshot path: %v", value.FileReferences[0].Path)
		}
	}
}
