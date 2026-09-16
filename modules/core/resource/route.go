package resource

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type PreviewPolicy interface {
	AllowPreview(
		context.Context,
		security.Actor,
		*site.Runtime,
		Resource,
	) error
}

type ResolveRouteOptions struct {
	Preview bool
}

type RouteResolverOption func(*RouteResolver) error

type RouteResolver struct {
	repository    Repository
	authorizer    security.Authorizer
	previewPolicy PreviewPolicy
	now           func() time.Time
}

func NewRouteResolver(
	repository Repository,
	authorizer security.Authorizer,
	options ...RouteResolverOption,
) (*RouteResolver, error) {
	if repository == nil {
		return nil, errors.New("resource route repository is nil")
	}
	if authorizer == nil {
		return nil, errors.New("resource route authorizer is nil")
	}

	resolver := &RouteResolver{
		repository: repository,
		authorizer: authorizer,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf(
				"resource route option at index %d is nil",
				index,
			)
		}
		if err := option(resolver); err != nil {
			return nil, fmt.Errorf(
				"apply resource route option at index %d: %w",
				index,
				err,
			)
		}
	}
	return resolver, nil
}

func WithPreviewPolicy(policy PreviewPolicy) RouteResolverOption {
	return func(resolver *RouteResolver) error {
		if policy == nil {
			return errors.New("resource preview policy is nil")
		}
		resolver.previewPolicy = policy
		return nil
	}
}

func WithRouteClock(now func() time.Time) RouteResolverOption {
	return func(resolver *RouteResolver) error {
		if now == nil {
			return errors.New("resource route clock is nil")
		}
		resolver.now = now
		return nil
	}
}

func (r *RouteResolver) ResolvePublishedByPath(
	ctx context.Context,
	actor security.Actor,
	siteRuntime *site.Runtime,
	path string,
	options ResolveRouteOptions,
) (Resource, error) {
	if err := validateContext(ctx, "resolve resource route"); err != nil {
		return Resource{}, err
	}
	if siteRuntime == nil {
		return Resource{}, errors.New("resource route site runtime is nil")
	}

	normalizedPath, err := NormalizeLookupPath(path)
	if err != nil {
		return Resource{}, ErrNotFound
	}
	if err := r.authorizer.Check(ctx, actor, readPermission); err != nil {
		return Resource{}, err
	}

	item, err := r.repository.ByPath(
		ctx,
		siteRuntime.Site().ID,
		normalizedPath,
	)
	if err != nil {
		return Resource{}, fmt.Errorf(
			"resolve resource route %q: %w",
			normalizedPath,
			err,
		)
	}
	if item.Path == nil || *item.Path != normalizedPath {
		return Resource{}, ErrNotFound
	}

	return r.resolveStored(ctx, actor, siteRuntime, item, options)
}

func (r *RouteResolver) ResolvePublishedByID(
	ctx context.Context,
	actor security.Actor,
	siteRuntime *site.Runtime,
	id ID,
	options ResolveRouteOptions,
) (Resource, error) {
	if err := validateContext(ctx, "resolve resource route target"); err != nil {
		return Resource{}, err
	}
	if siteRuntime == nil {
		return Resource{}, errors.New("resource route site runtime is nil")
	}
	if id <= 0 {
		return Resource{}, ErrNotFound
	}
	if err := r.authorizer.Check(ctx, actor, readPermission); err != nil {
		return Resource{}, err
	}

	item, err := r.repository.ByID(ctx, id)
	if err != nil {
		return Resource{}, fmt.Errorf(
			"resolve resource route target %d: %w",
			id,
			err,
		)
	}
	return r.resolveStored(ctx, actor, siteRuntime, item, options)
}

func (r *RouteResolver) resolveStored(
	ctx context.Context,
	actor security.Actor,
	siteRuntime *site.Runtime,
	item Resource,
	options ResolveRouteOptions,
) (Resource, error) {
	siteItem := siteRuntime.Site()
	if item.ID <= 0 || item.SiteID != siteItem.ID || item.Path == nil || item.DeletedAt != nil {
		return Resource{}, ErrNotFound
	}
	if _, err := NormalizeLookupPath(*item.Path); err != nil {
		return Resource{}, ErrNotFound
	}

	resourceType, exists := siteRuntime.Profile().
		Registry().
		ResourceType(item.Type)
	if !exists || resourceType.PathMode() != resourcetype.PathRoute {
		return Resource{}, ErrNotFound
	}

	now := r.now().UTC()
	published := item.IsPublic &&
		(item.PublishedAt == nil || !now.Before(item.PublishedAt.UTC())) &&
		(item.UnpublishedAt == nil || now.Before(item.UnpublishedAt.UTC()))
	if !published {
		if !options.Preview || r.previewPolicy == nil {
			return Resource{}, ErrNotFound
		}
		if err := r.previewPolicy.AllowPreview(
			ctx,
			actor,
			siteRuntime,
			Clone(item),
		); err != nil {
			return Resource{}, err
		}
	}

	return Clone(item), nil
}

func NormalizeLookupPath(path string) (string, error) {
	if path == "/" {
		return path, nil
	}
	if !strings.HasPrefix(path, "/") ||
		strings.HasSuffix(path, "/") ||
		strings.Contains(path, "//") {
		return "", fmt.Errorf("resource path %q is invalid", path)
	}

	for _, segment := range strings.Split(
		strings.TrimPrefix(path, "/"),
		"/",
	) {
		if !slugPattern.MatchString(segment) {
			return "", fmt.Errorf("resource path %q is invalid", path)
		}
	}
	return path, nil
}
