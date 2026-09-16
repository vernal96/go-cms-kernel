package entityhooks

import (
	"context"
	"errors"

	"github.com/vernal96/go-cms-kernel/security"
)

// Invocation carries only operation-lifetime metadata, never a transaction.
// It is propagated to persistence using context so nested domain operations
// participate in the same invocation. It must not be retained by a service.
type Invocation struct {
	Operation string
	Actor     security.Actor
	Registry  *Registry
}
type invocationKey struct{}

func Begin(ctx context.Context, registry *Registry, actor security.Actor, operation string) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("entity mutation context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if current := InvocationFrom(ctx); current != nil {
		return ctx, func() {}, nil
	}
	release, err := registry.Acquire()
	if err != nil {
		return nil, nil, err
	}
	return context.WithValue(ctx, invocationKey{}, &Invocation{Operation: operation, Actor: actor, Registry: registry}), release, nil
}

func InvocationFrom(ctx context.Context) *Invocation {
	if ctx == nil {
		return nil
	}
	invocation, _ := ctx.Value(invocationKey{}).(*Invocation)
	return invocation
}

func EmptyRegistry(scope Scope, id string) *Registry {
	r := NewRegistry(scope, id)
	_ = r.Seal()
	return r
}
