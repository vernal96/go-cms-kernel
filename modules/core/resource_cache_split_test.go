package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

type splitRepository struct {
	*resourceRepositoryStub
	routeLoads  int
	widgetLoads [][]widget.BindingID
}

func (r *splitRepository) LookupRoute(_ context.Context, siteID site.ID, path string) (resource.RouteTarget, error) {
	r.routeLoads++
	if r.item.SiteID != siteID || r.item.Path == nil || *r.item.Path != path {
		return resource.RouteTarget{}, resource.ErrNotFound
	}
	return resource.RouteTarget{ID: r.item.ID, SiteID: siteID, Kind: resource.StorageTree}, nil
}
func (r *splitRepository) WidgetsByID(_ context.Context, owner resource.ID, ids []widget.BindingID) ([]widget.Binding, error) {
	r.widgetLoads = append(r.widgetLoads, append([]widget.BindingID(nil), ids...))
	result := make([]widget.Binding, 0, len(ids))
	if owner != r.item.ID {
		return result, nil
	}
	for _, id := range ids {
		for _, binding := range r.item.Widgets {
			if binding.ID == id {
				result = append(result, widget.CloneBinding(binding))
			}
		}
	}
	return result, nil
}

func TestSplitCacheReusesResourceIdentityAndLoadsOnlyMissingWidgets(t *testing.T) {
	ctx := context.Background()
	path := "/page"
	store := newMemoryCacheStore()
	base := &splitRepository{resourceRepositoryStub: &resourceRepositoryStub{item: resource.Resource{ID: 7, SiteID: 3, Path: &path, Title: "before", Widgets: []widget.Binding{
		{ID: 11, Code: "core_html", Area: "body", Position: 0, Params: map[string]any{"html": "one"}},
		{ID: 12, Code: "core_html", Area: "sidebar", Position: 0, Params: map[string]any{"html": "two"}},
	}}}}
	policy := newTestRepositoryCachePolicy(store)
	repo := &cachedResourceRepository{siteID: 3, base: &invalidatingResourceRepository{base: base, policy: policy}, store: store, ttl: time.Minute, policy: policy}
	first, err := repo.ByPath(ctx, 3, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ByID(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ByPath(ctx, 3, path); err != nil {
		t.Fatal(err)
	}
	if base.routeLoads != 1 || base.byIDCalls != 1 {
		t.Fatalf("warm reads loaded DB: route=%d resource=%d", base.routeLoads, base.byIDCalls)
	}
	var target resource.RouteTarget
	if err = json.Unmarshal(store.values[routeCacheKey(3, path)], &target); err != nil || target.ID != 7 {
		t.Fatalf("route identity: %+v %v", target, err)
	}
	record, err := cache.DecodeJSON[cachedResourceRecord](store.values[resourceCacheKey(7)])
	if err != nil || len(record.Resource.Widgets) != 0 || len(record.Widgets) != 2 {
		t.Fatalf("resource still embeds configs: %+v %v", record, err)
	}
	missing := widgetConfigKey(3, 7, record.Widgets[1])
	delete(store.values, missing)
	fresh, err := repo.ByID(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Widgets) != 2 || fresh.Widgets[1].Params["html"] != "two" || base.byIDCalls != 1 || !reflect.DeepEqual(base.widgetLoads, [][]widget.BindingID{{12}}) {
		t.Fatalf("partial load: %+v calls=%v full=%d", fresh, base.widgetLoads, base.byIDCalls)
	}
	changed := resource.Clone(first)
	changed.Title = "after"
	if _, err = repo.Update(ctx, nil, first, changed, nil); err != nil {
		t.Fatal(err)
	}
	if fresh, err = repo.ByPath(ctx, 3, path); err != nil || fresh.Title != "after" {
		t.Fatalf("updated read: %+v %v", fresh, err)
	}
	if base.routeLoads != 1 {
		t.Fatal("content update discarded URL identity")
	}
	if _, err = repo.ByPath(ctx, 4, path); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("cross-site URL collision: %v", err)
	}
	if routeCacheKey(3, "/ab") == routeCacheKey(3, "/a/b") || routeCacheKey(3, path) == routeCacheKey(4, path) {
		t.Fatal("route key collision")
	}
}

func TestSplitCacheRejectsMixedWidgetSnapshots(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCacheStore()
	base := &splitRepository{resourceRepositoryStub: &resourceRepositoryStub{item: resource.Resource{ID: 7, SiteID: 3, Title: "old", Widgets: []widget.Binding{{ID: 11, Code: "core_html", Area: "body", Params: map[string]any{"html": "old"}}}}}}
	repo := &cachedResourceRepository{siteID: 3, base: base, store: store, ttl: time.Minute}
	if _, err := repo.ByID(ctx, 7); err != nil {
		t.Fatal(err)
	}
	record, _ := cache.DecodeJSON[cachedResourceRecord](store.values[resourceCacheKey(7)])
	delete(store.values, widgetConfigKey(3, 7, record.Widgets[0]))
	// A different process commits a new resource snapshot while this reader
	// still holds the old header. Partial refill must detect the hash mismatch.
	base.item.Title = "new"
	base.item.Widgets[0].Params["html"] = "new"
	item, err := repo.ByID(ctx, 7)
	if err != nil || item.Title != "new" || item.Widgets[0].Params["html"] != "new" || base.byIDCalls != 2 {
		t.Fatalf("mixed snapshot: %+v %v calls=%d", item, err, base.byIDCalls)
	}
	// A corrupt widget payload is also repaired without reloading the header.
	record, _ = cache.DecodeJSON[cachedResourceRecord](store.values[resourceCacheKey(7)])
	store.values[widgetConfigKey(3, 7, record.Widgets[0])] = []byte("not json")
	item, err = repo.ByID(ctx, 7)
	if err != nil || item.Widgets[0].Params["html"] != "new" || base.byIDCalls != 2 {
		t.Fatalf("corruption recovery: %+v %v", item, err)
	}
}

func TestSplitCacheDoesNotPersistFailedRouteLoads(t *testing.T) {
	ctx := context.Background()
	path := "/old"
	store := newMemoryCacheStore()
	base := &splitRepository{resourceRepositoryStub: &resourceRepositoryStub{item: resource.Resource{ID: 7, SiteID: 3, Path: &path}}}
	repo := &cachedResourceRepository{siteID: 3, base: base, store: store, ttl: time.Minute}
	if _, err := repo.ByPath(ctx, 3, "/new"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatal(err)
	}
	path = "/new"
	if item, err := repo.ByPath(ctx, 3, path); err != nil || item.ID != 7 {
		t.Fatalf("new route masked by previous 404: %+v %v", item, err)
	}
}

type mirrorCacheRepository struct {
	*splitRepository
	resource.LibraryItemRepository
	item  resource.LibraryItem
	mount resource.Resource
}

func (r *mirrorCacheRepository) LookupRoute(_ context.Context, id site.ID, path string) (resource.RouteTarget, error) {
	r.routeLoads++
	if id != r.mount.SiteID || r.mount.Path == nil || path != *r.mount.Path+"/"+r.item.Slug {
		return resource.RouteTarget{}, resource.ErrNotFound
	}
	return resource.RouteTarget{ID: r.item.ID, SiteID: id, Kind: resource.StorageLibraryItem, LibraryID: r.mount.ID, Mirrored: true}, nil
}
func (r *mirrorCacheRepository) ResolveLibraryItemRoute(_ context.Context, id site.ID, path string) (resource.LibraryItem, resource.Resource, error) {
	if id != r.mount.SiteID || r.mount.Path == nil || path != *r.mount.Path+"/"+r.item.Slug {
		return resource.LibraryItem{}, resource.Resource{}, resource.ErrNotFound
	}
	return r.item, r.mount, nil
}

func TestMirrorRouteCacheReflectsSourceChangesWithoutTargetInvalidation(t *testing.T) {
	path := "/mirror"
	ctx := context.Background()
	store := newMemoryCacheStore()
	base := &mirrorCacheRepository{splitRepository: &splitRepository{resourceRepositoryStub: &resourceRepositoryStub{}}, item: resource.LibraryItem{ID: 100, SiteID: 1, LibraryID: 10, Slug: "old", Title: "Before"}, mount: resource.Resource{ID: 20, SiteID: 2, Path: &path}}
	repo := &cachedResourceRepository{siteID: 2, base: base, store: store, ttl: time.Hour}
	first, _, err := repo.ResolveLibraryItemRoute(ctx, 2, "/mirror/old")
	if err != nil || first.Title != "Before" {
		t.Fatalf("%+v %v", first, err)
	}
	base.item.Title = "After"
	second, _, err := repo.ResolveLibraryItemRoute(ctx, 2, "/mirror/old")
	if err != nil || second.Title != "After" {
		t.Fatalf("stale content: %+v %v", second, err)
	}
	base.item.Slug = "new"
	if _, _, err := repo.ResolveLibraryItemRoute(ctx, 2, "/mirror/old"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("stale route: %v", err)
	}
	if item, _, err := repo.ResolveLibraryItemRoute(ctx, 2, "/mirror/new"); err != nil || item.ID != 100 {
		t.Fatalf("new route: %+v %v", item, err)
	}
	if len(store.values) != 0 {
		t.Fatal("mirror route was cached")
	}
}
