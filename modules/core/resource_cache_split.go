package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

type cachedWidgetRef struct {
	ID       widget.BindingID
	Area     widget.AreaCode
	Position int
	Digest   string
}

type cachedResourceRecord struct {
	Resource resource.Resource
	Widgets  []cachedWidgetRef
}

type cachedLibraryItemRecord struct {
	Item    resource.LibraryItem
	Widgets []cachedWidgetRef
}

func resourceCacheKey(id resource.ID) string    { return fmt.Sprintf("resource:id:v3:%d", id) }
func libraryItemCacheKey(id resource.ID) string { return fmt.Sprintf("library-item:id:v1:%d", id) }
func widgetConfigKey(siteID site.ID, owner resource.ID, ref cachedWidgetRef) string {
	return fmt.Sprintf("widget:config:v1:%d:%d:%d:%s", siteID, owner, ref.ID, ref.Digest)
}
func routeCacheKey(siteID site.ID, path string) string {
	sum := sha256.Sum256([]byte(path))
	return fmt.Sprintf("route:v1:%d:%x", siteID, sum)
}

func widgetConfig(binding widget.Binding) (widget.Binding, cachedWidgetRef, error) {
	ref := cachedWidgetRef{ID: binding.ID, Area: binding.Area, Position: binding.Position}
	binding.Area = ""
	binding.Position = 0
	raw, err := json.Marshal(binding)
	if err != nil {
		return widget.Binding{}, ref, err
	}
	digest := sha256.Sum256(raw)
	ref.Digest = hex.EncodeToString(digest[:])
	return binding, ref, nil
}

// Config keys are immutable/content-addressed. A layout can never silently
// consume a newer widget configuration, even across concurrent cache fills.
func (r *cachedResourceRepository) storeWidgets(ctx context.Context, siteID site.ID, owner resource.ID, bindings []widget.Binding) ([]cachedWidgetRef, error) {
	refs := make([]cachedWidgetRef, len(bindings))
	for index, binding := range bindings {
		config, ref, err := widgetConfig(binding)
		if err != nil {
			return nil, err
		}
		refs[index] = ref
		cache.WriteJSON(ctx, r.store, widgetConfigKey(siteID, owner, ref), config, cache.SetOptions{TTL: r.ttl})
	}
	return refs, nil
}

func (r *cachedResourceRepository) cacheReads() (resource.CacheReadRepository, error) {
	repository, ok := r.base.(resource.CacheReadRepository)
	if !ok {
		return nil, errors.New("resource cache read repository is unavailable")
	}
	return repository, nil
}

func (r *cachedResourceRepository) loadWidgets(ctx context.Context, siteID site.ID, owner resource.ID, refs []cachedWidgetRef) ([]widget.Binding, bool, error) {
	keys := make([]string, len(refs))
	for index, ref := range refs {
		keys[index] = widgetConfigKey(siteID, owner, ref)
	}
	values := cache.GetMany(ctx, r.store, keys)
	bindings := make(map[widget.BindingID]widget.Binding, len(refs))
	missing := make([]widget.BindingID, 0)
	for index, ref := range refs {
		value := values[keys[index]]
		binding, decoded := cache.DecodeCachedJSON[widget.Binding](ctx, r.store, keys[index], value)
		_, actual, configErr := widgetConfig(binding)
		if !decoded || configErr != nil || actual.ID != ref.ID || actual.Digest != ref.Digest {
			missing = append(missing, ref.ID)
			continue
		}
		bindings[ref.ID] = binding
	}
	if len(missing) > 0 {
		repository, err := r.cacheReads()
		if err != nil {
			return nil, false, err
		}
		loaded, err := repository.WidgetsByID(ctx, owner, missing)
		if err != nil {
			return nil, false, err
		}
		for _, binding := range loaded {
			config, ref, err := widgetConfig(binding)
			if err != nil {
				return nil, false, err
			}
			bindings[binding.ID] = config
			cache.WriteJSON(ctx, r.store, widgetConfigKey(siteID, owner, ref), config, cache.SetOptions{TTL: r.ttl})
		}
	}
	result := make([]widget.Binding, len(refs))
	for index, ref := range refs {
		binding, exists := bindings[ref.ID]
		_, actual, err := widgetConfig(binding)
		if !exists || err != nil || actual.Digest != ref.Digest {
			return nil, false, nil
		}
		binding.Area = ref.Area
		binding.Position = ref.Position
		result[index] = binding
	}
	return result, true, nil
}

