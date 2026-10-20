package filesystemcache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/connectors/localstorage"
	"github.com/vernal96/go-cms-kernel/filesystem"
)

func TestLocalFilesystemCacheOverwriteExpiryAndTags(t *testing.T) {
	root := t.TempDir()
	disk, err := localstorage.New(
		context.Background(),
		localstorage.Config{
			Code:       "private",
			Visibility: filesystem.VisibilityPrivate,
			Root:       root,
			BaseURL:    "http://localhost:8080",
			SigningKey: strings.Repeat("k", 32),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	store, err := New(
		context.Background(),
		Config{
			Code:   "files",
			Disk:   "private",
			Layout: LayoutAuto,
			Prefix: "cache/test",
			Now:    func() time.Time { return now },
			Random: bytes.NewReader(append(
				bytes.Repeat([]byte{7}, 32),
				bytes.Repeat([]byte{8}, 96)...,
			)),
		},
		disk,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Set(
		context.Background(),
		"alpha",
		[]byte("first"),
		cache.SetOptions{
			TTL:  time.Minute,
			Tags: []cache.Tag{"site:1"},
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(
		context.Background(),
		"alpha",
		[]byte("second"),
		cache.SetOptions{
			TTL:  time.Minute,
			Tags: []cache.Tag{"site:1"},
		},
	); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(context.Background(), "alpha")
	if err != nil || string(value) != "second" {
		t.Fatalf("value = %q, error = %v", value, err)
	}

	sum := sha256.Sum256([]byte("alpha"))
	hash := hex.EncodeToString(sum[:])
	expectedPath := filepath.Join(
		root,
		"cache",
		"test",
		"entries",
		hash[:2],
		hash[2:4],
		hash,
	)
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("sharded cache entry is missing: %v", err)
	}

	if err := store.InvalidateTag(
		context.Background(),
		"site:1",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(
		context.Background(),
		"alpha",
	); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("invalidated entry error = %v", err)
	}
	if _, err := os.Stat(expectedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalidated entry was not lazily deleted: %v", err)
	}

	if err := store.Set(
		context.Background(),
		"expires",
		[]byte("value"),
		cache.SetOptions{TTL: time.Second},
	); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if exists, err := store.Exists(
		context.Background(),
		"expires",
	); err != nil || exists {
		t.Fatalf("expired exists = %v, error = %v", exists, err)
	}
}

func TestFilesystemCacheTreatsCorruptEntryAsMiss(t *testing.T) {
	root := t.TempDir()
	disk, err := localstorage.New(
		context.Background(),
		localstorage.Config{
			Code:       "private",
			Visibility: filesystem.VisibilityPrivate,
			Root:       root,
			BaseURL:    "http://localhost:8080",
			SigningKey: strings.Repeat("k", 32),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(
		context.Background(),
		Config{
			Code:   "files",
			Disk:   "private",
			Layout: LayoutFlat,
			Prefix: "cache/test",
		},
		disk,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(
		context.Background(),
		"corrupt",
		[]byte("value"),
		cache.SetOptions{},
	); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("corrupt"))
	objectPath := filepath.Join(
		root,
		"cache",
		"test",
		"entries",
		hex.EncodeToString(sum[:]),
	)
	if err := os.WriteFile(objectPath, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, getErr := store.Get(context.Background(), "corrupt")
	if !errors.Is(getErr, cache.ErrMiss) || !errors.Is(getErr, cache.ErrCorrupt) {
		t.Fatalf("corrupt entry error = %v", getErr)
	}
}

func TestFilesystemCachePruneAndFlush(t *testing.T) {
	disk, err := localstorage.New(
		context.Background(),
		localstorage.Config{
			Code:       "private",
			Visibility: filesystem.VisibilityPrivate,
			Root:       t.TempDir(),
			BaseURL:    "http://localhost:8080",
			SigningKey: strings.Repeat("k", 32),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	store, err := New(context.Background(), Config{
		Code:   "files",
		Disk:   "private",
		Prefix: "cache/maintenance",
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(append(
			bytes.Repeat([]byte{4}, 32),
			bytes.Repeat([]byte{5}, 96)...,
		)),
	}, disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "keep", []byte("value"), cache.SetOptions{TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "expired", []byte("value"), cache.SetOptions{TTL: time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(
		context.Background(),
		"stale",
		[]byte("value"),
		cache.SetOptions{Tags: []cache.Tag{"resource:7"}},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.InvalidateTag(context.Background(), "resource:7"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := store.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Get(context.Background(), "keep"); err != nil || string(value) != "value" {
		t.Fatalf("kept value = %q, error = %v", value, err)
	}
	for _, key := range []string{"expired", "stale"} {
		if _, err := store.Get(context.Background(), key); !errors.Is(err, cache.ErrMiss) {
			t.Fatalf("pruned key %q error = %v", key, err)
		}
	}
	if err := store.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "keep"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("flushed value error = %v", err)
	}
}

func TestFilesystemCacheMissingTagGenerationInvalidatesEntry(t *testing.T) {
	ctx := context.Background()
	disk := &memoryDisk{
		code:         "private",
		visibility:   filesystem.VisibilityPrivate,
		distribution: filesystem.KeyDistributionSelfManaged,
		values:       make(map[string][]byte),
	}
	store, err := New(ctx, Config{Code: "cache", Prefix: "cache/missing-tag"}, disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(
		ctx,
		"resource",
		[]byte("stale"),
		cache.SetOptions{Tags: []cache.Tag{"resource:7"}},
	); err != nil {
		t.Fatal(err)
	}
	if err := disk.Delete(ctx, store.tagKey("resource:7")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "resource"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("entry with missing tag generation error = %v, want cache miss", err)
	}
}

func TestFilesystemCacheFlushUsesBoundedPrefixScan(t *testing.T) {
	ctx := context.Background()
	disk := &scanningMemoryDisk{memoryDisk: &memoryDisk{
		code:         "private",
		visibility:   filesystem.VisibilityPrivate,
		distribution: filesystem.KeyDistributionSelfManaged,
		values:       make(map[string][]byte),
	}}
	store, err := New(ctx, Config{Code: "cache", Prefix: "cache/flush"}, disk)
	if err != nil {
		t.Fatal(err)
	}
	entryCount := 2*flushScanPageSize + 1
	for index := 0; index < entryCount; index++ {
		key := fmt.Sprintf("key:%d", index)
		if err := store.Set(ctx, key, []byte("value"), cache.SetOptions{}); err != nil {
			t.Fatalf("set %q: %v", key, err)
		}
	}
	if err := store.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(disk.values) != 0 {
		t.Fatalf("flush left %d filesystem cache objects", len(disk.values))
	}
	if len(disk.limits) < 3 {
		t.Fatalf("prefix scan calls = %d, want multiple bounded pages", len(disk.limits))
	}
	for _, limit := range disk.limits {
		if limit != flushScanPageSize {
			t.Fatalf("prefix scan limit = %d, want %d", limit, flushScanPageSize)
		}
	}
}

func TestFilesystemCacheAutoUsesFlatLayoutForSelfManagedDisk(t *testing.T) {
	disk := &memoryDisk{
		code:         "private",
		visibility:   filesystem.VisibilityPrivate,
		distribution: filesystem.KeyDistributionSelfManaged,
		values:       make(map[string][]byte),
	}
	store, err := New(
		context.Background(),
		Config{
			Code:   "s3-cache",
			Disk:   disk.code,
			Layout: LayoutAuto,
			Prefix: "cache/s3",
		},
		disk,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(
		context.Background(),
		"key",
		[]byte("value"),
		cache.SetOptions{},
	); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("key"))
	expected := "cache/s3/entries/" + hex.EncodeToString(sum[:])
	if _, exists := disk.values[expected]; !exists {
		t.Fatalf("flat entry %q is missing; keys = %v", expected, disk.keys())
	}
}

func TestFilesystemCacheRejectsPublicDisk(t *testing.T) {
	disk := &memoryDisk{
		code:         "public",
		visibility:   filesystem.VisibilityPublic,
		distribution: filesystem.KeyDistributionHierarchical,
		values:       make(map[string][]byte),
	}
	if _, err := New(
		context.Background(),
		Config{Code: "cache", Disk: disk.code},
		disk,
	); err == nil {
		t.Fatal("public filesystem disk was accepted")
	}
}

type memoryDisk struct {
	code         filesystem.Code
	visibility   filesystem.Visibility
	distribution filesystem.KeyDistribution
	values       map[string][]byte
}

type scanningMemoryDisk struct {
	*memoryDisk
	limits []int
}

type memoryPrefixScan struct {
	disk   *scanningMemoryDisk
	keys   []string
	offset int
}

func (d *scanningMemoryDisk) OpenPrefixScan(
	_ context.Context,
	prefix string,
) (filesystem.PrefixScan, error) {
	keys := make([]string, 0, len(d.values))
	for key := range d.values {
		if strings.HasPrefix(key, prefix+"/") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return &memoryPrefixScan{disk: d, keys: keys}, nil
}

func (s *memoryPrefixScan) Next(
	_ context.Context,
	limit int,
) (filesystem.PrefixScanPage, error) {
	s.disk.limits = append(s.disk.limits, limit)
	end := s.offset + limit
	if end > len(s.keys) {
		end = len(s.keys)
	}
	page := filesystem.PrefixScanPage{
		Keys: append([]string(nil), s.keys[s.offset:end]...),
		Done: end == len(s.keys),
	}
	s.offset = end
	return page, nil
}

func (*memoryPrefixScan) Close() error { return nil }

func (d *memoryDisk) Code() filesystem.Code {
	return d.code
}

func (d *memoryDisk) Visibility() filesystem.Visibility {
	return d.visibility
}

func (*memoryDisk) Ping(context.Context) error {
	return nil
}

func (d *memoryDisk) PutNew(
	ctx context.Context,
	key string,
	source io.Reader,
	contentType string,
) error {
	if _, exists := d.values[key]; exists {
		return filesystem.ErrConflict
	}
	return d.Put(ctx, key, source, contentType)
}

func (d *memoryDisk) Put(
	_ context.Context,
	key string,
	source io.Reader,
	_ string,
) error {
	value, err := io.ReadAll(source)
	if err != nil {
		return err
	}
	d.values[key] = append([]byte(nil), value...)
	return nil
}

func (d *memoryDisk) Open(
	_ context.Context,
	key string,
) (io.ReadCloser, error) {
	value, exists := d.values[key]
	if !exists {
		return nil, filesystem.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}

func (d *memoryDisk) Delete(
	_ context.Context,
	key string,
) error {
	delete(d.values, key)
	return nil
}

func (*memoryDisk) URL(
	context.Context,
	filesystem.Reference,
) (string, error) {
	return "", filesystem.ErrUnsupported
}

func (*memoryDisk) TemporaryURL(
	context.Context,
	filesystem.Reference,
	time.Time,
) (string, error) {
	return "", filesystem.ErrUnsupported
}

func (*memoryDisk) Close() error {
	return nil
}

func (d *memoryDisk) KeyDistribution() filesystem.KeyDistribution {
	return d.distribution
}

func (d *memoryDisk) keys() []string {
	result := make([]string, 0, len(d.values))
	for key := range d.values {
		result = append(result, key)
	}
	return result
}

var _ filesystem.Disk = (*memoryDisk)(nil)
var _ filesystem.OverwriteDisk = (*memoryDisk)(nil)
var _ filesystem.KeyDistributionProvider = (*memoryDisk)(nil)
