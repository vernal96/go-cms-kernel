package cache

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type testFactory struct {
	code  Code
	store Store
	err   error
}

func (f testFactory) Code() Code {
	return f.code
}

func (f testFactory) Open(
	context.Context,
	Dependencies,
) (Store, error) {
	return f.store, f.err
}

type testStore struct {
	code       Code
	values     map[string][]byte
	pingErr    error
	closeErr   error
	closeOrder *[]Code
}

func (s *testStore) Code() Code {
	return s.code
}

func (s *testStore) Ping(context.Context) error {
	return s.pingErr
}

func (s *testStore) Get(
	_ context.Context,
	key string,
) ([]byte, error) {
	value, exists := s.values[key]
	if !exists {
		return nil, ErrMiss
	}
	return append([]byte(nil), value...), nil
}

func (s *testStore) Set(
	_ context.Context,
	key string,
	value []byte,
	options SetOptions,
) error {
	if options.TTL < 0 {
		return ErrInvalidTTL
	}
	if s.values == nil {
		s.values = make(map[string][]byte)
	}
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func (s *testStore) Exists(
	ctx context.Context,
	key string,
) (bool, error) {
	_, err := s.Get(ctx, key)
	return err == nil, nil
}

func (s *testStore) Delete(
	_ context.Context,
	key string,
) error {
	delete(s.values, key)
	return nil
}

func (*testStore) InvalidateTag(context.Context, Tag) error {
	return nil
}

func (s *testStore) Close() error {
	if s.closeOrder != nil {
		*s.closeOrder = append(*s.closeOrder, s.code)
	}
	return s.closeErr
}

func TestManagerLifecycleAndReverseClose(t *testing.T) {
	var order []Code
	first := &testStore{code: "first", closeOrder: &order}
	second := &testStore{code: "second", closeOrder: &order}

	manager, err := NewManager(
		context.Background(),
		[]Factory{
			testFactory{code: first.code, store: first},
			testFactory{code: second.code, store: second},
		},
		Dependencies{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if store, exists := manager.Store("second"); !exists || store != second {
		t.Fatal("second cache store was not resolved")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []Code{"second", "first"}) {
		t.Fatalf("close order = %v", order)
	}
	if _, exists := manager.Store("first"); exists {
		t.Fatal("closed manager still resolves stores")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 {
		t.Fatalf("stores closed more than once: %v", order)
	}
}

func TestManagerRejectsDuplicateAndClosesOpenedStore(t *testing.T) {
	var order []Code
	first := &testStore{code: "shared", closeOrder: &order}
	_, err := NewManager(
		context.Background(),
		[]Factory{
			testFactory{code: "shared", store: first},
			testFactory{
				code:  "shared",
				store: &testStore{code: "shared"},
			},
		},
		Dependencies{},
	)
	if err == nil {
		t.Fatal("duplicate cache store was accepted")
	}
	if !reflect.DeepEqual(order, []Code{"shared"}) {
		t.Fatalf("partially opened stores not closed: %v", order)
	}
}

func TestManagerStrictPingClosesStore(t *testing.T) {
	pingErr := errors.New("redis unavailable")
	store := &testStore{code: "redis", pingErr: pingErr}
	_, err := NewManager(
		context.Background(),
		[]Factory{
			testFactory{code: store.code, store: store},
		},
		Dependencies{},
	)
	if !errors.Is(err, pingErr) {
		t.Fatalf("error = %v", err)
	}
}

func TestModuleManagerScopesAliasesAndAllowsSharedNamespace(t *testing.T) {
	global := &testStore{code: "global", values: make(map[string][]byte)}
	manager := &Manager{
		stores: map[Code]Store{"global": global},
	}
	first, err := NewModuleManager(
		manager,
		"profile-a",
		"module-a",
		[]Binding{{Alias: "default", Code: "global"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewModuleManager(
		manager,
		"profile-b",
		"module-a",
		[]Binding{{Alias: "default", Code: "global"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	firstStore, _ := first.Store("default")
	secondStore, _ := second.Store("default")
	if err := firstStore.Set(
		context.Background(),
		"key",
		[]byte("first"),
		SetOptions{TTL: time.Minute},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := secondStore.Get(context.Background(), "key"); !errors.Is(
		err,
		ErrMiss,
	) {
		t.Fatalf("automatic namespaces are not isolated: %v", err)
	}

	sharedA, err := NewModuleManager(
		manager,
		"profile-a",
		"module-a",
		[]Binding{{
			Alias:     "shared",
			Code:      "global",
			Namespace: "project/shared",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	sharedB, err := NewModuleManager(
		manager,
		"profile-b",
		"module-b",
		[]Binding{{
			Alias:     "other",
			Code:      "global",
			Namespace: "project/shared",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	sharedStoreA, _ := sharedA.Store("shared")
	sharedStoreB, _ := sharedB.Store("other")
	if err := sharedStoreA.Set(
		context.Background(),
		"key",
		[]byte("shared"),
		SetOptions{},
	); err != nil {
		t.Fatal(err)
	}
	value, err := sharedStoreB.Get(context.Background(), "key")
	if err != nil || string(value) != "shared" {
		t.Fatalf("shared value = %q, %v", value, err)
	}
}

func TestRuntimeModuleManagerIsolatesDefaultNamespaceBySite(t *testing.T) {
	global := &testStore{code: "global", values: make(map[string][]byte)}
	manager := &Manager{stores: map[Code]Store{"global": global}}
	first, err := NewRuntimeModuleManager(
		manager,
		RuntimeScope{Profile: "shared", Site: "1"},
		"module",
		[]Binding{{Alias: "runtime", Code: "global"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRuntimeModuleManager(
		manager,
		RuntimeScope{Profile: "shared", Site: "2"},
		"module",
		[]Binding{{Alias: "runtime", Code: "global"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	firstStore, _ := first.Store("runtime")
	secondStore, _ := second.Store("runtime")
	if err := firstStore.Set(
		context.Background(),
		"key",
		[]byte("site-one"),
		SetOptions{TTL: time.Minute},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := secondStore.Get(context.Background(), "key"); !errors.Is(err, ErrMiss) {
		t.Fatalf("site-scoped cache namespace leaked: %v", err)
	}
}

func TestModuleManagerRejectsUnknownStoreAndDuplicateAlias(t *testing.T) {
	manager := &Manager{stores: map[Code]Store{}}
	if _, err := NewModuleManager(
		manager,
		"profile",
		"module",
		[]Binding{{Alias: "cache", Code: "missing"}},
	); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("missing store error = %v", err)
	}

	manager.stores["present"] = &testStore{code: "present"}
	if _, err := NewModuleManager(
		manager,
		"profile",
		"module",
		[]Binding{
			{Alias: "cache", Code: "present"},
			{Alias: "cache", Code: "present"},
		},
	); err == nil {
		t.Fatal("duplicate alias was accepted")
	}
}
