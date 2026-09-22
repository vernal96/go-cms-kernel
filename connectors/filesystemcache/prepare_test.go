package filesystemcache

import (
	"context"
	"errors"
	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/connectors/localstorage"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"strings"
	"testing"
	"time"
)

func TestPreparedFilesystemWriteKeepsOriginalDependencyVersions(t *testing.T) {
	ctx := context.Background()
	disk, err := localstorage.New(ctx, localstorage.Config{Code: "private", Visibility: filesystem.VisibilityPrivate, Root: t.TempDir(), BaseURL: "http://localhost", SigningKey: strings.Repeat("k", 32)})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := New(ctx, Config{Code: "cache"}, disk)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := New(ctx, Config{Code: "cache"}, disk)
	if err != nil {
		t.Fatal(err)
	}
	fill, err := reader.Prepare(ctx, []cache.Tag{"resource:7"})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.InvalidateTag(ctx, "resource:7"); err != nil {
		t.Fatal(err)
	}
	if err = fill(ctx, "key", []byte("stale"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Get(ctx, "key"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("late fill remained valid: %v", err)
	}
}
