package widgets

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func TestLibraryResourcesUsesSourceSchemaAndIndependentCursor(t *testing.T) {
	sourcePath, mirrorPath := "/source", "/mirror"
	templateCode := template.Code("article")
	sourceRuntime := &site.Runtime{}
	for _, mirror := range []bool{false, true} {
		mount := resource.Resource{ID: 10, SiteID: 1, Type: resourcetype.Library, Path: &sourcePath}
		source := mount
		if mirror {
			mount = resource.Resource{ID: 20, SiteID: 2, Type: resourcetype.LibraryMirror, Path: &mirrorPath}
		}
		query := func(_ context.Context, q resource.LibraryItemQuery) (resource.LibraryItemPage, resource.LibraryCollection, error) {
			if q.LibraryID != mount.ID || q.SiteID != mount.SiteID || q.Cursor != "own-cursor" || q.Limit != 5 || !q.PublicOnly || len(q.Filters) != 1 || q.Filters[0].Field != "resource.field.score" || q.Filters[0].Kind != "" {
				t.Fatalf("query=%+v", q)
			}
			return resource.LibraryItemPage{Items: []resource.LibraryItem{{ID: 100, Slug: "entry", Template: &templateCode, Fields: map[string]any{"cover": int64(42)}}}, NextCursor: "next"}, resource.LibraryCollection{Source: source, Mount: mount, Runtime: sourceRuntime}, nil
		}
		projected := false
		instance, err := NewLibraryResources(query, mirror, func(_ context.Context, runtime *site.Runtime, code *template.Code, fields map[string]any) (map[string]any, error) {
			projected = true
			if runtime != sourceRuntime || code == nil || *code != templateCode || fields["cover"] != int64(42) {
				t.Fatalf("projection runtime=%p code=%v fields=%#v", runtime, code, fields)
			}
			return map[string]any{"cover": map[string]any{"id": int64(42), "url": "/_cms/files/42"}}, nil
		}).New(map[string]any{"per_page": float64(5), "filters": []any{map[string]any{"field": "resource.field.score", "operator": "gte", "value": float64(3)}}})
		if err != nil {
			t.Fatal(err)
		}
		output, err := instance.Render(context.Background(), widget.RenderInput{Key: "a", Cursor: "own-cursor", Site: widget.SiteSnapshot{ID: int64(mount.SiteID)}, Resource: widget.ResourceSnapshot{ID: int64(mount.ID)}})
		if err != nil {
			t.Fatal(err)
		}
		if output["next_cursor"] != "next" || output["cursor_parameter"] != "cursor.a" {
			t.Fatal(output)
		}
		items := output["resources"].([]map[string]any)
		fields := items[0]["fields"].(map[string]any)
		if !projected || fields["cover"].(map[string]any)["url"] != "/_cms/files/42" {
			t.Fatalf("projection missing or not enriched: projected=%v output=%#v", projected, output)
		}
	}
}
