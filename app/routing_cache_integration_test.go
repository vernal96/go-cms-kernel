package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	kernel "github.com/vernal96/go-cms-kernel"
	appkernel "github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/cache"
	pg "github.com/vernal96/go-cms-kernel/connectors/postgres"
	rediscache "github.com/vernal96/go-cms-kernel/connectors/redis"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core"
	corepg "github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/user/adapters/argon2id"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
	"github.com/vernal96/go-cms-kernel/transport/httpserver"
)

// Run against an isolated database. All reads use the real runtime cache and
// all mutations use application services and real PostgreSQL transactions.
func TestRoutingCachePostgresRedisLifecycle(t *testing.T) {
	host, redisAddr := os.Getenv("CMS_TEST_POSTGRES_HOST"), os.Getenv("CMS_TEST_REDIS_ADDR")
	if host == "" || redisAddr == "" {
		t.Skip("set CMS_TEST_POSTGRES_HOST and CMS_TEST_REDIS_ADDR")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	port, _ := strconv.Atoi(os.Getenv("CMS_TEST_POSTGRES_PORT"))
	if port == 0 {
		port = 5432
	}
	profile := kernel.ProfileCode("routing-cache")
	code := template.Code("cached")
	application, err := appkernel.New(ctx, appkernel.Definition{
		Logger: fakeLoggerFactory{}, EventBus: fakeEventBusFactory{}, PasswordHasher: argon2id.Factory{},
		MainDatabase: appkernel.DatabaseDefinition{Connector: pg.Factory{Config: pg.Config{Code: "main", Host: host, Port: port, Database: os.Getenv("CMS_TEST_POSTGRES_DB"), User: os.Getenv("CMS_TEST_POSTGRES_USER"), Password: os.Getenv("CMS_TEST_POSTGRES_PASSWORD"), SSLMode: "disable", MaxConns: 4, ConnMaxLifetime: time.Minute, ConnectTimeout: 5 * time.Second}}, Adapters: []kernel.ModuleDatabaseFactory{corepg.DatabaseFactory{}}},
		Caches:       []cache.Factory{rediscache.Factory{Config: rediscache.Config{Code: "test", Addrs: []string{redisAddr}, Prefix: fmt.Sprintf("routing-test:%d", time.Now().UnixNano())}}},
		Profiles:     []kernel.Profile{{Code: profile, Modules: []kernel.ProfileModule{{Module: core.Module{}, Caches: []cache.Binding{{Alias: core.DurableCacheAlias, Code: "test"}, {Alias: core.HotCacheAlias, Code: "test"}}}, {Module: admin.Module{}}}, Templates: []template.Definition{{Code: code, Label: "Cached", Layout: template.Layout{Body: []template.Item{template.ResourceWidgets{}}, Sidebar: []template.Item{template.ResourceWidgets{}}}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	if err = migrations.NewManager().UpAll(ctx, application.MigrationPlans()); err != nil {
		t.Fatal(err)
	}
	if err = application.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	actor := security.System()
	createSite := func(suffix string) *site.Runtime {
		t.Helper()
		runtime, err := application.Sites().Create(ctx, actor, site.CreateInput{ProfileCode: profile, Domain: fmt.Sprintf("cache-%d-%s.example", time.Now().UnixNano(), suffix), Locale: "en", IsPublic: true})
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	first, second := createSite("a"), createSite("b")
	repository := func(runtime *site.Runtime) resource.Repository {
		module, _ := runtime.Profile().Registry().Module(core.ModuleCode)
		return module.(*core.Runtime).Database().Resources()
	}
	create := func(input resource.CreateInput) resource.Resource {
		t.Helper()
		item, err := application.Resources().Create(ctx, actor, input)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	root := create(resource.CreateInput{SiteID: first.Site().ID, Title: "Root", Template: &code})
	other := create(resource.CreateInput{SiteID: second.Site().ID, Title: "Other", Template: &code})
	page := create(resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Title: "Original", Slug: "page", Template: &code})
	load := func(id resource.ID) resource.Resource {
		t.Helper()
		item, err := application.Resources().Get(ctx, actor, id)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	addWidget := func(owner resource.ID, html string) widget.Binding {
		t.Helper()
		current := load(owner)
		binding, err := application.Resources().CreateWidget(ctx, actor, owner, resource.CreateWidgetInput{ExpectedVersion: current.Version, Code: "core_html", Area: widget.AreaBody, Columns: 12, Params: map[string]any{"html": html}})
		if err != nil {
			t.Fatal(err)
		}
		return binding
	}
	w1, w2 := addWidget(page.ID, "first"), addWidget(page.ID, "second")
	handler, err := httpserver.CompileSite(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, status int) map[string]any {
		t.Helper()
		request := httptest.NewRequest("GET", path, nil)
		request = request.WithContext(core.WithSiteRuntime(httptransport.WithActor(request.Context(), actor), first))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s: status %d want %d: %s", path, response.Code, status, response.Body.String())
		}
		var result map[string]any
		if status == 200 {
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	body := func(result map[string]any) []any { return result["widgets"].(map[string]any)["body"].([]any) }
	html := func(value any) string { return value.(map[string]any)["data"].(map[string]any)["html"].(string) }
	for i := 0; i < 2; i++ {
		result := get("/page", 200)
		if html(body(result)[0]) != "first" {
			t.Fatal(result)
		}
	}
	if cached, err := repository(second).ByPath(ctx, other.SiteID, "/"); err != nil || cached.Title != "Other" {
		t.Fatalf("site isolation: %+v %v", cached, err)
	}
	current := load(page.ID)
	_, err = application.Resources().UpdateWidget(ctx, actor, page.ID, w1.ID, resource.UpdateWidgetInput{ExpectedVersion: current.Version, Columns: 12, Params: map[string]any{"html": "changed"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := html(body(get("/page", 200))[0]); got != "changed" {
		t.Fatalf("stale widget: %s", got)
	}
	current = load(page.ID)
	_, err = application.Resources().ReorderWidgets(ctx, actor, page.ID, current.Version, []widget.Order{{ID: w2.ID, Area: widget.AreaBody, Position: 0}, {ID: w1.ID, Area: widget.AreaBody, Position: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got := html(body(get("/page", 200))[0]); got != "second" {
		t.Fatalf("stale order: %s", got)
	}
	update := func(item resource.Resource, title, slug string) resource.Resource {
		t.Helper()
		result, err := application.Resources().Update(ctx, actor, resource.UpdateInput{ID: item.ID, ExpectedVersion: item.Version, ParentID: item.ParentID, Type: item.Type, Template: item.Template, Title: title, Slug: slug, IsPublic: true, TypeSettings: item.TypeSettings})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	page = update(load(page.ID), "Edited", "page")
	if got := get("/page", 200)["resource"].(map[string]any)["title"]; got != "Edited" {
		t.Fatalf("stale resource: %v", got)
	}
	child := create(resource.CreateInput{SiteID: root.SiteID, ParentID: &page.ID, Title: "Child", Slug: "child", Template: &code})
	get("/page/child", 200)
	page = update(load(page.ID), "Moved", "renamed")
	get("/page", 404)
	get("/page/child", 404)
	get("/renamed", 200)
	get("/renamed/child", 200)
	_, err = application.Resources().Create(ctx, actor, resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Title: "Collision", Slug: "renamed"})
	if !errors.Is(err, resource.ErrConflict) && !errors.Is(err, resource.ErrRouteConflict) {
		t.Fatalf("URL collision: %v", err)
	}
	get("/renamed", 200)
	if err = application.Resources().Delete(ctx, actor, page.ID); err != nil {
		t.Fatal(err)
	}
	get("/renamed", 404)
	get("/renamed/child", 404)
	if err = application.Resources().Restore(ctx, actor, page.ID, true); err != nil {
		t.Fatal(err)
	}
	get("/renamed", 200)
	get("/renamed/child", 200)
	if err = application.Resources().DeleteWidget(ctx, actor, page.ID, w1.ID, load(page.ID).Version); err != nil {
		t.Fatal(err)
	}
	if len(body(get("/renamed", 200))) != 1 {
		t.Fatal("deleted widget survived")
	}
	lib := create(resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Title: "Library", Slug: "library", Type: resourcetype.Library, TypeSettings: map[string]any{"item_url_pattern": "/{slug}"}})
	libraryItems, err := resource.NewLibraryService(repository(first).(resource.LibraryItemRepository), application.Resources())
	if err != nil {
		t.Fatal(err)
	}
	item, err := libraryItems.Create(ctx, actor, resource.CreateLibraryItemInput{SiteID: root.SiteID, LibraryID: lib.ID, Template: &code, Title: "Item", Slug: "item"})
	if err != nil {
		t.Fatal(err)
	}
	get("/library/item", 200)
	get("/library/item", 200)
	item, err = libraryItems.Update(ctx, actor, resource.UpdateLibraryItemInput{ID: item.ID, ExpectedVersion: item.Version, Template: &code, Title: "Item edited", Slug: "changed", IsPublic: true})
	if err != nil {
		t.Fatal(err)
	}
	get("/library/item", 404)
	get("/library/changed", 200)
	lib = update(load(lib.ID), "Library", "articles")
	get("/library/changed", 404)
	get("/articles/changed", 200)
	_, err = application.Resources().Create(ctx, actor, resource.CreateInput{SiteID: root.SiteID, ParentID: &lib.ID, Title: "Collision with item", Slug: "changed"})
	if !errors.Is(err, resource.ErrRouteConflict) {
		t.Fatalf("tree/item collision: %v", err)
	}
	get("/articles/changed", 200)
	if err = libraryItems.Delete(ctx, actor, item.ID, false); err != nil {
		t.Fatal(err)
	}
	get("/articles/changed", 404)
	if err = libraryItems.Restore(ctx, actor, item.ID); err != nil {
		t.Fatal(err)
	}
	get("/articles/changed", 200)
	// Publication remains a request-time decision despite a warm route/entity.
	current = load(root.ID)
	_, err = application.Resources().CreateWidget(ctx, actor, root.ID, resource.CreateWidgetInput{ExpectedVersion: current.Version, Code: "core_resource_list", Area: widget.AreaBody, Columns: 12, Params: map[string]any{"parent_mode": "current", "limit": int64(100)}})
	if err != nil {
		t.Fatal(err)
	}
	collectionContains := func(id resource.ID) bool {
		t.Helper()
		items := body(get("/", 200))[0].(map[string]any)["data"].(map[string]any)["items"].([]any)
		for _, item := range items {
			if item.(map[string]any)["id"] == float64(id) {
				return true
			}
		}
		return false
	}
	scheduledAt := time.Now().Add(2 * time.Second)
	scheduled := create(resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Title: "Scheduled", Slug: "scheduled", Template: &code, PublishedAt: &scheduledAt})
	get("/scheduled", 404)
	if collectionContains(scheduled.ID) {
		t.Fatal("scheduled item appeared early")
	}
	time.Sleep(time.Until(scheduledAt) + 30*time.Millisecond)
	get("/scheduled", 200)
	if !collectionContains(scheduled.ID) {
		t.Fatal("collection cache hid scheduled publication")
	}
	current = load(scheduled.ID)
	unpublishAt := time.Now().Add(2 * time.Second)
	_, err = application.Resources().Update(ctx, actor, resource.UpdateInput{ID: current.ID, ExpectedVersion: current.Version, ParentID: current.ParentID, Type: current.Type, Template: current.Template, Title: current.Title, Slug: current.Slug, IsPublic: true, UnpublishedAt: &unpublishAt})
	if err != nil {
		t.Fatal(err)
	}
	get("/scheduled", 200)
	if !collectionContains(scheduled.ID) {
		t.Fatal("collection lost published item")
	}
	time.Sleep(time.Until(unpublishAt) + 30*time.Millisecond)
	get("/scheduled", 404)
	if collectionContains(scheduled.ID) {
		t.Fatal("collection cache survived unpublication deadline")
	}
	// Library publication also gates its cached items.
	current = load(lib.ID)
	past := time.Now().Add(-time.Second)
	_, err = application.Resources().Update(ctx, actor, resource.UpdateInput{ID: current.ID, ExpectedVersion: current.Version, ParentID: current.ParentID, Type: current.Type, Title: current.Title, Slug: current.Slug, IsPublic: true, TypeSettings: current.TypeSettings, UnpublishedAt: &past})
	if err != nil {
		t.Fatal(err)
	}
	get("/articles/changed", 404)
	// Moving a resource between sites must invalidate both URL namespaces.
	current = load(child.ID)
	_, err = application.Resources().TransferToSite(ctx, actor, current.ID, second.Site().ID, current.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	get("/renamed/child", 404)
	if item, err := repository(second).ByPath(ctx, second.Site().ID, "/child"); err != nil || item.ID != child.ID {
		t.Fatalf("transferred route: %+v %v", item, err)
	}
	// A publication date may also be part of a library item's URL.
	datedLibrary := create(resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Title: "Dated", Slug: "dated", Type: resourcetype.Library, TypeSettings: map[string]any{"item_url_pattern": "/{year}/{slug}"}})
	oldDate := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	dated, err := libraryItems.Create(ctx, actor, resource.CreateLibraryItemInput{SiteID: root.SiteID, LibraryID: datedLibrary.ID, Template: &code, Title: "Dated item", Slug: "item", PublishedAt: &oldDate})
	if err != nil {
		t.Fatal(err)
	}
	get("/dated/2024/item", 200)
	newDate := oldDate.AddDate(1, 0, 0)
	_, err = libraryItems.Update(ctx, actor, resource.UpdateLibraryItemInput{ID: dated.ID, ExpectedVersion: dated.Version, Template: &code, Title: dated.Title, Slug: dated.Slug, IsPublic: true, PublishedAt: &newDate})
	if err != nil {
		t.Fatal(err)
	}
	get("/dated/2024/item", 404)
	get("/dated/2025/item", 200)
	// Revision restore replaces widget binding IDs and may replace the URL.
	revisionPage := create(resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Title: "Revision", Slug: "revision", Template: &code})
	revisionWidget := addWidget(revisionPage.ID, "original revision")
	saved := load(revisionPage.ID)
	get("/revision", 200)
	_, err = application.Resources().UpdateWidget(ctx, actor, saved.ID, revisionWidget.ID, resource.UpdateWidgetInput{ExpectedVersion: saved.Version, Columns: 12, Params: map[string]any{"html": "new revision"}})
	if err != nil {
		t.Fatal(err)
	}
	update(load(saved.ID), "Changed revision", "changed-revision")
	get("/changed-revision", 200)
	_, management, _, err := application.CMSManagement()
	if err != nil {
		t.Fatal(err)
	}
	_, err = management.RestoreRevision(ctx, actor, saved.SiteID, saved.ID, saved.Version, load(saved.ID).Version)
	if err != nil {
		t.Fatal(err)
	}
	get("/changed-revision", 404)
	if got := html(body(get("/revision", 200))[0]); got != "original revision" {
		t.Fatalf("stale restored widget: %s", got)
	}
}
