package widget

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/connectors/filesystemcache"
	"github.com/vernal96/go-cms-kernel/connectors/localstorage"
	"github.com/vernal96/go-cms-kernel/filesystem"
)

type resultTestStore struct {
	*filesystemcache.Connector
	batches int
}

func (s *resultTestStore) GetMany(ctx context.Context, keys []string) map[string]cache.ReadResult {
	s.batches++
	result := make(map[string]cache.ReadResult, len(keys))
	for _, key := range keys {
		value, err := s.Connector.Get(ctx, key)
		result[key] = cache.ReadResult{Value: value, Err: err}
	}
	return result
}
func newResultTestStore(t *testing.T) *resultTestStore {
	t.Helper()
	ctx := context.Background()
	disk, err := localstorage.New(ctx, localstorage.Config{Code: "private", Visibility: filesystem.VisibilityPrivate, Root: t.TempDir(), BaseURL: "http://localhost", SigningKey: strings.Repeat("k", 32)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := filesystemcache.New(ctx, filesystemcache.Config{Code: "test"}, disk)
	if err != nil {
		t.Fatal(err)
	}
	return &resultTestStore{Connector: store}
}

type cacheTestInstance struct {
	calls    atomic.Int32
	render   func() (map[string]any, error)
	deadline time.Time
}

func (i *cacheTestInstance) Render(context.Context, RenderInput) (map[string]any, error) {
	i.calls.Add(1)
	if i.render != nil {
		return i.render()
	}
	return map[string]any{"text": "result"}, nil
}
func (i *cacheTestInstance) ResultCachePolicy(RenderInput) ResultCachePolicy {
	return ResultCachePolicy{TTL: time.Minute, Tags: []cache.Tag{"collection"}}
}
func (i *cacheTestInstance) ResultCacheDeadline() time.Time { return i.deadline }

func TestWidgetResultsBatchIdentityInvalidationAndRuntimeIsolation(t *testing.T) {
	ctx := context.Background()
	store := newResultTestStore(t)
	first, second := &cacheTestInstance{}, &cacheTestInstance{}
	jobs := []RenderJob{{Identity: "first", Fingerprint: map[string]any{"value": "a"}, Instance: first, Input: RenderInput{Site: SiteSnapshot{ID: 1}, Resource: ResourceSnapshot{ID: 7}}}, {Identity: "second", Fingerprint: "b", Instance: second}}
	render := func(generation string) {
		t.Helper()
		for _, result := range RenderBatch(ctx, store, generation, jobs) {
			if result.Err != nil || result.Data["text"] != "result" {
				t.Fatalf("render: %+v", result)
			}
		}
	}
	render("one")
	render("one")
	if first.calls.Load() != 1 || second.calls.Load() != 1 || store.batches != 2 {
		t.Fatalf("warm render calls=%d,%d batches=%d", first.calls.Load(), second.calls.Load(), store.batches)
	}
	jobs[0].Fingerprint = map[string]any{"value": "edited"}
	render("one")
	if first.calls.Load() != 2 || second.calls.Load() != 1 {
		t.Fatal("editing one instance discarded the other result")
	}
	jobs[0], jobs[1] = jobs[1], jobs[0]
	render("one")
	if first.calls.Load() != 2 || second.calls.Load() != 1 {
		t.Fatal("reordering repeated render")
	}
	jobs[1].Input.Resource.Content = "new content"
	render("one")
	if first.calls.Load() != 3 {
		t.Fatal("resource dependency missing from key")
	}
	jobs[1].Input.Site.ID = 2
	render("one")
	if first.calls.Load() != 4 {
		t.Fatal("site identity missing from key")
	}
	if err := store.InvalidateTag(ctx, "collection"); err != nil {
		t.Fatal(err)
	}
	render("one")
	if first.calls.Load() != 5 || second.calls.Load() != 2 {
		t.Fatal("external dependency invalidation failed")
	}
	render("two")
	if first.calls.Load() != 6 || second.calls.Load() != 3 {
		t.Fatal("runtime generation reused old results")
	}
}

func TestWidgetResultLateFillAndPublicationDeadline(t *testing.T) {
	ctx := context.Background()
	store := newResultTestStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	instance := &cacheTestInstance{render: func() (map[string]any, error) { close(started); <-release; return map[string]any{"text": "old"}, nil }}
	job := RenderJob{Identity: "race", Instance: instance}
	done := make(chan []RenderResult, 1)
	go func() { done <- RenderBatch(ctx, store, "runtime", []RenderJob{job}) }()
	<-started
	if err := store.InvalidateTag(ctx, "collection"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if result := <-done; result[0].Err != nil {
		t.Fatal(result[0].Err)
	}
	instance.render = nil
	if got := RenderBatch(ctx, store, "runtime", []RenderJob{job})[0]; got.Err != nil || got.Data["text"] != "result" || instance.calls.Load() != 2 {
		t.Fatalf("stale result survived mutation: %+v", got)
	}
	bounded := &cacheTestInstance{deadline: time.Now().Add(-time.Second)}
	job = RenderJob{Identity: "scheduled", Instance: bounded}
	RenderBatch(ctx, store, "runtime", []RenderJob{job})
	RenderBatch(ctx, store, "runtime", []RenderJob{job})
	if bounded.calls.Load() != 2 {
		t.Fatal("result cached past publication deadline")
	}
}

func TestWidgetResultErrorsAndOptOutAreNotCached(t *testing.T) {
	ctx := context.Background()
	store := newResultTestStore(t)
	failed := &cacheTestInstance{render: func() (map[string]any, error) { return nil, errors.New("failed") }}
	nilData := &cacheTestInstance{render: func() (map[string]any, error) { return nil, nil }}
	var uncachedCalls int
	uncached, _ := (Functional{Render: func(context.Context, RenderInput, map[string]any) (map[string]any, error) {
		uncachedCalls++
		return map[string]any{"ok": true}, nil
	}}).New(nil)
	jobs := []RenderJob{{Identity: "failed", Instance: failed}, {Identity: "nil", Instance: nilData}, {Identity: "private", Instance: uncached}}
	for i := 0; i < 2; i++ {
		results := RenderBatch(ctx, store, "runtime", jobs)
		if results[0].Err == nil || results[2].Data == nil {
			t.Fatal("per-widget isolation failed")
		}
	}
	if failed.calls.Load() != 2 || nilData.calls.Load() != 2 || uncachedCalls != 2 {
		t.Fatal("failure or opt-out was cached")
	}
}
