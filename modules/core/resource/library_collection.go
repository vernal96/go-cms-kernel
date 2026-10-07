package resource

import (
	"context"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

// LibraryCollection keeps the public mount separate from the owning library.
// Neither the stored item identity nor its rendering runtime is transferred.
type LibraryCollection struct {
	Mount   Resource
	Source  Resource
	Runtime *site.Runtime
}

func (c LibraryCollection) URL(item LibraryItem) (string, error) {
	library := Clone(c.Source)
	library.Path = c.Mount.Path
	return EffectiveLibraryItemURL(library, item)
}

func Published(item Resource, now time.Time) bool {
	return item.DeletedAt == nil && item.IsPublic &&
		(item.PublishedAt == nil || !now.Before(*item.PublishedAt)) &&
		(item.UnpublishedAt == nil || now.Before(*item.UnpublishedAt))
}

func (s *LibraryService) Collection(ctx context.Context, actor security.Actor, siteID site.ID, id ID, publicOnly bool) (LibraryCollection, error) {
	if err := validateContext(ctx, "library collection"); err != nil {
		return LibraryCollection{}, err
	}
	if err := s.common.authorizer.Check(ctx, actor, readPermission); err != nil {
		return LibraryCollection{}, err
	}
	mount, err := s.common.repository.ByID(ctx, id)
	if err != nil {
		return LibraryCollection{}, err
	}
	if mount.SiteID != siteID || mount.DeletedAt != nil {
		return LibraryCollection{}, ErrNotFound
	}
	source := mount
	switch mount.Type {
	case resourcetype.Library:
	case resourcetype.LibraryMirror:
		source, err = s.common.repository.ByID(ctx, ID(resourcetype.SourceLibraryID(mount.TypeSettings)))
		if err != nil {
			return LibraryCollection{}, err
		}
	default:
		return LibraryCollection{}, ErrInvalidReference
	}
	if source.Type != resourcetype.Library || source.DeletedAt != nil {
		return LibraryCollection{}, ErrNotFound
	}
	runtime, exists := s.common.runtime(ctx, source.SiteID)
	if !exists {
		return LibraryCollection{}, ErrNotFound
	}
	if publicOnly || mount.Type == resourcetype.LibraryMirror {
		now := time.Now().UTC()
		mountRuntime, available := s.common.runtime(ctx, mount.SiteID)
		if current, ok := s.common.sites.(interface {
			CheckCurrent(context.Context, *site.Runtime) error
		}); ok {
			if err := current.CheckCurrent(ctx, runtime); err != nil {
				return LibraryCollection{}, err
			}
			if available && mountRuntime != runtime {
				if err := current.CheckCurrent(ctx, mountRuntime); err != nil {
					return LibraryCollection{}, err
				}
			}
		}
		if !available || !runtime.Site().IsPublic || !Published(source, now) || (publicOnly && (!mountRuntime.Site().IsPublic || !Published(mount, now))) {
			return LibraryCollection{}, ErrNotFound
		}
	}
	return LibraryCollection{Mount: mount, Source: source, Runtime: runtime}, nil
}

func (s *LibraryService) QueryCollection(ctx context.Context, actor security.Actor, query LibraryItemQuery) (LibraryItemPage, LibraryCollection, error) {
	collection, err := s.Collection(ctx, actor, query.SiteID, query.LibraryID, query.PublicOnly)
	if err != nil {
		return LibraryItemPage{}, LibraryCollection{}, err
	}
	query.SiteID, query.LibraryID = collection.Source.SiteID, collection.Source.ID
	if collection.Mount.Type == resourcetype.LibraryMirror {
		query.PublicOnly = true
	}
	page, err := s.Query(ctx, actor, query)
	return page, collection, err
}

type ResolvedLibraryItem struct {
	Item       LibraryItem
	Collection LibraryCollection
}

func (s *LibraryService) ResolvePublished(ctx context.Context, actor security.Actor, siteID site.ID, path string) (ResolvedLibraryItem, error) {
	if err := validateContext(ctx, "resolve library item"); err != nil {
		return ResolvedLibraryItem{}, err
	}
	if err := s.common.authorizer.Check(ctx, actor, readPermission); err != nil {
		return ResolvedLibraryItem{}, err
	}
	item, mount, err := s.repository.ResolveLibraryItemRoute(ctx, siteID, path)
	if err != nil {
		return ResolvedLibraryItem{}, err
	}
	collection, err := s.Collection(ctx, actor, siteID, mount.ID, true)
	if err != nil {
		return ResolvedLibraryItem{}, err
	}
	if item.LibraryID != collection.Source.ID || item.SiteID != collection.Source.SiteID || !Published(Resource{DeletedAt: item.DeletedAt, IsPublic: item.IsPublic, PublishedAt: item.PublishedAt, UnpublishedAt: item.UnpublishedAt}, time.Now().UTC()) {
		return ResolvedLibraryItem{}, ErrNotFound
	}
	return ResolvedLibraryItem{Item: item, Collection: collection}, nil
}
