package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

// ReadResult preserves per-key misses and failures in a batched lookup.
type ReadResult struct {
	Value []byte
	Err   error
}

// BatchReader lets remote stores pipeline both entries and dependency tokens.
type BatchReader interface {
	GetMany(context.Context, []string) map[string]ReadResult
}

func GetMany(ctx context.Context, store Store, keys []string) map[string]ReadResult {
	if len(keys) == 0 {
		return map[string]ReadResult{}
	}
	if batch, ok := store.(BatchReader); ok {
		return batch.GetMany(ctx, keys)
	}
	result := make(map[string]ReadResult, len(keys))
	for _, key := range keys {
		if _, exists := result[key]; exists {
			continue
		}
		if store == nil {
			result[key] = ReadResult{Err: ErrMiss}
			continue
		}
		value, err := store.Get(ctx, key)
		result[key] = ReadResult{Value: value, Err: err}
	}
	return result
}

// DecodeJSON rejects trailing data and preserves numbers in untyped fields.
func DecodeJSON[T any](raw []byte) (T, error) {
	var result T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err != nil {
			return result, err
		}
		return result, errors.New("cached JSON contains trailing value")
	}
	return result, nil
}

func ReadJSON[T any](ctx context.Context, store Store, key string) (T, bool) {
	var zero T
	if store == nil {
		return zero, false
	}
	raw, err := store.Get(ctx, key)
	if err != nil {
		return zero, false
	}
	return DecodeCachedJSON[T](ctx, store, key, ReadResult{Value: raw})
}

func DecodeCachedJSON[T any](ctx context.Context, store Store, key string, value ReadResult) (T, bool) {
	var zero T
	if value.Err != nil {
		return zero, false
	}
	result, err := DecodeJSON[T](value.Value)
	if err != nil {
		report(ctx, store, Event{Type: EventDecodeError, Key: key, Error: err})
		_ = store.Delete(ctx, key)
		return zero, false
	}
	return result, true
}

func WriteJSON[T any](ctx context.Context, store Store, key string, value T, options SetOptions) {
	if store == nil {
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		report(ctx, store, Event{Type: EventWriteError, Key: key, Error: err})
		return
	}
	_ = store.Set(ctx, key, raw, options)
}

func (s *scopedStore) GetMany(ctx context.Context, keys []string) map[string]ReadResult {
	physical := make([]string, 0, len(keys))
	for _, key := range keys {
		if key != "" {
			physical = append(physical, scopedValue(s.keyNamespace, key))
		}
	}
	values := GetMany(ctx, s.store, physical)
	result := make(map[string]ReadResult, len(keys))
	for _, key := range keys {
		if key == "" {
			result[key] = ReadResult{Err: errors.New("cache key is empty")}
		} else {
			result[key] = values[scopedValue(s.keyNamespace, key)]
		}
	}
	return result
}

func (s *observedStore) GetMany(ctx context.Context, keys []string) map[string]ReadResult {
	result := GetMany(ctx, s.store, keys)
	for key, item := range result {
		event := Event{Type: EventHit, Store: s.Code(), Key: key, Error: item.Err}
		switch {
		case errors.Is(item.Err, ErrCorrupt):
			event.Type = EventDecodeError
		case errors.Is(item.Err, ErrMiss):
			event.Type = EventMiss
			event.Error = nil
		case item.Err != nil:
			event.Type = EventReadError
		}
		s.observer.Observe(ctx, event)
	}
	return result
}
