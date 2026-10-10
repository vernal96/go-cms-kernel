package resource

import (
	"context"
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	corefile "github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

type referenceFileService struct {
	corefile.File
}

type referenceMediaService struct {
	media.Service
	item  media.ResolvedMedia
	err   error
	calls int
}

func (s *referenceMediaService) Resolve(context.Context, security.Actor, media.ID) (media.ResolvedMedia, error) {
	s.calls++
	return s.item, s.err
}

func TestFileReferenceValidationRequiresReadOnlyForNewValue(t *testing.T) {
	selected := &referenceMediaService{err: security.ErrForbidden}
	service := &Service{media: selected}
	references := []field.FileReference{{Key: "asset", ID: 7}}
	if err := service.validateFileReferences(context.Background(), security.User(1), references, map[string]media.ID{"asset": 7}); err != nil {
		t.Fatalf("unchanged reference failed: %v", err)
	}
	if selected.calls != 0 {
		t.Fatalf("unchanged reference lookups = %d", selected.calls)
	}
	if err := service.validateFileReferences(context.Background(), security.User(1), references, nil); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("new reference error = %v", err)
	}

	selected.err = nil
	selected.item = media.ResolvedMedia{File: corefile.File{ID: 17, Storage: filesystem.Code("public"), MIMEType: "image/png"}}
	references[0].Options = field.FileOptions{Disk: "public", VirtualPath: "assets", SettingsCode: "image", MIMETypes: []string{"image/*"}}
	if err := service.validateFileReferences(context.Background(), security.User(1), references, nil); err != nil {
		t.Fatalf("allowed reference failed: %v", err)
	}
	selected.item.File.Storage = "private"
	if err := service.validateFileReferences(context.Background(), security.User(1), references, nil); err == nil {
		t.Fatal("disallowed storage was accepted")
	}
}
