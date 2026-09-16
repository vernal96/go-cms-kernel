package postgres

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

type bindingTestModule struct{ hookTestModule }

func (bindingTestModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return hookTestRuntime{}, nil
}

func TestPostgresWidgetParamBindingsLifecycle(t *testing.T) {
	_, database, ctx := openOutboxIntegrationDatabase(t)
	module := bindingTestModule{}
	factory, err := kernel.NewProfileRuntimeFactory(hookTestResolver{}, kernel.RuntimeServices{EventBus: hookTestBus{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	layout := template.Layout{Body: []template.Item{template.ResourceWidgets{}}, Sidebar: []template.Item{template.ResourceWidgets{}}}
	blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "bindings", Modules: []kernel.ProfileModule{{Module: module}}, Templates: []template.Definition{
		{Code: "bound", Label: "Bound", Fields: []field.Definition{{Key: "headline", Label: "Headline", Type: field.TypeString}}, Layout: layout},
		{Code: "empty", Label: "Empty", Layout: layout},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sites := hookTestSites{}
	siteIDs := []site.ID{}
	for i := 0; i < 2; i++ {
		stored, err := database.Sites().(site.ManagementRepository).Create(ctx, nil, site.Site{ProfileCode: "bindings", Domain: fmt.Sprintf("bindings-%d-%d.example", time.Now().UnixNano(), i), Locale: "ru-RU", Settings: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := site.NewRuntimeFromBlueprint(ctx, stored, blueprint)
		if err != nil {
			t.Fatal(err)
		}
		sites[stored.ID] = runtime
		siteIDs = append(siteIDs, stored.ID)
	}
	service, err := resource.NewService(database.Resources(), sites, hookTestMedia{}, hookTestAccess{})
	if err != nil {
		t.Fatal(err)
	}
	actor := security.System()
	root, err := service.Create(ctx, actor, resource.CreateInput{SiteID: siteIDs[0], Title: "Root"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(ctx, actor, resource.CreateInput{SiteID: siteIDs[1], Title: "Target root"})
	if err != nil {
		t.Fatal(err)
	}
	code := template.Code("bound")
	tree, err := service.Create(ctx, actor, resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Template: &code, Title: "Tree", Slug: "tree", Fields: map[string]any{"headline": "original"}})
	if err != nil {
		t.Fatal(err)
	}
	library, err := service.Create(ctx, actor, resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Type: resourcetype.Library, Title: "Library", Slug: "library"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := resource.NewLibraryService(database.Resources().(resource.LibraryItemRepository), service)
	if err != nil {
		t.Fatal(err)
	}
	item, err := items.Create(ctx, actor, resource.CreateLibraryItemInput{SiteID: root.SiteID, LibraryID: library.ID, Template: &code, Title: "Item", Slug: "item", Fields: map[string]any{"headline": "item original"}})
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := resource.NewRevisionService(database.Resources().(resource.RevisionRepository), service, items, hookTestAccess{})
	if err != nil {
		t.Fatal(err)
	}
	load := func(id resource.ID) (resource.Resource, error) {
		if id != item.ID {
			return service.Get(ctx, actor, id)
		}
		current, err := items.Get(ctx, actor, id)
		return resource.Resource{ID: current.ID, SiteID: current.SiteID, Version: current.Version, Widgets: current.Widgets}, err
	}
	for _, id := range []resource.ID{tree.ID, item.ID} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			current, err := load(id)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := service.CreateWidget(ctx, actor, id, resource.CreateWidgetInput{ExpectedVersion: current.Version, Code: "core_hook_widget", Area: widget.AreaBody, Columns: 12, ParamBindings: widget.ParamBindings{"text": widget.ResourceField("headline")}})
			if err != nil {
				t.Fatal(err)
			}
			current, err = load(id)
			if err != nil {
				t.Fatal(err)
			}
			historical := current.Version
			check := func(current resource.Resource) {
				t.Helper()
				if len(current.Widgets) != 1 || current.Widgets[0].ParamBindings["text"] != widget.ResourceField("headline") || len(current.Widgets[0].Params) != 0 {
					t.Fatalf("bindings lost: %#v", current.Widgets)
				}
			}
			check(current)
			_, err = service.UpdateWidget(ctx, actor, id, binding.ID, resource.UpdateWidgetInput{ExpectedVersion: current.Version, Columns: 6, ParamBindings: widget.ParamBindings{"text": widget.ResourceProperty("title")}})
			if err != nil {
				t.Fatal(err)
			}
			current, err = load(id)
			if err != nil {
				t.Fatal(err)
			}
			if current.Widgets[0].ParamBindings["text"] != widget.ResourceProperty("title") {
				t.Fatal("update lost reference")
			}
			restored, err := revisions.Restore(ctx, actor, current.SiteID, id, historical, current.Version)
			if err != nil {
				t.Fatal(err)
			}
			check(restored)
			loaded, err := load(id)
			if err != nil {
				t.Fatal(err)
			}
			check(loaded)
		})
	}
	// A schema switch must not persist a dangling source.
	current, err := service.Get(ctx, actor, tree.ID)
	if err != nil {
		t.Fatal(err)
	}
	empty := template.Code("empty")
	_, err = service.Update(ctx, actor, resource.UpdateInput{ID: tree.ID, ExpectedVersion: current.Version, ParentID: &root.ID, Template: &empty, Title: current.Title, Slug: current.Slug})
	if err == nil {
		t.Fatal("schema switch accepted dangling binding")
	}
	moved, err := service.TransferToSite(ctx, actor, tree.ID, siteIDs[1], current.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Resource.SiteID != siteIDs[1] || moved.Resource.Widgets[0].ParamBindings["text"] != widget.ResourceField("headline") {
		t.Fatal("transfer lost binding")
	}
	current, err = service.Get(ctx, actor, library.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.TransferToSite(ctx, actor, library.ID, siteIDs[1], current.Version, nil); err != nil {
		t.Fatal(err)
	}
	transferredItem, err := items.Get(ctx, actor, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if transferredItem.SiteID != siteIDs[1] || transferredItem.Widgets[0].ParamBindings["text"] != widget.ResourceField("headline") {
		t.Fatal("library transfer lost item binding")
	}
}
