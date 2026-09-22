package redis

import (
	"context"
	"errors"
	"github.com/vernal96/go-cms-kernel/cache"
	"testing"
	"time"
)

type countingBatchClient struct {
	*memoryClient
	batches int
}

func (c *countingBatchClient) GetMany(ctx context.Context, keys []string) map[string]cache.ReadResult {
	c.batches++
	return c.memoryClient.GetMany(ctx, keys)
}

func TestBatchReadsShareTokenRoundTripAndIsolateFailures(t *testing.T) {
	ctx := context.Background()
	backend := &countingBatchClient{memoryClient: &memoryClient{values: make(map[string][]byte)}}
	store := newConnector(Config{Code: "batch"}, backend)
	for _, key := range []string{"a", "b", "stale"} {
		tag := cache.Tag("shared")
		if key == "stale" {
			tag = "old"
		}
		if err := store.Set(ctx, key, []byte(key), cache.SetOptions{TTL: time.Minute, Tags: []cache.Tag{tag}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.InvalidateTag(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	backend.values[store.entryKey("corrupt")] = []byte("invalid")
	backend.batches = 0
	result := store.GetMany(ctx, []string{"a", "b", "stale", "missing", "corrupt", "a"})
	if backend.batches != 2 {
		t.Fatalf("round trips=%d", backend.batches)
	}
	for _, key := range []string{"a", "b"} {
		if result[key].Err != nil || string(result[key].Value) != key {
			t.Fatalf("%s: %+v", key, result[key])
		}
	}
	for _, key := range []string{"stale", "missing", "corrupt"} {
		if !errors.Is(result[key].Err, cache.ErrMiss) {
			t.Fatalf("%s: %+v", key, result[key])
		}
	}
	if !errors.Is(result["corrupt"].Err, cache.ErrCorrupt) {
		t.Fatal("corruption is not observable")
	}
}

func TestPreparedWriteCannotReviveInvalidatedValue(t *testing.T) {
	ctx := context.Background()
	backend := &memoryClient{values: make(map[string][]byte)}
	reader := newConnector(Config{Code: "shared"}, backend)
	writer := newConnector(Config{Code: "shared"}, backend)
	fill, err := reader.Prepare(ctx, []cache.Tag{"resource:7"})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.InvalidateTag(ctx, "resource:7"); err != nil {
		t.Fatal(err)
	}
	if err = fill(ctx, "resource", []byte("old"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Get(ctx, "resource"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("late fill revived stale value: %v", err)
	}
	fresh, err := reader.Prepare(ctx, []cache.Tag{"resource:7"})
	if err != nil {
		t.Fatal(err)
	}
	if err = fresh(ctx, "resource", []byte("new"), time.Minute); err != nil {
		t.Fatal(err)
	}
	value, err := writer.Get(ctx, "resource")
	if err != nil || string(value) != "new" {
		t.Fatalf("fresh fill=%q %v", value, err)
	}
}
