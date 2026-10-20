package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
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

func TestRuntimeTargetUpdatesBoundTargetsAndPreserveInvalidation(t *testing.T) {
	physical := newTaggedStore("filesystem_cache")
	manager := &Manager{
		stores:      map[Code]Store{physical.Code(): physical},
		coordinator: NewCoordinator(),
	}
	makeTargets := func(siteID string, store Store) []RuntimeTarget {
		targets := NewRuntimeTargetSet()
		_, err := NewRuntimeModuleManager(
			manager,
			RuntimeScope{Profile: "dev", Site: siteID, Targets: targets},
			"core",
			[]Binding{{Alias: "durable", Code: physical.Code()}},
		)
		if err != nil {
			t.Fatal(err)
		}
		// The collector uses the resolver's configured physical store. Keep the
		// parameter to make each candidate runtime's physical store explicit.
		if store.Code() != physical.Code() {
			t.Fatal("candidate store code does not match configured binding")
		}
		return targets.Targets()
	}
	countTargets := func() int {
		manager.coordinator.mu.RLock()
		defer manager.coordinator.mu.RUnlock()
		count := 0
		for _, targets := range manager.coordinator.runtimeScopes {
			count += len(targets)
		}
		return count
	}

	initial := makeTargets("7", physical)
	update, err := manager.PrepareRuntimeTargets(map[string][]RuntimeTarget{"7": initial})
	if err != nil {
		t.Fatal(err)
	}
	update.Publish()
	if count := countTargets(); count != 1 {
		t.Fatalf("initial active target count = %d", count)
	}
	currentStore := Store(physical)

	for iteration := range 25 {
		candidate := newTaggedStore(Code(fmt.Sprintf("candidate_%d", iteration)))
		// Candidate stores are resolved independently from the manager used by
		// the normal runtime manager; use a one-store resolver for this scope.
		candidateManager := &Manager{
			stores:      map[Code]Store{candidate.Code(): candidate},
			coordinator: manager.coordinator,
		}
		targets := NewRuntimeTargetSet()
		_, err := NewRuntimeModuleManager(
			candidateManager,
			RuntimeScope{Profile: "dev", Site: "7", Targets: targets},
			"core",
			[]Binding{{Alias: "durable", Code: candidate.Code()}},
		)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := manager.PrepareRuntimeTargets(map[string][]RuntimeTarget{
			"7": targets.Targets(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if count := countTargets(); count != 2 {
			t.Fatalf("prepare must retain old and candidate stores, got %d", count)
		}
		tag := Tag(scopedValue("sites/7/dependencies", "resource:1"))
		for index, store := range []Store{currentStore, candidate} {
			key := fmt.Sprintf("lifecycle:%d:%d", iteration, index)
			if err := store.Set(
				context.Background(),
				key,
				[]byte("cached"),
				SetOptions{Tags: []Tag{tag}},
			); err != nil {
				t.Fatal(err)
			}
		}
		if err := manager.Invalidate(context.Background(), "resource:1"); err != nil {
			t.Fatal(err)
		}
		for index, store := range []Store{currentStore, candidate} {
			key := fmt.Sprintf("lifecycle:%d:%d", iteration, index)
			if _, err := store.Get(context.Background(), key); !errors.Is(err, ErrMiss) {
				t.Fatalf("prepared target %d was missed by invalidation: %v", index, err)
			}
		}
		prepared.Abort()
		if count := countTargets(); count != 1 {
			t.Fatalf("aborted candidate leaked targets: %d", count)
		}

		prepared, err = manager.PrepareRuntimeTargets(map[string][]RuntimeTarget{
			"7": targets.Targets(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := candidate.Set(
			context.Background(),
			"published-candidate",
			[]byte("cached"),
			SetOptions{Tags: []Tag{tag}},
		); err != nil {
			t.Fatal(err)
		}
		if err := manager.Invalidate(context.Background(), "resource:1"); err != nil {
			t.Fatal(err)
		}
		if _, err := candidate.Get(
			context.Background(), "published-candidate",
		); !errors.Is(err, ErrMiss) {
			t.Fatalf("candidate target missed invalidation before publish: %v", err)
		}
		prepared.Publish()
		if count := countTargets(); count != 1 {
			t.Fatalf("published rebuild leaked old targets: %d", count)
		}
		currentStore = candidate
	}

	deletion, err := manager.PrepareRuntimeTargets(map[string][]RuntimeTarget{})
	if err != nil {
		t.Fatal(err)
	}
	deletion.Publish()
	if count := countTargets(); count != 0 {
		t.Fatalf("deleted site retained cache targets: %d", count)
	}
}

func TestFailedRuntimeCandidateDoesNotRegisterTargets(t *testing.T) {
	store := newTaggedStore("filesystem_cache")
	manager := &Manager{
		stores:      map[Code]Store{store.Code(): store},
		coordinator: NewCoordinator(),
	}
	targets := NewRuntimeTargetSet()
	_, err := NewRuntimeModuleManager(
		manager,
		RuntimeScope{Profile: "dev", Site: "failed-site", Targets: targets},
		"core",
		[]Binding{{Alias: "durable", Code: store.Code()}},
	)
	if err != nil {
		t.Fatal(err)
	}
	// A later module build failure drops this candidate's target set without
	// changing active registrations.
	manager.coordinator.mu.RLock()
	defer manager.coordinator.mu.RUnlock()
	if len(manager.coordinator.runtimeScopes) != 0 {
		t.Fatalf("failed candidate registered runtime scopes: %d", len(manager.coordinator.runtimeScopes))
	}
}

func TestRuntimeTargetUpdatesSerializeWithConcurrentInvalidation(t *testing.T) {
	const scope = "sites/7/dependencies"
	old := &blockingTaggedStore{
		Store: newTaggedStore("old"), entered: make(chan struct{}), release: make(chan struct{}),
	}
	candidate := &blockingTaggedStore{
		Store: newTaggedStore("candidate"), entered: make(chan struct{}), release: make(chan struct{}),
	}
	manager := &Manager{coordinator: NewCoordinator()}
	active, err := manager.PrepareRuntimeTargets(map[string][]RuntimeTarget{
		"7": {{Scope: scope, Store: old}},
	})
	if err != nil {
		t.Fatal(err)
	}
	active.Publish()
	update, err := manager.PrepareRuntimeTargets(map[string][]RuntimeTarget{
		"7": {{Scope: scope, Store: candidate}},
	})
	if err != nil {
		t.Fatal(err)
	}

	invalidationDone := make(chan error, 1)
	go func() {
		invalidationDone <- manager.Invalidate(context.Background(), "resource:1")
	}()
	select {
	case <-old.entered:
	case <-candidate.entered:
	case <-time.After(time.Second):
		t.Fatal("invalidation did not reach a staged target")
	}

	publishDone := make(chan struct{})
	go func() {
		update.Publish()
		close(publishDone)
	}()
	select {
	case <-publishDone:
		t.Fatal("target publication passed an in-flight invalidation")
	case <-time.After(20 * time.Millisecond):
	}
	close(old.release)
	close(candidate.release)
	if err := <-invalidationDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("target publication did not resume after invalidation")
	}
}

func TestRuntimeTargetAbortSerializesWithConcurrentInvalidation(t *testing.T) {
	const scope = "sites/7/dependencies"
	old := &blockingTaggedStore{
		Store:   newTaggedStore("old-abort"),
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	candidate := &blockingTaggedStore{
		Store:   newTaggedStore("candidate-abort"),
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	manager := &Manager{coordinator: NewCoordinator()}
	active, err := manager.PrepareRuntimeTargets(map[string][]RuntimeTarget{
		"7": {{Scope: scope, Store: old}},
	})
	if err != nil {
		t.Fatal(err)
	}
	active.Publish()
	update, err := manager.PrepareRuntimeTargets(map[string][]RuntimeTarget{
		"7": {{Scope: scope, Store: candidate}},
	})
	if err != nil {
		t.Fatal(err)
	}

	invalidationDone := make(chan error, 1)
	go func() {
		invalidationDone <- manager.Invalidate(context.Background(), "resource:1")
	}()
	select {
	case <-old.entered:
	case <-candidate.entered:
	case <-time.After(time.Second):
		t.Fatal("invalidation did not reach a staged target")
	}

	abortDone := make(chan struct{})
	go func() {
		update.Abort()
		close(abortDone)
	}()
	select {
	case <-abortDone:
		t.Fatal("target abort passed an in-flight invalidation")
	case <-time.After(20 * time.Millisecond):
	}
	close(old.release)
	close(candidate.release)
	if err := <-invalidationDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-abortDone:
	case <-time.After(time.Second):
		t.Fatal("target abort did not resume after invalidation")
	}
	manager.coordinator.mu.RLock()
	defer manager.coordinator.mu.RUnlock()
	if len(manager.coordinator.runtimeScopes["7"]) != 1 {
		t.Fatalf("aborted candidate target count = %d", len(manager.coordinator.runtimeScopes["7"]))
	}
}

type blockingTaggedStore struct {
	Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingTaggedStore) InvalidateTag(ctx context.Context, tag Tag) error {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Store.InvalidateTag(ctx, tag)
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
