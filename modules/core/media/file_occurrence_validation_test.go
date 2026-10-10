package media

import (
	"context"
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

func TestUpdateEnforcesAllFileOccurrencesAndAllowsPDFMetadata(t *testing.T) {
	ctx := context.Background()
	ref := FileOccurrence{OwnerKind: "resource", OwnerID: 7, SiteID: 1, Container: "fields:document:0", Path: []string{"document"}, Target: field.ReferenceFile}
	references := []field.Reference{{Path: ref.Path, Target: field.ReferenceFile, ID: 1, Options: field.FileOptions{Disk: "public", MIMETypes: []string{"application/pdf"}}}}
	s, repository := newServiceForTest(t, FilePolicies{FileFieldUsage: func(_ context.Context, target file.File, usage Usage) error {
		return ValidateFileOccurrence(*usage.Occurrence, references, target)
	}})
	s.(*service).files = memoryFiles{items: map[file.ID]file.File{
		1: {ID: 1, Storage: "public", MIMEType: "image/png"},
		2: {ID: 2, Storage: "public", MIMEType: "application/pdf"},
	}}
	created, err := s.Create(ctx, security.System(), CreateInput{FileID: 2})
	if err != nil {
		t.Fatal(err)
	}
	ref.MediaID = created.ID
	references[0].ID = int64(created.ID)
	repository.usages[created.ID] = []Usage{{Kind: FileFieldUsage, OwnerID: ref.OwnerID, Occurrence: &ref}}
	title := "PDF title"
	updated, err := s.Update(ctx, security.System(), UpdateInput{ID: created.ID, FileID: 2, Title: &title})
	if err != nil || updated.Title == nil || *updated.Title != title {
		t.Fatalf("PDF same-file metadata update: %#v, %v", updated, err)
	}
	if _, err := s.Update(ctx, security.System(), UpdateInput{ID: created.ID, FileID: 1}); err == nil {
		t.Fatal("incompatible MIME switch succeeded")
	}
	stored, err := s.Get(ctx, security.System(), created.ID)
	if err != nil || stored.FileID != 2 || *stored.Title != title {
		t.Fatalf("failed switch mutated Media: %#v, %v", stored, err)
	}
	// Every occurrence participates, including another nested owner constraint.
	references[0].Options.MIMETypes = nil
	other := ref
	other.OwnerKind, other.OwnerID, other.Container = "site", 1, "settings"
	other.Path = []string{"rows", "0", "icons", "1"}
	references = append(references, field.Reference{Path: other.Path, Target: field.ReferenceFile, ID: int64(created.ID), Options: field.FileOptions{Disk: "private"}})
	repository.usages[created.ID] = append(repository.usages[created.ID], Usage{Kind: FileFieldUsage, OwnerID: other.OwnerID, Occurrence: &other})
	_, err = s.Update(ctx, security.System(), UpdateInput{ID: created.ID, FileID: 2})
	var validation field.ValidationErrors
	if !errors.As(err, &validation) || validation[0].Key != "rows[0].icons[1]" {
		t.Fatalf("second occurrence disk guard: %v", err)
	}
}

func TestFileOccurrenceRejectsStalePathOrIdentity(t *testing.T) {
	ref := FileOccurrence{OwnerKind: "site", OwnerID: 1, Target: field.ReferenceFile, MediaID: 8, Path: []string{"icon"}}
	for _, references := range [][]field.Reference{nil, {{Target: field.ReferenceFile, ID: 9, Path: ref.Path}}, {{Target: field.ReferenceFile, ID: 8, Path: []string{"other"}}}, {{Target: field.ReferenceMedia, ID: 8, Path: ref.Path}}} {
		if err := ValidateFileOccurrence(ref, references, file.File{}); !errors.Is(err, ErrInvalidReference) {
			t.Fatalf("stale occurrence accepted: %v", err)
		}
	}
}
