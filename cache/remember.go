package cache

import (
	"context"
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
		options.Tags,
		func(T) SetOptions { return options },
		loader,
	)
}

// RememberJSONWithOptions captures dependencies before loading. The options
// callback controls only TTL; dependencies must be declared before the read.
func RememberJSONWithOptions[T any](
	ctx context.Context,
	store Store,
	key string,
	tags []Tag,
	options func(T) SetOptions,
	loader func(context.Context) (T, error),
) (T, error) {
	var zero T
	if store == nil {
		return loader(ctx)
	}

	if result, hit := ReadJSON[T](ctx, store, key); hit {
		return result, nil
	}

	if locks, ok := store.(LoadLocker); ok {
		release, err := locks.LockLoad(ctx, key)
		if err != nil {
			return zero, err
		}
		defer release()
	}
	if result, hit := ReadJSON[T](ctx, store, key); hit {
		return result, nil
	}

	prepared := Prepare(ctx, store, tags)
	result, err := loader(ctx)
	if err != nil {
		return zero, err
	}
	setOptions := SetOptions{}
	if options != nil {
		setOptions = options(result)
	}
	WritePreparedJSON(ctx, store, prepared, key, result, setOptions.TTL)
	return result, nil
}
