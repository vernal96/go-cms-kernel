package cache

import (
	"context"
	"sync"
)

// LoadLocks serializes cold fills for a key. It is owned by a physical store,
// never a global registry; idle keys are removed immediately.
type LoadLocks struct {
	mu   sync.Mutex
	keys map[string]*loadLock
}
type loadLock struct {
	token chan struct{}
	refs  int
}
type LoadLocker interface {
	LockLoad(context.Context, string) (func(), error)
}

func (g *LoadLocks) LockLoad(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.keys == nil {
		g.keys = make(map[string]*loadLock)
	}
	entry := g.keys[key]
	if entry == nil {
		entry = &loadLock{token: make(chan struct{}, 1)}
		g.keys[key] = entry
	}
	entry.refs++
	g.mu.Unlock()
	unref := func() {
		g.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(g.keys, key)
		}
		g.mu.Unlock()
	}
	select {
	case entry.token <- struct{}{}:
		return func() { <-entry.token; unref() }, nil
	case <-ctx.Done():
		unref()
		return nil, ctx.Err()
	}
}
func (s *scopedStore) LockLoad(ctx context.Context, key string) (func(), error) {
	if locks, ok := s.store.(LoadLocker); ok {
		return locks.LockLoad(ctx, scopedValue(s.keyNamespace, key))
	}
	return func() {}, nil
}

// LockLoad coordinates manual read-through paths as well as RememberJSON.
func LockLoad(ctx context.Context, store Store, key string) (func(), error) {
	if locks, ok := store.(LoadLocker); ok {
		return locks.LockLoad(ctx, key)
	}
	return func() {}, ctx.Err()
}
