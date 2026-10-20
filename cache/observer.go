package cache

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
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
	LoadLocks
	store         Store
	observer      Observer
	readFailUntil atomic.Int64
}

func observeStore(store Store, observer Observer) Store {
	return &observedStore{store: store, observer: observer}
}

func (s *observedStore) Code() Code { return s.store.Code() }

func (s *observedStore) Ping(ctx context.Context) error {
	return s.store.Ping(ctx)
}

func (s *observedStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := s.readBackoffError(); err != nil {
		return nil, err
	}
	operationCtx, cancel := cacheOperationContext(ctx)
	defer cancel()
	value, err := s.store.Get(operationCtx, key)
	if s.isBackendFailure(ctx, err) {
		s.openReadBackoff()
	}
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
	s.observe(ctx, event)
	return value, err
}

func (s *observedStore) Set(
	ctx context.Context,
	key string,
	value []byte,
	options SetOptions,
) error {
	if err := s.readBackoffError(); err != nil {
		return err
	}
	operationCtx, cancel := cacheOperationContext(ctx)
	defer cancel()
	err := s.store.Set(operationCtx, key, value, options)
	if s.isBackendFailure(ctx, err) {
		s.openReadBackoff()
	}
	if err != nil {
		s.observe(ctx, Event{
			Type: EventWriteError, Store: s.Code(), Key: key, Error: err,
		})
	}
	return err
}

func (s *observedStore) Exists(ctx context.Context, key string) (bool, error) {
	operationCtx, cancel := cacheOperationContext(ctx)
	defer cancel()
	return s.store.Exists(operationCtx, key)
}

func (s *observedStore) Delete(ctx context.Context, key string) error {
	operationCtx, cancel := cacheOperationContext(ctx)
	defer cancel()
	return s.store.Delete(operationCtx, key)
}

func (s *observedStore) InvalidateTag(ctx context.Context, tag Tag) error {
	operationCtx, cancel := cacheOperationContext(ctx)
	defer cancel()
	err := s.store.InvalidateTag(operationCtx, tag)
	if err != nil {
		s.observe(ctx, Event{
			Type: EventInvalidationError, Store: s.Code(), Tag: tag, Error: err,
		})
	}
	return err
}

func (s *observedStore) Close() error { return s.store.Close() }

// Cache operations are optional and must not inherit a slow remote store's
// retry duration on a request path.
const (
	cacheOperationTimeout = 2 * time.Second
	cacheReadBackoff      = 5 * time.Second
)

var errStoreBackoff = errors.New("cache store temporarily unavailable")

func cacheOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, cacheOperationTimeout)
}

func (s *observedStore) isBackendFailure(
	requestCtx context.Context,
	err error,
) bool {
	return err != nil && !errors.Is(err, ErrMiss) &&
		(requestCtx == nil || requestCtx.Err() == nil)
}

func (s *observedStore) openReadBackoff() {
	s.readFailUntil.Store(time.Now().Add(cacheReadBackoff).UnixNano())
}

func (s *observedStore) readBackoffError() error {
	until := s.readFailUntil.Load()
	if until == 0 || time.Now().UnixNano() >= until {
		return nil
	}
	return errStoreBackoff
}

func (s *observedStore) observe(ctx context.Context, event Event) {
	if s.observer == nil {
		return
	}
	event.Store = s.Code()
	s.observer.Observe(ctx, event)
}

type eventReporter interface {
	report(context.Context, Event)
}

func (s *observedStore) report(ctx context.Context, event Event) {
	if s.observer == nil {
		return
	}
	event.Store = s.Code()
	s.observer.Observe(ctx, event)
}

func report(ctx context.Context, store Store, event Event) {
	if reporter, ok := store.(eventReporter); ok {
		reporter.report(ctx, event)
	}
}

var _ Store = (*observedStore)(nil)
