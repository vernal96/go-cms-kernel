package postgres

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

func TestPostgresDynamicWidgetAreasLifecycle(t *testing.T) {
	_, database, ctx := openOutboxIntegrationDatabase(t)
	factory, err := kernel.NewProfileRuntimeFactory(hookTestResolver{}, kernel.RuntimeServices{EventBus: hookTestBus{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := database.Sites().(site.ManagementRepository).Create(ctx, nil, site.Site{ProfileCode: "zones", Domain: fmt.Sprintf("zones-%d.example", time.Now().UnixNano()), Locale: "ru", Settings: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	sites := hookTestSites{}
	reload := func(layout template.Layout) {
		t.Helper()
		blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "zones", Modules: []kernel.Module{bindingTestModule{}}, Templates: []template.Definition{{Code: "page", Label: "Page", Layout: layout}}})
		if err != nil {
			t.Fatal(err)
		}
		sites[stored.ID], err = site.NewRuntimeFromBlueprint(ctx, stored, blueprint)
		if err != nil {
			t.Fatal(err)
		}
	}
	reload(nil)
	service, err := resource.NewService(database.Resources(), sites, hookTestMedia{}, hookTestAccess{})
	if err != nil {
		t.Fatal(err)
	}
	actor := security.System()
	code := template.Code("page")
	tree, err := service.Create(ctx, actor, resource.CreateInput{SiteID: stored.ID, Template: &code, Title: "Root"})
	if err != nil {
		t.Fatal(err)
	}
	library, err := service.Create(ctx, actor, resource.CreateInput{SiteID: stored.ID, ParentID: &tree.ID, Type: resourcetype.Library, Title: "Library", Slug: "library"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := resource.NewLibraryService(database.Resources().(resource.LibraryItemRepository), service)
	if err != nil {
		t.Fatal(err)
	}
	item, err := items.Create(ctx, actor, resource.CreateLibraryItemInput{SiteID: stored.ID, LibraryID: library.ID, Template: &code, Title: "Item", Slug: "item"})
	if err != nil {
		t.Fatal(err)
	}
	load := func(id resource.ID) resource.Resource {
		t.Helper()
		if id == tree.ID {
			value, err := service.Get(ctx, actor, id)
			if err != nil {
				t.Fatal(err)
			}
			return value
		}
		value, err := items.Get(ctx, actor, id)
		if err != nil {
			t.Fatal(err)
		}
		return resource.Resource{ID: value.ID, SiteID: value.SiteID, Version: value.Version, Widgets: value.Widgets}
	}
	compose := func(value resource.Resource) widget.Placements {
		t.Helper()
		runtime, _ := sites[stored.ID].Profile().Template(code)
		result, err := template.Compose(runtime, value.Widgets)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	ids := []resource.ID{tree.ID, item.ID}
	for _, id := range ids {
		current := load(id)
		_, err := service.CreateWidget(ctx, actor, id, resource.CreateWidgetInput{ExpectedVersion: current.Version, Code: "core_hook_widget", Area: widget.AreaDefault, Columns: 12, Params: map[string]any{"text": "Hello"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	layout := template.Layout{{Code: "main", Label: "Main", AdminSpan: 16}, {Code: "aside", Label: "Aside", AdminSpan: 8}, {Code: "footer", Label: "Footer"}}
	reload(layout)
	for _, id := range ids {
		current := load(id)
		if rendered := compose(current); len(rendered) != 4 || len(rendered[widget.AreaDefault]) != 1 {
			t.Fatalf("added zones: %#v", rendered)
		}
		_, err := service.ReorderWidgets(ctx, actor, id, current.Version, []widget.Order{{ID: current.Widgets[0].ID, Area: "footer", Position: 0}})
		if err != nil {
			t.Fatal(err)
		}
		current = load(id)
		if _, exists := compose(current)[widget.AreaDefault]; exists {
			t.Fatal("default did not disappear")
		}
	}
	reload(layout[:2])
	for _, id := range ids {
		current := load(id)
		if current.Widgets[0].Area != "footer" || len(compose(current)[widget.AreaDefault]) != 1 {
			t.Fatal("missing area was lost or rewritten")
		}
	}
	reload(layout)
	for _, id := range ids {
		if len(compose(load(id))["footer"]) != 1 {
			t.Fatal("area did not return")
		}
	}
	reload(layout[:2])
	revisions, err := resource.NewRevisionService(database.Resources().(resource.RevisionRepository), service, items, hookTestAccess{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		current := load(id)
		historical := current.Version
		_, err := service.ReorderWidgets(ctx, actor, id, current.Version, []widget.Order{{ID: current.Widgets[0].ID, Area: "main", Position: 0}})
		if err != nil {
			t.Fatal(err)
		}
		current = load(id)
		restored, err := revisions.Restore(context.Background(), actor, stored.ID, id, historical, current.Version)
		if err != nil {
			t.Fatal(err)
		}
		if restored.Widgets[0].Area != "footer" || len(compose(restored)[widget.AreaDefault]) != 1 {
			t.Fatal("revision lost recovered area")
		}
	}
}