func (r *cachedResourceRepository) readResource(ctx context.Context, id resource.ID) (resource.Resource, error) {
	key := resourceCacheKey(id)
	if record, hit := cache.ReadJSON[cachedResourceRecord](ctx, r.store, key); hit && record.Resource.ID == id {
		bindings, consistent, err := r.loadWidgets(ctx, record.Resource.SiteID, id, record.Widgets)
		if err != nil {
			return resource.Resource{}, err
		}
		if consistent {
			record.Resource.Widgets = bindings
			return record.Resource, nil
		}
	}
	prepared := cache.Prepare(ctx, r.store, []cache.Tag{siteTag(r.siteID), siteResourceTreeTag(r.siteID), resourceTag(id)})
	item, err := r.base.ByID(ctx, id)
	if err != nil {
		return resource.Resource{}, err
	}
	// ByID is also used by management reads across runtime scopes. Capture
	// the actual owner's dependencies before repeating an authoritative load.
	if item.SiteID != r.siteID {
		owner := item.SiteID
		prepared = cache.Prepare(ctx, r.store, []cache.Tag{siteTag(owner), siteResourceTreeTag(owner), resourceTag(id)})
		item, err = r.base.ByID(ctx, id)
		if err != nil {
			return resource.Resource{}, err
		}
		if item.SiteID != owner {
			return item, nil
		}
	}
	refs, err := r.storeWidgets(ctx, item.SiteID, id, item.Widgets)
	if err != nil {
		return resource.Resource{}, err
	}
	record := cachedResourceRecord{Resource: item, Widgets: refs}
	record.Resource.Widgets = nil
	cache.WritePreparedJSON(ctx, r.store, prepared, key, record, r.ttl)
	return item, nil
}

func (r *cachedResourceRepository) readLibraryItem(ctx context.Context, id resource.ID) (resource.LibraryItem, error) {
	key := libraryItemCacheKey(id)
	if record, hit := cache.ReadJSON[cachedLibraryItemRecord](ctx, r.store, key); hit && record.Item.ID == id {
		bindings, consistent, err := r.loadWidgets(ctx, record.Item.SiteID, id, record.Widgets)
		if err != nil {
			return resource.LibraryItem{}, err
		}
		if consistent {
			record.Item.Widgets = bindings
			return record.Item, nil
		}
	}
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItem{}, err
	}
	prepared := cache.Prepare(ctx, r.store, []cache.Tag{siteTag(r.siteID), siteResourceTreeTag(r.siteID), resourceTag(id)})
	item, err := repository.LibraryItemByID(ctx, id)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if item.SiteID != r.siteID {
		owner := item.SiteID
		prepared = cache.Prepare(ctx, r.store, []cache.Tag{siteTag(owner), siteResourceTreeTag(owner), resourceTag(id)})
		item, err = repository.LibraryItemByID(ctx, id)
		if err != nil {
			return resource.LibraryItem{}, err
		}
		if item.SiteID != owner {
			return item, nil
		}
	}
	refs, err := r.storeWidgets(ctx, item.SiteID, id, item.Widgets)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	record := cachedLibraryItemRecord{Item: item, Widgets: refs}
	record.Item.Widgets = nil
	cache.WritePreparedJSON(ctx, r.store, prepared, key, record, r.ttl)
	return item, nil
}

func (r *cachedResourceRepository) lookupRoute(ctx context.Context, siteID site.ID, path string) (resource.RouteTarget, error) {
	if _, err := resource.NormalizeLookupPath(path); err != nil {
		return resource.RouteTarget{}, resource.ErrNotFound
	}
	key := routeCacheKey(siteID, path)
	if target, hit := cache.ReadJSON[resource.RouteTarget](ctx, r.store, key); hit {
		return target, nil
	}
	prepared := cache.Prepare(ctx, r.store, []cache.Tag{siteTag(siteID), siteRoutesTag(siteID)})
	repository, err := r.cacheReads()
	if err != nil {
		return resource.RouteTarget{}, err
	}
	target, err := repository.LookupRoute(ctx, siteID, path)
	if err != nil {
		return target, err
	}
	cache.WritePreparedJSON(ctx, r.store, prepared, key, target, r.ttl)
	return target, nil
}

func (r *invalidatingResourceRepository) LookupRoute(ctx context.Context, siteID site.ID, path string) (resource.RouteTarget, error) {
	repository, ok := r.base.(resource.CacheReadRepository)
	if !ok {
		return resource.RouteTarget{}, errors.New("resource cache read repository is unavailable")
	}
	return repository.LookupRoute(ctx, siteID, path)
}

func (r *invalidatingResourceRepository) WidgetsByID(ctx context.Context, owner resource.ID, ids []widget.BindingID) ([]widget.Binding, error) {
	repository, ok := r.base.(resource.CacheReadRepository)
	if !ok {
		return nil, errors.New("resource cache read repository is unavailable")
	}
	return repository.WidgetsByID(ctx, owner, ids)
}

func siteRoutesTag(id site.ID) cache.Tag { return cache.Tag(fmt.Sprintf("site:%d:routes", id)) }
func siteResourceTreeTag(id site.ID) cache.Tag {
	return cache.Tag(fmt.Sprintf("site:%d:resource-tree", id))
}
