package cache_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/connectors/filesystemcache"
	"github.com/vernal96/go-cms-kernel/connectors/localstorage"
	"github.com/vernal96/go-cms-kernel/filesystem"
)

func stores(t *testing.T) (cache.Store, cache.Store) {
	t.Helper()
	ctx := context.Background()
	disk, err := localstorage.New(ctx, localstorage.Config{Code: "test", Visibility: filesystem.VisibilityPrivate, Root: t.TempDir(), BaseURL: "http://localhost", SigningKey: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	a, err := filesystemcache.New(ctx, filesystemcache.Config{Code: "test"}, disk)
	if err != nil {
		t.Fatal(err)
	}
	b, err := filesystemcache.New(ctx, filesystemcache.Config{Code: "test"}, disk)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}
func TestRememberRejectsLateFillAfterIndependentInvalidation(t *testing.T) {
	a, b := stores(t)
	ctx := context.Background()
	opts := cache.SetOptions{TTL: time.Minute, Tags: []cache.Tag{"resource:1"}}
	_, err := cache.RememberJSON(ctx, a, "menu", opts, func(context.Context) (string, error) { return "old", b.InvalidateTag(ctx, "resource:1") })
	if err != nil {
		t.Fatal(err)
	}
	value, err := cache.RememberJSON(ctx, b, "menu", opts, func(context.Context) (string, error) { return "new", nil })
	if err != nil || value != "new" {
		t.Fatalf("late fill survived: %q %v", value, err)
	}
}
func TestRememberConcurrentColdKeyLoadsOnce(t *testing.T) {
	a, _ := stores(t)
	var calls atomic.Int32
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(32)
	for range 32 {
		done.Go(func() {
			ready.Done()
			<-start
			value, err := cache.RememberJSON(context.Background(), a, "cold", cache.SetOptions{TTL: time.Minute}, func(context.Context) (string, error) { calls.Add(1); return "value", nil })
			if err != nil || value != "value" {
				t.Errorf("%q %v", value, err)
			}
		})
	}
	ready.Wait()
	close(start)
	done.Wait()
	if calls.Load() != 1 {
		t.Fatalf("loads: %d", calls.Load())
	}
}
