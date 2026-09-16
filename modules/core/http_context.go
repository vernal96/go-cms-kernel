package core

import (
	"context"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

type requestContextKey uint8

const (
	siteRuntimeContextKey requestContextKey = iota + 1
	resourceContextKey
)

func WithSiteRuntime(
	ctx context.Context,
	runtime *site.Runtime,
) context.Context {
	return context.WithValue(ctx, siteRuntimeContextKey, runtime)
}

func SiteRuntimeFromContext(ctx context.Context) (*site.Runtime, bool) {
	if ctx == nil {
		return nil, false
	}
	runtime, exists := ctx.Value(siteRuntimeContextKey).(*site.Runtime)
	return runtime, exists && runtime != nil
}

func WithResource(
	ctx context.Context,
	item resource.Resource,
) context.Context {
	return context.WithValue(
		ctx,
		resourceContextKey,
		resource.Clone(item),
	)
}

func ResourceFromContext(ctx context.Context) (resource.Resource, bool) {
	if ctx == nil {
		return resource.Resource{}, false
	}
	item, exists := ctx.Value(resourceContextKey).(resource.Resource)
	if !exists {
		return resource.Resource{}, false
	}
	return resource.Clone(item), true
}
