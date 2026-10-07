package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	sitepostgres "github.com/vernal96/go-cms-kernel/modules/core/site/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/modules/core/widgets"
	"github.com/vernal96/go-cms-kernel/modules/search"
)

func mirrorFixture(t *testing.T) (*fixture, resource.Resource, resource.Resource) {
	t.Helper()
	f := newFixture(t)
	source := f.create(t, f.sites[0], "Source", func(r *resource.Resource) {
		r.Type = resourcetype.Library
		r.TypeSettings = map[string]any{"item_url_pattern": "/{slug}"}
	})
	mirror := f.create(t, f.sites[1], "Mirror", func(r *resource.Resource) {
		r.Type = resourcetype.LibraryMirror
		r.TypeSettings = map[string]any{"source_library_id": int64(source.ID)}
	})
	t.Cleanup(func() {
		_, err := f.connector.Pool().Exec(context.Background(), `DELETE FROM core.resources WHERE id=$1`, mirror.ID)
		if err != nil {
			t.Error(err)
		}
	})
	return f, source, mirror
}

func TestPostgresLibraryMirrorRoutesLifecycleAndSearch(t *testing.T) {
	f, source, mirror := mirrorFixture(t)
	item := f.createItem(t, source, "mirrorneedle", nil)
	path := *mirror.Path + "/" + item.Slug
	resolved, mount, err := f.libraryItems.ResolveLibraryItemRoute(f.ctx, mirror.SiteID, path)
	if err != nil || resolved.ID != item.ID || resolved.SiteID != source.SiteID || mount.ID != mirror.ID {
		t.Fatalf("resolve: %+v %+v %v", resolved, mount, err)
	}
	find := func() search.Page {
		t.Helper()
		page, err := f.engine.Search(f.ctx, search.Query{SiteID: mirror.SiteID, RouteTypes: []resourcetype.Code{resourcetype.LibraryMirror}, Input: search.Input{Text: "mirrorneedle", Page: 1, PerPage: 20}})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	filtered, err := f.libraryItems.QueryLibraryItems(f.ctx, resource.LibraryItemQuery{SiteID: source.SiteID, LibraryID: source.ID, Limit: 10, PublicOnly: true, Filters: []resource.FilterCondition{{Field: resource.FieldID, Operator: resource.FilterEqual, Value: int64(item.ID)}}})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != item.ID || filtered.NextCursor != "" {
		t.Fatalf("filter: %+v %v", filtered, err)
	}
	if page := find(); page.Pagination.Total != 1 || page.Items[0].URL != path {
		t.Fatalf("search: %+v", page)
	}
	duplicate := resource.Clone(mirror)
	duplicate.ID = 0
	duplicate.Slug = "duplicate"
	duplicatePath := "/duplicate"
	duplicate.Path = &duplicatePath
	if _, err := f.resources.Create(f.ctx, nil, duplicate, nil); err == nil {
		t.Fatal("duplicate mirror accepted")
	}
	conflict := resource.Resource{SiteID: mirror.SiteID, ParentID: &mirror.ID, Type: resourcetype.Page, Slug: item.Slug, Title: "Conflict", Path: &path}
	if _, err := f.resources.Create(f.ctx, nil, conflict, nil); !errors.Is(err, resource.ErrRouteConflict) {
		t.Fatalf("tree collision: %v", err)
	}
	child := f.create(t, mirror.SiteID, "Reserved", func(r *resource.Resource) {
		r.ParentID = &mirror.ID
		r.Slug = "reserved"
		path := *mirror.Path + "/reserved"
		r.Path = &path
	})
	_, err = f.libraryItems.CreateLibraryItem(f.ctx, nil, resource.LibraryItem{SiteID: source.SiteID, LibraryID: source.ID, Title: "Conflict", Slug: child.Slug}, false)
	if !errors.Is(err, resource.ErrRouteConflict) {
		t.Fatalf("item collision: %v", err)
	}
	// Ordinary tree children are never mirrored.
	sourceChild := f.create(t, source.SiteID, "Child", func(r *resource.Resource) {
		r.ParentID = &source.ID
		r.Slug = "tree-child"
		path := *source.Path + "/tree-child"
		r.Path = &path
	})
	if _, _, err := f.libraryItems.ResolveLibraryItemRoute(f.ctx, mirror.SiteID, *mirror.Path+"/"+sourceChild.Slug); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("tree child mirrored: %v", err)
	}
	for _, tableAndID := range []struct {
		table string
		id    resource.ID
	}{{"resources", mirror.ID}, {"resources", source.ID}, {"library_items", item.ID}} {
		if _, err := f.connector.Pool().Exec(f.ctx, fmt.Sprintf("UPDATE core.%s SET is_public=false WHERE id=$1", tableAndID.table), tableAndID.id); err != nil {
			t.Fatal(err)
		}
		if page := find(); page.Pagination.Total != 0 {
			t.Fatalf("unpublished %s visible: %+v", tableAndID.table, page)
		}
		if _, err := f.connector.Pool().Exec(f.ctx, fmt.Sprintf("UPDATE core.%s SET is_public=true WHERE id=$1", tableAndID.table), tableAndID.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.connector.Pool().Exec(f.ctx, `UPDATE core.sites SET is_public=false WHERE id=$1`, source.SiteID); err != nil {
		t.Fatal(err)
	}
	if page := find(); page.Pagination.Total != 0 {
		t.Fatal("private source site searchable")
	}
	if _, err := f.connector.Pool().Exec(f.ctx, `UPDATE core.sites SET is_public=true WHERE id=$1`, source.SiteID); err != nil {
		t.Fatal(err)
	}
	updated := resource.Clone(source)
	updated.TypeSettings = map[string]any{"item_url_pattern": "/archive/{id}"}
	source, err = f.resources.Update(f.ctx, nil, source, updated, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.libraryItems.ResolveLibraryItemRoute(f.ctx, mirror.SiteID, path); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("old URL survives: %v", err)
	}
	path = fmt.Sprintf("%s/archive/%d", *mirror.Path, item.ID)
	if _, _, err := f.libraryItems.ResolveLibraryItemRoute(f.ctx, mirror.SiteID, path); err != nil {
		t.Fatal(err)
	}
	if page := find(); page.Pagination.Total != 1 || page.Items[0].URL != path {
		t.Fatalf("new URL search: %+v", page)
	}
	if err := f.resources.Delete(f.ctx, source.ID); !errors.Is(err, resource.ErrReferenced) {
		t.Fatal("source deletion allowed")
	}
	siteRepo, err := sitepostgres.NewRepository(f.connector)
	if err != nil {
		t.Fatal(err)
	}
	if err := siteRepo.Delete(f.ctx, source.SiteID); !errors.Is(err, site.ErrReferenced) {
		t.Fatal("source site deletion allowed")
	}
	other := f.create(t, source.SiteID, "Other", func(r *resource.Resource) {
		r.Type = resourcetype.Library
		r.TypeSettings = map[string]any{"item_url_pattern": "/{slug}"}
	})
	otherItem := f.createItem(t, other, "Replacement", nil)
	current, err := f.resources.ByID(f.ctx, mirror.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement := resource.Clone(current)
	replacement.TypeSettings = map[string]any{"source_library_id": int64(other.ID)}
	if _, err := f.resources.Update(f.ctx, nil, current, replacement, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.libraryItems.ResolveLibraryItemRoute(f.ctx, mirror.SiteID, path); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("retarget retained old URL: %v", err)
	}
	if resolved, _, err := f.libraryItems.ResolveLibraryItemRoute(f.ctx, mirror.SiteID, *mirror.Path+"/"+otherItem.Slug); err != nil || resolved.ID != otherItem.ID {
		t.Fatalf("retarget: %+v %v", resolved, err)
	}

}

func TestPostgresLibraryMirrorWidgetsLargeCollection(t *testing.T) {
	if os.Getenv("CMS_TEST_MIRROR_LARGE") != "1" {
		t.Skip("set CMS_TEST_MIRROR_LARGE=1 for 100,000-resource mirror pagination")
	}
	f, source, mirror := mirrorFixture(t)
	_, err := f.connector.Pool().Exec(f.ctx, `WITH entities AS (
 INSERT INTO core.resource_entities(site_id,storage_kind) SELECT $1,'library_item' FROM generate_series(1,100000) RETURNING id
 ) INSERT INTO core.library_items(id,site_id,library_id,partition_at,title,slug,is_public,is_searchable)
 SELECT id,$1,$2,now(),'Resource '||id,'resource-'||id,true,true FROM entities`, source.SiteID, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		// Delete the partitioned payload before identity rows to avoid per-identity cascade probes.
		if _, err := f.connector.Pool().Exec(cleanup, `DELETE FROM core.library_items WHERE library_id=$1`, source.ID); err != nil {
			t.Error(err)
		}
		if _, err := f.connector.Pool().Exec(cleanup, `DELETE FROM core.resource_entities WHERE site_id=$1 AND storage_kind='library_item'`, source.SiteID); err != nil {
			t.Error(err)
		}
	})
	started := time.Now()
	query := func(ctx context.Context, q resource.LibraryItemQuery) (resource.LibraryItemPage, resource.LibraryCollection, error) {
		mount := source
		if q.LibraryID == mirror.ID {
			mount = mirror
		}
		q.LibraryID = source.ID
		q.SiteID = source.SiteID
		page, err := f.libraryItems.QueryLibraryItems(ctx, q)
		return page, resource.LibraryCollection{Source: source, Mount: mount}, err
	}
	var firstIDs []resource.ID
	for _, isMirror := range []bool{false, true} {
		mount := source
		if isMirror {
			mount = mirror
		}
		instance, err := widgets.NewLibraryResources(query, isMirror).New(map[string]any{"per_page": float64(17), "sorting": []any{map[string]any{"field": "resource.id", "direction": "asc"}}})
		if err != nil {
			t.Fatal(err)
		}
		input := widget.RenderInput{Key: "resources", Site: widget.SiteSnapshot{ID: int64(mount.SiteID)}, Resource: widget.ResourceSnapshot{ID: int64(mount.ID)}}
		first, err := instance.Render(f.ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		items := first["resources"].([]map[string]any)
		if len(items) != 17 {
			t.Fatal(len(items))
		}
		firstIDs = append(firstIDs, items[0]["id"].(resource.ID))
		if items[0]["url"] != fmt.Sprintf("%s/resource-%d", *mount.Path, firstIDs[len(firstIDs)-1]) {
			t.Fatal(items[0])
		}
		input.Cursor = first["next_cursor"].(string)
		if input.Cursor == "" {
			t.Fatal("missing cursor")
		}
		second, err := instance.Render(f.ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		next := second["resources"].([]map[string]any)
		if next[0]["id"].(resource.ID) <= items[16]["id"].(resource.ID) {
			t.Fatal("cursor repeated entries")
		}
		input.Cursor = ""
		input.Key = "independent"
		independent, err := instance.Render(f.ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if independent["resources"].([]map[string]any)[0]["id"] != items[0]["id"] || independent["cursor_parameter"] != "cursor.independent" {
			t.Fatal(independent)
		}
	}
	if firstIDs[0] != firstIDs[1] {
		t.Fatal("mirror copied resources")
	}
	t.Logf("both widgets, 100000 resources, three pages each: %s", time.Since(started))
	var copies int
	if err := f.connector.Pool().QueryRow(f.ctx, `SELECT count(*) FROM core.library_items WHERE site_id=$1`, site.ID(mirror.SiteID)).Scan(&copies); err != nil || copies != 0 {
		t.Fatalf("copies=%d err=%v", copies, err)
	}
}
