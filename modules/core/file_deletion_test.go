package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type deletionPermission struct {
	denied permission.Code
	calls  []permission.Code
}

func (a *deletionPermission) Check(_ context.Context, _ security.Actor, code permission.Code) error {
	a.calls = append(a.calls, code)
	if code == a.denied {
		return security.ErrForbidden
	}
	return nil
}
func (a *deletionPermission) Allowed(ctx context.Context, actor security.Actor, codes []permission.Code) ([]permission.Code, error) {
	var allowed []permission.Code
	for _, code := range codes {
		if err := a.Check(ctx, actor, code); err == nil {
			allowed = append(allowed, code)
		} else if !errors.Is(err, security.ErrForbidden) {
			return nil, err
		}
	}
	return allowed, nil
}

func TestFileDeletionRequiresBothFileAndMediaDeleteBeforeRepositoryAccess(t *testing.T) {
	for _, code := range []permission.Code{"core.file.delete", "core.media.delete"} {
		auth := &deletionPermission{denied: code}
		service := &MediaFileDeletions{authorizer: auth}
		_, err := service.Delete(context.Background(), security.User(1), media.DeleteFileInput{SiteID: 7, MediaID: 1, ExpectedFileID: 2, ExpectedUpdatedAt: time.Now()})
		if !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("permission %s: %v", code, err)
		}
	}
}

type deletionDisk struct {
	filesystem.Disk
	present map[string]bool
	fail    string
}

func (d *deletionDisk) Delete(_ context.Context, path string) error {
	if path == d.fail {
		return errors.New("disk offline")
	}
	if !d.present[path] {
		return filesystem.ErrNotFound
	}
	delete(d.present, path)
	return nil
}

type deletionDisks struct{ disk *deletionDisk }

func (d deletionDisks) Disk(filesystem.Code) (filesystem.Disk, bool) { return d.disk, true }

func TestPhysicalDeletionRetriesAfterPartialFailureAndAcceptsAlreadyMissingBytes(t *testing.T) {
	disk := &deletionDisk{present: map[string]bool{"first": true, "second": true}, fail: "second"}
	service := &MediaFileDeletions{disks: deletionDisks{disk}}
	files := []file.File{{ID: 1, Storage: "public", Path: "first"}, {ID: 2, Storage: "public", Path: "second"}}
	if err := service.deletePhysical(context.Background(), files); err == nil || disk.present["first"] == true || !disk.present["second"] {
		t.Fatal("invalid partial cleanup", err, disk.present)
	}
	disk.fail = ""
	if err := service.deletePhysical(context.Background(), files); err != nil || len(disk.present) != 0 {
		t.Fatal("idempotent retry failed", err, disk.present)
	}
}
