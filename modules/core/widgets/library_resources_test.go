package widgets

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func TestLibraryResourcesUsesSourceSchemaAndIndependentCursor(t *testing.T) {
	sourcePath, mirrorPath := "/source", "/mirror"
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
			return resource.LibraryItemPage{Items: []resource.LibraryItem{{ID: 100, Slug: "entry"}}, NextCursor: "next"}, resource.LibraryCollection{Source: source, Mount: mount}, nil
		}
		instance, err := NewLibraryResources(query, mirror).New(map[string]any{"per_page": float64(5), "filters": []any{map[string]any{"field": "resource.field.score", "operator": "gte", "value": float64(3)}}})
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
	}
}
