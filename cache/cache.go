package cache

import (
	"context"
	"errors"
	"time"

	"github.com/vernal96/go-cms-kernel/filesystem"
)

type Code string
type Alias string
type Tag string

type EventType string

const (
	EventHit               EventType = "hit"
	EventMiss              EventType = "miss"
	EventReadError         EventType = "read_error"
	EventDecodeError       EventType = "decode_error"
	EventWriteError        EventType = "write_error"
	EventInvalidationError EventType = "invalidation_error"
)

type Event struct {
	Type  EventType
	Store Code
	Key   string
	Tag   Tag
	Error error
}

type Observer interface {
	Observe(context.Context, Event)
}

type ObserverFunc func(context.Context, Event)

func (f ObserverFunc) Observe(ctx context.Context, event Event) {
	if f != nil {
		f(ctx, event)
	}
}

var (
	ErrMiss          = errors.New("cache entry not found")
	ErrCorrupt       = errors.New("cache entry is corrupt")
	ErrStoreNotFound = errors.New("cache store not found")
	ErrInvalidTTL    = errors.New("cache TTL is invalid")
)

type SetOptions struct {
	TTL  time.Duration
	Tags []Tag
}

type Store interface {
	Code() Code
	Ping(context.Context) error
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, SetOptions) error
	Exists(context.Context, string) (bool, error)
	Delete(context.Context, string) error
	InvalidateTag(context.Context, Tag) error
	Close() error
}

type Resolver interface {
	Store(Code) (Store, bool)
}

type Invalidator interface {
	Invalidate(context.Context, ...Tag) error
}

type InvalidatorFunc func(context.Context, ...Tag) error

func (f InvalidatorFunc) Invalidate(ctx context.Context, tags ...Tag) error {
	if f == nil {
		return nil
	}
	return f(ctx, tags...)
}

type Pruner interface {
	Prune(context.Context) error
}

type Flusher interface {
	Flush(context.Context) error
}

type FilesystemResolver interface {
	Disk(filesystem.Code) (filesystem.Disk, bool)
}

type Dependencies struct {
	Filesystems FilesystemResolver
	Observer    Observer
}

type Factory interface {
	Code() Code
	Open(context.Context, Dependencies) (Store, error)
}

type Binding struct {
	Alias     Alias
	Code      Code
	Namespace string
}

type ModuleManager interface {
	Store(Alias) (Store, bool)
	Binding(Alias) (Binding, bool)
}
