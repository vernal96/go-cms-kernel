package cache

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/vernal96/go-cms-kernel/filesystem"
)

type Manager struct {
	stores      map[Code]Store
	order       []Store
	coordinator *Coordinator
	closeOnce   sync.Once
	closeErr    error
	closed      atomic.Bool
}

func NewManager(
	ctx context.Context,
	factories []Factory,
	dependencies Dependencies,
) (_ *Manager, resultErr error) {
	if ctx == nil {
		return nil, errors.New("cache manager context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	manager := &Manager{
		stores:      make(map[Code]Store, len(factories)),
		coordinator: NewCoordinator(),
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, manager.Close())
		}
	}()

	for index, factory := range factories {
		if isNilFactory(factory) {
			return nil, fmt.Errorf(
				"cache factory at index %d is nil",
				index,
			)
		}
		code := factory.Code()
		if code == "" {
			return nil, fmt.Errorf(
				"cache factory at index %d has empty code",
				index,
			)
		}
		if _, exists := manager.stores[code]; exists {
			return nil, fmt.Errorf(
				"cache store %q is configured more than once",
				code,
			)
		}

		store, err := factory.Open(ctx, dependencies)
		if !isNilStore(store) {
			manager.order = append(manager.order, store)
		}
		if err != nil {
			return nil, fmt.Errorf("open cache store %q: %w", code, err)
		}
		if isNilStore(store) {
			return nil, fmt.Errorf(
				"cache factory %q returned nil store",
				code,
			)
		}
		if store.Code() != code {
			return nil, fmt.Errorf(
				"cache factory %q returned store %q",
				code,
				store.Code(),
			)
		}
		if err := store.Ping(ctx); err != nil {
			return nil, fmt.Errorf("ping cache store %q: %w", code, err)
		}
		manager.stores[code] = observeStore(store, dependencies.Observer)
	}

	return manager, nil
}

// Invalidate fans logical dependencies out to all physical targets currently
// participating in runtime cache scopes.
func (m *Manager) Invalidate(ctx context.Context, tags ...Tag) error {
	if m == nil || m.closed.Load() || m.coordinator == nil {
		return nil
	}
	return m.coordinator.Invalidate(ctx, tags...)
}

func (m *Manager) Prune(ctx context.Context) error {
	if m == nil || m.closed.Load() {
		return nil
	}
	var result []error
	for _, store := range m.order {
		if pruner, ok := store.(Pruner); ok {
			if err := pruner.Prune(ctx); errors.Is(err, filesystem.ErrUnsupported) {
				continue
			} else if err != nil {
				result = append(result, fmt.Errorf(
					"prune cache store %q: %w", store.Code(), err,
				))
			}
		}
	}
	return errors.Join(result...)
}

func (m *Manager) Flush(ctx context.Context) error {
	if m == nil || m.closed.Load() {
		return nil
	}
	var result []error
	for _, store := range m.order {
		if flusher, ok := store.(Flusher); ok {
			if err := flusher.Flush(ctx); errors.Is(err, filesystem.ErrUnsupported) {
				continue
			} else if err != nil {
				result = append(result, fmt.Errorf(
					"flush cache store %q: %w", store.Code(), err,
				))
			}
		}
	}
	return errors.Join(result...)
}

func (m *Manager) Store(code Code) (Store, bool) {
	if m == nil || m.closed.Load() {
		return nil, false
	}
	store, exists := m.stores[code]
	return store, exists
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.closed.Store(true)
		var closeErrors []error
		for index := len(m.order) - 1; index >= 0; index-- {
			store := m.order[index]
			if err := store.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf(
					"close cache store %q: %w",
					store.Code(),
					err,
				))
			}
		}
		m.closeErr = errors.Join(closeErrors...)
	})
	return m.closeErr
}

func isNilFactory(factory Factory) bool {
	if factory == nil {
		return true
	}
	return isNilReflectValue(factory)
}

func isNilStore(store Store) bool {
	if store == nil {
		return true
	}
	return isNilReflectValue(store)
}

func isNilReflectValue(value any) bool {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ Resolver = (*Manager)(nil)
var _ Invalidator = (*Manager)(nil)
var _ Pruner = (*Manager)(nil)
var _ Flusher = (*Manager)(nil)
