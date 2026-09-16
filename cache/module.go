package cache

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type scopedManager struct {
	stores   map[Alias]Store
	bindings map[Alias]Binding
}

type RuntimeScope struct {
	Profile string
	Site    string
}

func NewModuleManager(
	resolver Resolver,
	profileCode string,
	moduleCode string,
	bindings []Binding,
) (ModuleManager, error) {
	return NewRuntimeModuleManager(
		resolver,
		RuntimeScope{Profile: profileCode},
		moduleCode,
		bindings,
	)
}

func NewRuntimeModuleManager(
	resolver Resolver,
	scope RuntimeScope,
	moduleCode string,
	bindings []Binding,
) (ModuleManager, error) {
	var coordinator *Coordinator
	if owner, ok := resolver.(*Manager); ok {
		if owner.coordinator == nil {
			owner.coordinator = NewCoordinator()
		}
		coordinator = owner.coordinator
	}
	manager := &scopedManager{
		stores:   make(map[Alias]Store, len(bindings)),
		bindings: make(map[Alias]Binding, len(bindings)),
	}

	for index, binding := range bindings {
		if binding.Alias == "" {
			return nil, fmt.Errorf(
				"cache binding at index %d has empty alias",
				index,
			)
		}
		if binding.Code == "" {
			return nil, fmt.Errorf(
				"cache binding %q has empty store code",
				binding.Alias,
			)
		}
		if _, exists := manager.stores[binding.Alias]; exists {
			return nil, fmt.Errorf(
				"cache alias %q is configured more than once",
				binding.Alias,
			)
		}
		if resolver == nil {
			return nil, fmt.Errorf(
				"resolve cache alias %q: %w: %q",
				binding.Alias,
				ErrStoreNotFound,
				binding.Code,
			)
		}
		store, exists := resolver.Store(binding.Code)
		if !exists {
			return nil, fmt.Errorf(
				"resolve cache alias %q: %w: %q",
				binding.Alias,
				ErrStoreNotFound,
				binding.Code,
			)
		}

		keyNamespace := strings.TrimSpace(binding.Namespace)
		if keyNamespace == "" {
			if scope.Site == "" {
				keyNamespace = fmt.Sprintf(
					"profiles/%s/modules/%s/caches/%s",
					scope.Profile,
					moduleCode,
					binding.Alias,
				)
			} else {
				keyNamespace = fmt.Sprintf(
					"sites/%s/profiles/%s/modules/%s/caches/%s",
					scope.Site,
					scope.Profile,
					moduleCode,
					binding.Alias,
				)
			}
		}
		if err := validateNamespace(keyNamespace); err != nil {
			return nil, fmt.Errorf(
				"cache alias %q namespace: %w",
				binding.Alias,
				err,
			)
		}
		dependencyNamespace := dependencyScope(scope)
		if err := validateNamespace(dependencyNamespace); err != nil {
			return nil, fmt.Errorf(
				"cache alias %q dependency namespace: %w",
				binding.Alias,
				err,
			)
		}

		binding.Namespace = keyNamespace
		manager.bindings[binding.Alias] = binding
		manager.stores[binding.Alias] = &scopedStore{
			store:               store,
			keyNamespace:        keyNamespace,
			dependencyNamespace: dependencyNamespace,
			coordinator:         coordinator,
		}
		if coordinator != nil {
			coordinator.register(dependencyNamespace, store)
		}
	}

	return manager, nil
}

func (m *scopedManager) Store(alias Alias) (Store, bool) {
	if m == nil {
		return nil, false
	}
	store, exists := m.stores[alias]
	return store, exists
}

func (m *scopedManager) Binding(alias Alias) (Binding, bool) {
	if m == nil {
		return Binding{}, false
	}
	binding, exists := m.bindings[alias]
	return binding, exists
}

type scopedStore struct {
	store               Store
	keyNamespace        string
	dependencyNamespace string
	coordinator         *Coordinator
}

func (s *scopedStore) Code() Code {
	return s.store.Code()
}

func (s *scopedStore) Ping(ctx context.Context) error {
	return s.store.Ping(ctx)
}

func (s *scopedStore) Get(
	ctx context.Context,
	key string,
) ([]byte, error) {
	if key == "" {
		return nil, errors.New("cache key is empty")
	}
	return s.store.Get(ctx, scopedValue(s.keyNamespace, key))
}

func (s *scopedStore) Set(
	ctx context.Context,
	key string,
	value []byte,
	options SetOptions,
) error {
	if key == "" {
		return errors.New("cache key is empty")
	}
	tags := make([]Tag, len(options.Tags))
	for index, tag := range options.Tags {
		if tag == "" {
			return errors.New("cache tag is empty")
		}
		tags[index] = Tag(scopedValue(s.dependencyNamespace, string(tag)))
	}
	options.Tags = tags
	return s.store.Set(
		ctx,
		scopedValue(s.keyNamespace, key),
		value,
		options,
	)
}

func (s *scopedStore) Exists(
	ctx context.Context,
	key string,
) (bool, error) {
	if key == "" {
		return false, errors.New("cache key is empty")
	}
	return s.store.Exists(ctx, scopedValue(s.keyNamespace, key))
}

func (s *scopedStore) Delete(
	ctx context.Context,
	key string,
) error {
	if key == "" {
		return errors.New("cache key is empty")
	}
	return s.store.Delete(ctx, scopedValue(s.keyNamespace, key))
}

func (s *scopedStore) InvalidateTag(
	ctx context.Context,
	tag Tag,
) error {
	if tag == "" {
		return errors.New("cache tag is empty")
	}
	if s.coordinator != nil {
		return s.coordinator.invalidateScope(
			ctx,
			s.dependencyNamespace,
			tag,
		)
	}
	return s.store.InvalidateTag(ctx, Tag(scopedValue(
		s.dependencyNamespace,
		string(tag),
	)))
}

// A module-scoped store borrows the global store. Module code must not be
// able to close application-owned infrastructure.
func (*scopedStore) Close() error {
	return nil
}

func validateNamespace(value string) error {
	if strings.ContainsRune(value, '\x00') {
		return errors.New("contains NUL")
	}
	return nil
}

func scopedValue(namespace, value string) string {
	return strconv.Itoa(len(namespace)) + ":" + namespace + value
}

func dependencyScope(scope RuntimeScope) string {
	if scope.Site != "" {
		return fmt.Sprintf("sites/%s/dependencies", scope.Site)
	}
	return fmt.Sprintf("profiles/%s/dependencies", scope.Profile)
}

func (s *scopedStore) report(ctx context.Context, event Event) {
	report(ctx, s.store, event)
}

var _ ModuleManager = (*scopedManager)(nil)
var _ Store = (*scopedStore)(nil)
