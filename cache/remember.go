package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

// RememberJSON provides typed, fail-open JSON read-through caching. Cache
// infrastructure and payload errors fall back to the authoritative loader;
// loader errors are returned and never cached.
func RememberJSON[T any](
	ctx context.Context,
	store Store,
	key string,
	options SetOptions,
	loader func(context.Context) (T, error),
) (T, error) {
	return RememberJSONWithOptions(
		ctx,
		store,
		key,
		func(T) SetOptions { return options },
		loader,
	)
}

// RememberJSONWithOptions allows dependency tags to be derived from the
// authoritative result on a miss.
func RememberJSONWithOptions[T any](
	ctx context.Context,
	store Store,
	key string,
	options func(T) SetOptions,
	loader func(context.Context) (T, error),
) (T, error) {
	var zero T
	if store == nil {
		return loader(ctx)
	}

	raw, err := store.Get(ctx, key)
	if err == nil {
		var result T
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		decodeErr := decoder.Decode(&result)
		if decodeErr == nil {
			decodeErr = decoder.Decode(&struct{}{})
			if errors.Is(decodeErr, io.EOF) {
				return result, nil
			}
			if decodeErr == nil {
				decodeErr = errors.New("cached JSON contains trailing value")
			}
		}
		report(ctx, store, Event{
			Type: EventDecodeError, Key: key, Error: decodeErr,
		})
		_ = store.Delete(ctx, key)
	}

	result, err := loader(ctx)
	if err != nil {
		return zero, err
	}
	raw, err = json.Marshal(result)
	if err != nil {
		report(ctx, store, Event{
			Type: EventWriteError, Key: key, Error: err,
		})
		return result, nil
	}
	// Cache is an optimization: an observable write failure must not turn a
	// successful authoritative read into a domain failure.
	setOptions := SetOptions{}
	if options != nil {
		setOptions = options(result)
	}
	_ = store.Set(ctx, key, raw, setOptions)
	return result, nil
}
