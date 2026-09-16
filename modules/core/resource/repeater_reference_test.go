package resource

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

func TestRepeaterReferenceServiceValidation(t *testing.T) {
	schema, err := field.CompilePersistent([]field.Definition{{Key: "slides", Type: field.TypeRepeater, Label: "Slides", Options: field.RepeaterOptions{Fields: []field.Definition{
		{Key: "image", Type: field.TypeMedia, Label: "Image"},
		{Key: "attachment", Type: field.TypeFile, Label: "Attachment", Options: field.FileOptions{MIMETypes: []string{"image/*"}}},
	}}}}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := schema.Validate(map[string]any{"slides": []any{map[string]any{"image": 7, "attachment": 8}}})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := schema.StoredValues(normalized)
	if err != nil {
		t.Fatal(err)
	}
	files, err := schema.FileReferences(normalized)
	if err != nil {
		t.Fatal(err)
	}
	mediaService := newTestMediaService()
	fileService := &referenceFileService{err: security.ErrForbidden}
	service := &Service{media: mediaService, files: fileService}
	ctx := context.Background()
	actor := security.User(1)
	if err := service.validateMediaFields(ctx, actor, stored); !errors.Is(err, media.ErrNotFound) || !strings.Contains(err.Error(), "slides[0].image") {
		t.Fatalf("missing media=%v", err)
	}
	mediaService.items[7] = media.ResolvedMedia{Media: media.Media{ID: 7}, File: file.File{MIMEType: "application/pdf"}}
	if err := service.validateMediaFields(ctx, actor, stored); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("non-image accepted: %v", err)
	}
	mediaService.items[7] = media.ResolvedMedia{Media: media.Media{ID: 7}, File: file.File{MIMEType: "image/png"}}
	if err := service.validateMediaFields(ctx, actor, stored); err != nil {
		t.Fatal(err)
	}
	if err := service.validateFileReferences(ctx, actor, files, nil); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("permission bypassed: %v", err)
	}
	fileService.err = nil
	fileService.item = file.File{ID: 8, MIMEType: "application/pdf"}
	if err := service.validateFileReferences(ctx, actor, files, nil); err == nil {
		t.Fatal("nested MIME restriction bypassed")
	}
	fileService.item.MIMEType = "image/png"
	if err := service.validateFileReferences(ctx, actor, files, nil); err != nil {
		t.Fatal(err)
	}
}
