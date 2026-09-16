package cache

import (
	"context"
	"errors"
	"log/slog"
)

func NewSlogObserver(logger *slog.Logger) Observer {
	if logger == nil {
		return nil
	}
	return ObserverFunc(func(ctx context.Context, event Event) {
		attributes := []any{
			slog.String("event", "cache."+string(event.Type)),
			slog.String("cache.store", string(event.Store)),
		}
		if event.Key != "" {
			attributes = append(attributes, slog.String("cache.key", event.Key))
		}
		if event.Tag != "" {
			attributes = append(
				attributes,
				slog.String("cache.tag", string(event.Tag)),
			)
		}
		if event.Error != nil {
			attributes = append(attributes, slog.Any("error", event.Error))
			logger.WarnContext(ctx, "cache operation failed", attributes...)
			return
		}
		logger.DebugContext(ctx, "cache lookup", attributes...)
	})
}

type observedStore struct {
	store    Store
	observer Observer
}

func observeStore(store Store, observer Observer) Store {
	if observer == nil {
		return store
	}
	return &observedStore{store: store, observer: observer}
}

func (s *observedStore) Code() Code { return s.store.Code() }

func (s *observedStore) Ping(ctx context.Context) error {
	return s.store.Ping(ctx)
}

func (s *observedStore) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := s.store.Get(ctx, key)
	event := Event{Type: EventHit, Store: s.Code(), Key: key, Error: err}
	switch {
	case errors.Is(err, ErrCorrupt):
		event.Type = EventDecodeError
	case errors.Is(err, ErrMiss):
		event.Type = EventMiss
		event.Error = nil
	case err != nil:
		event.Type = EventReadError
	}
	s.observer.Observe(ctx, event)
	return value, err
}

func (s *observedStore) Set(
	ctx context.Context,
	key string,
	value []byte,
	options SetOptions,
) error {
	err := s.store.Set(ctx, key, value, options)
	if err != nil {
		s.observer.Observe(ctx, Event{
			Type: EventWriteError, Store: s.Code(), Key: key, Error: err,
		})
	}
	return err
}

func (s *observedStore) Exists(ctx context.Context, key string) (bool, error) {
	return s.store.Exists(ctx, key)
}

func (s *observedStore) Delete(ctx context.Context, key string) error {
	return s.store.Delete(ctx, key)
}

func (s *observedStore) InvalidateTag(ctx context.Context, tag Tag) error {
	err := s.store.InvalidateTag(ctx, tag)
	if err != nil {
		s.observer.Observe(ctx, Event{
			Type: EventInvalidationError, Store: s.Code(), Tag: tag, Error: err,
		})
	}
	return err
}

func (s *observedStore) Close() error { return s.store.Close() }

type eventReporter interface {
	report(context.Context, Event)
}

func (s *observedStore) report(ctx context.Context, event Event) {
	event.Store = s.Code()
	s.observer.Observe(ctx, event)
}

func report(ctx context.Context, store Store, event Event) {
	if reporter, ok := store.(eventReporter); ok {
		reporter.report(ctx, event)
	}
}

var _ Store = (*observedStore)(nil)
