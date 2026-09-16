package redis

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
)

func TestRedisCacheOverwriteExpiryAndTags(t *testing.T) {
	backend := &memoryClient{values: make(map[string][]byte)}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	store := newConnector(
		Config{
			Code:   "redis",
			Prefix: "test",
			Now:    func() time.Time { return now },
			Random: bytes.NewReader(bytes.Repeat([]byte{9}, 128)),
		},
		backend,
	)

	if err := store.Set(
		context.Background(),
		"key",
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
		"key",
		[]byte("second"),
		cache.SetOptions{
			TTL:  time.Minute,
			Tags: []cache.Tag{"site:1"},
		},
	); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(context.Background(), "key")
	if err != nil || string(value) != "second" {
		t.Fatalf("value = %q, error = %v", value, err)
	}

	if err := store.InvalidateTag(
		context.Background(),
		"site:1",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(
		context.Background(),
		"key",
	); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("invalidated entry error = %v", err)
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

func TestRedisCacheMissErrorsAndLifecycle(t *testing.T) {
	backend := &memoryClient{
		values:  make(map[string][]byte),
		pingErr: errors.New("unavailable"),
	}
	store := newConnector(Config{Code: "redis"}, backend)
	if !errors.Is(
		store.Ping(context.Background()),
		backend.pingErr,
	) {
		t.Fatal("ping error was not propagated")
	}
	if _, err := store.Get(
		context.Background(),
		"missing",
	); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("missing error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if !backend.closed {
		t.Fatal("redis client was not closed")
	}
}

func TestRedisCacheTreatsCorruptEntryAsObservableMiss(t *testing.T) {
	backend := &memoryClient{values: make(map[string][]byte)}
	store := newConnector(Config{Code: "redis", Prefix: "test"}, backend)
	backend.values[store.entryKey("corrupt")] = []byte("bad")
	if _, err := store.Get(context.Background(), "corrupt"); !errors.Is(err, cache.ErrMiss) || !errors.Is(err, cache.ErrCorrupt) {
		t.Fatalf("corrupt entry error = %v", err)
	}
}

func TestRedisCacheRepairsCorruptTagToken(t *testing.T) {
	backend := &memoryClient{values: make(map[string][]byte)}
	store := newConnector(
		Config{
			Code:   "redis",
			Prefix: "test",
			Random: bytes.NewReader(bytes.Repeat([]byte{3}, 64)),
		},
		backend,
	)
	backend.values[store.tagKey("resource:7")] = []byte("bad")
	if err := store.Set(
		context.Background(),
		"resource:7",
		[]byte("value"),
		cache.SetOptions{Tags: []cache.Tag{"resource:7"}},
	); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(context.Background(), "resource:7")
	if err != nil || string(value) != "value" {
		t.Fatalf("value = %q, error = %v", value, err)
	}
}

func TestUniversalOptionsCoverStandaloneSentinelAndCluster(t *testing.T) {
	standalone, err := universalOptions(Config{
		Code:  "standalone",
		Addrs: []string{"redis:6379"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(standalone.Addrs) != 1 || standalone.MasterName != "" {
		t.Fatalf("standalone options = %#v", standalone)
	}

	sentinel, err := universalOptions(Config{
		Code:       "sentinel",
		Addrs:      []string{"one:26379", "two:26379"},
		MasterName: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sentinel.MasterName != "primary" {
		t.Fatalf("sentinel options = %#v", sentinel)
	}

	cluster, err := universalOptions(Config{
		Code:  "cluster",
		Addrs: []string{"one:6379", "two:6379"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cluster.Addrs) != 2 || cluster.MasterName != "" {
		t.Fatalf("cluster options = %#v", cluster)
	}
}

func TestRedisCacheConcurrentSetAndInvalidate(t *testing.T) {
	backend := &memoryClient{values: make(map[string][]byte)}
	store := newConnector(
		Config{
			Code:   "redis",
			Prefix: "concurrent",
			Random: rand.Reader,
		},
		backend,
	)
	var wait sync.WaitGroup
	for index := range 50 {
		wait.Add(2)
		go func(value byte) {
			defer wait.Done()
			_ = store.Set(
				context.Background(),
				"key",
				[]byte{value},
				cache.SetOptions{Tags: []cache.Tag{"site:1"}},
			)
		}(byte(index))
		go func() {
			defer wait.Done()
			_ = store.InvalidateTag(context.Background(), "site:1")
		}()
	}
	wait.Wait()

	if err := store.Set(
		context.Background(),
		"key",
		[]byte("final"),
		cache.SetOptions{Tags: []cache.Tag{"site:1"}},
	); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(context.Background(), "key")
	if err != nil || string(value) != "final" {
		t.Fatalf("final value = %q, error = %v", value, err)
	}
}

type memoryClient struct {
	mu      sync.Mutex
	values  map[string][]byte
	pingErr error
	closed  bool
}

func (c *memoryClient) Ping(context.Context) error {
	return c.pingErr
}

func (c *memoryClient) Get(
	_ context.Context,
	key string,
) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, exists := c.values[key]
	if !exists {
		return nil, cache.ErrMiss
	}
	return append([]byte(nil), value...), nil
}

func (c *memoryClient) Set(
	_ context.Context,
	key string,
	value []byte,
	_ time.Duration,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = append([]byte(nil), value...)
	return nil
}

func (c *memoryClient) Delete(
	_ context.Context,
	key string,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, key)
	return nil
}

func (c *memoryClient) Close() error {
	c.closed = true
	return nil
}

var _ client = (*memoryClient)(nil)
