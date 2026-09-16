package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestDependencyInvalidationCrossesAliasesStoresAndModulesWithinSite(t *testing.T) {
	filesystem := newTaggedStore("filesystem_cache")
	redis := newTaggedStore("redis_cache")
	manager := &Manager{
		stores: map[Code]Store{
			filesystem.Code(): filesystem,
			redis.Code():      redis,
		},
		coordinator: NewCoordinator(),
	}

	coreCaches, err := NewRuntimeModuleManager(
		manager,
		RuntimeScope{Profile: "dev", Site: "7"},
		"core",
		[]Binding{{Alias: "durable", Code: filesystem.Code()}},
	)
	if err != nil {
		t.Fatal(err)
	}
	seoCaches, err := NewRuntimeModuleManager(
		manager,
		RuntimeScope{Profile: "dev", Site: "7"},
		"seo",
		[]Binding{{Alias: "hot", Code: redis.Code()}},
	)
	if err != nil {
		t.Fatal(err)
	}
	siteBCaches, err := NewRuntimeModuleManager(
		manager,
		RuntimeScope{Profile: "dev", Site: "8"},
		"core",
		[]Binding{{Alias: "durable", Code: filesystem.Code()}},
	)
	if err != nil {
		t.Fatal(err)
	}

	coreStore, _ := coreCaches.Store("durable")
	seoStore, _ := seoCaches.Store("hot")
	siteBStore, _ := siteBCaches.Store("durable")
	for _, target := range []struct {
		store Store
		key   string
	}{
		{coreStore, "resource:id:42"},
		{seoStore, "seo:resource:42"},
		{siteBStore, "resource:id:42"},
	} {
		if err := target.store.Set(
			context.Background(),
			target.key,
			[]byte("cached"),
			SetOptions{Tags: []Tag{"resource:42"}},
		); err != nil {
			t.Fatal(err)
		}
	}

	if err := coreStore.InvalidateTag(
		context.Background(),
		"resource:42",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := coreStore.Get(context.Background(), "resource:id:42"); !errors.Is(err, ErrMiss) {
		t.Fatalf("core cache error = %v", err)
	}
	if _, err := seoStore.Get(context.Background(), "seo:resource:42"); !errors.Is(err, ErrMiss) {
		t.Fatalf("seo cache error = %v", err)
	}
	if _, err := siteBStore.Get(context.Background(), "resource:id:42"); err != nil {
		t.Fatalf("site B cache was invalidated: %v", err)
	}
}

func TestRuntimeCacheTargetRegistrationIsIdempotent(t *testing.T) {
	store := newTaggedStore("filesystem_cache")
	manager := &Manager{
		stores:      map[Code]Store{store.Code(): store},
		coordinator: NewCoordinator(),
	}
	for range 20 {
		_, err := NewRuntimeModuleManager(
			manager,
			RuntimeScope{Profile: "dev", Site: "7"},
			"core",
			[]Binding{{Alias: "durable", Code: store.Code()}},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	if count := len(manager.coordinator.targets); count != 1 {
		t.Fatalf("registered targets = %d", count)
	}
}

func TestApplicationInvalidationReachesEveryRegisteredSiteScope(t *testing.T) {
	store := newTaggedStore("filesystem_cache")
	manager := &Manager{
		stores:      map[Code]Store{store.Code(): store},
		coordinator: NewCoordinator(),
	}
	var scoped []Store
	for _, site := range []string{"7", "8"} {
		moduleCaches, err := NewRuntimeModuleManager(
			manager,
			RuntimeScope{Profile: "dev", Site: site},
			"core",
			[]Binding{{Alias: "durable", Code: store.Code()}},
		)
		if err != nil {
			t.Fatal(err)
		}
		target, _ := moduleCaches.Store("durable")
		if err := target.Set(
			context.Background(),
			"site:list",
			[]byte("value"),
			SetOptions{Tags: []Tag{"sites"}},
		); err != nil {
			t.Fatal(err)
		}
		scoped = append(scoped, target)
	}
	if err := manager.Invalidate(context.Background(), "sites"); err != nil {
		t.Fatal(err)
	}
	for index, target := range scoped {
		if _, err := target.Get(context.Background(), "site:list"); !errors.Is(err, ErrMiss) {
			t.Fatalf("site target %d error = %v", index, err)
		}
	}
}

type taggedStore struct {
	code   Code
	mu     sync.Mutex
	values map[string][]byte
	tags   map[string][]Tag
}

func newTaggedStore(code Code) *taggedStore {
	return &taggedStore{
		code: code, values: make(map[string][]byte), tags: make(map[string][]Tag),
	}
}

func (s *taggedStore) Code() Code               { return s.code }
func (*taggedStore) Ping(context.Context) error { return nil }
func (s *taggedStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.values[key]
	if !exists {
		return nil, ErrMiss
	}
	return append([]byte(nil), value...), nil
}
func (s *taggedStore) Set(
	_ context.Context,
	key string,
	value []byte,
	options SetOptions,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = append([]byte(nil), value...)
	s.tags[key] = append([]Tag(nil), options.Tags...)
	return nil
}
func (s *taggedStore) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.Get(ctx, key)
	return err == nil, nil
}
func (s *taggedStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	delete(s.tags, key)
	return nil
}
func (s *taggedStore) InvalidateTag(_ context.Context, tag Tag) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, tags := range s.tags {
		for _, current := range tags {
			if current == tag {
				delete(s.values, key)
				delete(s.tags, key)
				break
			}
		}
	}
	return nil
}
func (*taggedStore) Close() error { return nil }

var _ Store = (*taggedStore)(nil)
