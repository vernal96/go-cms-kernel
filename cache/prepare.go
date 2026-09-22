package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// PreparedSet keeps the dependency versions observed BEFORE the authoritative
// read. Invalidation during that read makes a late cache fill invisible.
type PreparedSet func(context.Context, string, []byte, time.Duration) error
type PreparedStore interface {
	Prepare(context.Context, []Tag) (PreparedSet, error)
}

func Prepare(ctx context.Context, store Store, tags []Tag) PreparedSet {
	if store == nil {
		return nil
	}
	if prepared, ok := store.(PreparedStore); ok {
		set, err := prepared.Prepare(ctx, tags)
		if err != nil {
			report(ctx, store, Event{Type: EventReadError, Error: err})
			return nil
		}
		return set
	}
	if len(tags) > 0 {
		report(ctx, store, Event{Type: EventWriteError, Error: errors.New("cache store does not support prepared dependency writes")})
		return nil
	}
	return func(ctx context.Context, key string, value []byte, ttl time.Duration) error {
		return store.Set(ctx, key, value, SetOptions{TTL: ttl, Tags: tags})
	}
}

func WritePreparedJSON[T any](ctx context.Context, store Store, set PreparedSet, key string, value T, ttl time.Duration) {
	if set == nil {
		return
	}
	raw, err := json.Marshal(value)
	if err == nil {
		err = set(ctx, key, raw, ttl)
	}
	if err != nil {
		report(ctx, store, Event{Type: EventWriteError, Key: key, Error: err})
	}
}

func (s *scopedStore) Prepare(ctx context.Context, tags []Tag) (PreparedSet, error) {
	physical := make([]Tag, len(tags))
	for i, tag := range tags {
		if tag == "" {
			return nil, errors.New("cache tag is empty")
		}
		physical[i] = Tag(scopedValue(s.dependencyNamespace, string(tag)))
	}
	set := Prepare(ctx, s.store, physical)
	if set == nil {
		return nil, nil
	}
	return func(ctx context.Context, key string, value []byte, ttl time.Duration) error {
		if key == "" {
			return errors.New("cache key is empty")
		}
		return set(ctx, scopedValue(s.keyNamespace, key), value, ttl)
	}, nil
}

func (s *observedStore) Prepare(ctx context.Context, tags []Tag) (PreparedSet, error) {
	return Prepare(ctx, s.store, tags), nil
}
