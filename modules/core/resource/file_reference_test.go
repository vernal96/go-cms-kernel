package resource

import (
	"context"
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	corefile "github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

type referenceFileService struct {
	corefile.Service
	item  corefile.File
	err   error
	calls int
}

func (s *referenceFileService) GetFile(context.Context, security.Actor, corefile.ID) (corefile.File, error) {
	s.calls++
	return s.item, s.err
}

func TestFileReferenceValidationRequiresReadOnlyForNewValue(t *testing.T) {
	files := &referenceFileService{err: security.ErrForbidden}
	service := &Service{files: files}
	references := []field.FileReference{{Key: "asset", ID: 7}}
	if err := service.validateFileReferences(context.Background(), security.User(1), references, map[string]corefile.ID{"asset": 7}); err != nil {
		t.Fatalf("unchanged reference failed: %v", err)
	}
	if files.calls != 0 {
		t.Fatalf("unchanged reference lookups = %d", files.calls)
	}
	if err := service.validateFileReferences(context.Background(), security.User(1), references, nil); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("new reference error = %v", err)
	}

	files.err = nil
	files.item = corefile.File{ID: 7, Storage: filesystem.Code("public"), MIMEType: "image/png"}
	references[0].Options = field.FileOptions{Storages: []filesystem.Code{"public"}, MIMETypes: []string{"image/*"}}
	if err := service.validateFileReferences(context.Background(), security.User(1), references, nil); err != nil {
		t.Fatalf("allowed reference failed: %v", err)
	}
	files.item.Storage = "private"
	if err := service.validateFileReferences(context.Background(), security.User(1), references, nil); err == nil {
		t.Fatal("disallowed storage was accepted")
	}
}
