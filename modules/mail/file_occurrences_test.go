package mail

import (
	"context"
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
)

func TestMailStaticAttachmentOccurrenceValidationChecksSavedIdentityDiskAndMIME(t *testing.T) {
	t.Parallel()
	template := mailTemplate()
	template.ID = 3
	template.Attachments = []AttachmentTemplate{{Source: AttachmentStatic, MediaID: mediaIDPointer(7)}}
	repository := &memoryRepository{template: template}
	renderer, _ := testRenderer(t, SenderPolicy{})
	service, err := NewService(5, repository, renderer, allowAuthorizer{}, testUsers{}, nil, Limits{MaxRecipients: 10, MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20}, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	service.staticAttachmentMedia = field.MediaOptions{Disk: "documents", VirtualPath: "mail", SettingsCode: "mail_attachment", MIMETypes: []string{"application/pdf"}}
	runtime := &Runtime{service: service}
	ref := media.FileOccurrence{OwnerKind: staticAttachmentOwnerKind, OwnerID: 3, SiteID: 5, Container: "attachments", Path: []string{"0", "media_id"}, MediaID: 7, Target: field.ReferenceMedia}
	target := file.File{ID: 7, Storage: "documents", MIMEType: "application/pdf"}
	if err := runtime.ValidateMediaFileOccurrence(context.Background(), ref, target); err != nil {
		t.Fatalf("valid occurrence rejected: %v", err)
	}

	stale := ref
	stale.MediaID = 8
	if err := runtime.ValidateMediaFileOccurrence(context.Background(), stale, target); !errors.Is(err, media.ErrInvalidReference) {
		t.Fatalf("stale media identity error = %v", err)
	}
	target.MIMEType = "image/png"
	if err := runtime.ValidateMediaFileOccurrence(context.Background(), ref, target); err == nil {
		t.Fatal("media update bypassed the static attachment MIME constraint")
	}
	target.MIMEType, target.Storage = "application/pdf", "public"
	if err := runtime.ValidateMediaFileOccurrence(context.Background(), ref, target); err == nil {
		t.Fatal("media update bypassed the static attachment disk constraint")
	}
	if err := runtime.ValidateMediaFileOccurrence(
		context.Background(),
		media.FileOccurrence{
			OwnerKind: staticAttachmentOwnerKind,
			OwnerID:   3,
			SiteID:    6,
			Container: "attachments",
			Path:      ref.Path,
			MediaID:   7,
			Target:    field.ReferenceMedia,
		},
		file.File{Storage: "documents", MIMEType: "application/pdf"},
	); !errors.Is(err, media.ErrInvalidReference) {
		t.Fatalf("cross-site occurrence error = %v", err)
	}
}
