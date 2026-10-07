package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel"
	appkernel "github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/user/adapters/argon2id"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	corewidgets "github.com/vernal96/go-cms-kernel/modules/core/widgets"
	"github.com/vernal96/go-cms-kernel/modules/seo"
)

type mirrorHTTPRepository struct {
	resourceRepository
	resource.LibraryItemRepository
	item       resource.LibraryItem
	queries    []resource.LibraryItemQuery
	queryMutex sync.Mutex
}

func (r *mirrorHTTPRepository) QueryLibraryItems(_ context.Context, query resource.LibraryItemQuery) (resource.LibraryItemPage, error) {
	r.queryMutex.Lock()
	r.queries = append(r.queries, query)
	r.queryMutex.Unlock()
	cursor, err := resource.EncodeLibraryCursor(query, r.item)
	return resource.LibraryItemPage{Items: []resource.LibraryItem{r.item}, NextCursor: cursor}, err
}

func (r *mirrorHTTPRepository) ResolveLibraryItemRoute(_ context.Context, id site.ID, path string) (resource.LibraryItem, resource.Resource, error) {
	mount := r.byID[10]
	if id == 2 {
		mount = r.byID[20]
	}
	if mount.SiteID != id || path != *mount.Path+"/story" {
		return resource.LibraryItem{}, resource.Resource{}, resource.ErrNotFound
	}
	return r.item, mount, nil
}

type mirrorSEORepository struct{}

func (mirrorSEORepository) ByResource(context.Context, site.ID, resource.ID) (seo.Metadata, error) {
	return seo.Metadata{}, seo.ErrNotFound
}
func (mirrorSEORepository) UsedByResources(context.Context, site.ID, []resource.ID) (bool, error) {
	return false, nil
}
func (mirrorSEORepository) Save(_ context.Context, m seo.Metadata) (seo.Metadata, error) {
	return m, nil
}

type mirrorSEODatabase struct{}

func (mirrorSEODatabase) ModuleCode() kernel.ModuleCode    { return seo.ModuleCode }
func (mirrorSEODatabase) ResourceMetadata() seo.Repository { return mirrorSEORepository{} }
func (mirrorSEODatabase) Build(kernel.DBConnector) (kernel.ModuleDatabase, error) {
	return mirrorSEODatabase{}, nil
}

func TestLibraryMirrorHTTPSourceProfileWidgetsSEOAndPublication(t *testing.T) {
	ctx := context.Background()
	sourceTemplate := template.Code("source_only")
	mountTemplate := template.Code("mount_only")
	repo := &mirrorHTTPRepository{resourceRepository: resourceRepository{byID: map[resource.ID]resource.Resource{
		10: {ID: 10, SiteID: 1, Type: resourcetype.Library, Title: "Source", Path: stringPointer("/library"), IsPublic: true, TypeSettings: map[string]any{"item_url_pattern": "/{slug}"}},
		20: {ID: 20, SiteID: 2, Type: resourcetype.LibraryMirror, Template: &mountTemplate, Title: "Own mirror", Content: "Own content", Path: stringPointer("/mirror"), IsPublic: true, TypeSettings: map[string]any{"source_library_id": int64(10)}},
	}}, item: resource.LibraryItem{ID: 100, SiteID: 1, LibraryID: 10, Template: &sourceTemplate, Title: "Story", Slug: "story", IsPublic: true, Content: `<a href="https://source.test/raw">original HTML</a>`}}
	repo.byPath = map[string]resource.Resource{"/mirror": repo.byID[20]}
	sites := &publicationSiteRepository{items: []site.Site{{Name: "Test site", ID: 1, ProfileCode: "source", Domain: "source.test", Locale: "ru-RU", IsPublic: true}, {Name: "Test site", ID: 2, ProfileCode: "destination", Domain: "mirror.test", Locale: "en-US", IsPublic: true}}}
	var order []string
	probe := widget.NewRef("probe")
	module := transportModule{code: "source_widgets", resourceType: transportResourceType{code: "probe_type"}, order: &order, widgets: []widget.Widget{handlerWidget{definition: widget.Definition{Reference: probe, Label: "Probe", Description: "Source runtime probe"}, new: func(map[string]any) (widget.Instance, error) {
		return handlerWidgetInstance{render: func(_ context.Context, input widget.RenderInput) (map[string]any, error) {
			return map[string]any{"domain": input.Site.Domain, "site_id": input.Site.ID, "title": input.Resource.Title, "cursor": input.Cursor}, nil
		}}, nil
	}}}}
	app, err := appkernel.New(ctx, appkernel.Definition{Logger: loggerFactory{}, PasswordHasher: argon2id.Factory{}, SiteAccessPolicy: admin.AllowAllSitesPolicy{}, EventBus: eventBusFactory{}, MainDatabase: appkernel.DatabaseDefinition{Connector: connectorFactory{}, Adapters: []kernel.ModuleDatabaseFactory{databaseFactory{sites: sites, resources: repo}, mirrorSEODatabase{}}}, Profiles: []kernel.Profile{
		{Code: "source", Modules: []kernel.Module{core.New(core.Config{}), admin.New(), module, seo.New(seo.Config{DefaultCanonicalTemplate: "https://{{ site.domain }}{{ resource.path }}"})}, Templates: []template.Definition{{Code: sourceTemplate, Label: "Source", Layout: template.Layout{{Code: "main", Label: "Main", Items: []template.Item{template.Widget{Widget: corewidgets.Content}, template.Widget{Widget: probe}, template.ResourceWidgets{}}}}}}},
		{Code: "destination", Modules: []kernel.Module{core.New(core.Config{}), admin.New()}, Templates: []template.Definition{{Code: mountTemplate, Label: "Mount", Layout: template.Layout{{Code: "main", Label: "Main", Items: []template.Item{template.Widget{Widget: corewidgets.Content}, template.Widget{Widget: corewidgets.LibraryMirrorResources, Params: map[string]any{"per_page": int64(1)}}, template.Widget{Widget: corewidgets.LibraryMirrorResources, Params: map[string]any{"per_page": int64(2)}}}}}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	handler, err := newTestHandler(app)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest("GET", "http://mirror.test/api"+path, nil))
		return res
	}
	res := request("/mirror/story")
	if res.Code != 200 {
		t.Fatalf("%d %s", res.Code, res.Body.String())
	}
	var body struct {
		Resource struct {
			ID       int64
			Path     string
			Template string
		}
		Widgets    map[string][]struct{ Data map[string]any }
		Extensions map[string]any
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Resource.ID != 100 || body.Resource.Path != "/mirror/story" || body.Resource.Template != "source_only" {
		t.Fatalf("resource: %+v %s", body.Resource, res.Body.String())
	}
	raw := res.Body.String()
	if !strings.Contains(raw, "https://source.test/library/story") || !strings.Contains(raw, `"domain":"source.test"`) || !strings.Contains(raw, "original HTML") {
		t.Fatalf("source context missing: %s", raw)
	}
	res = request("/mirror")
	if res.Code != 200 || !strings.Contains(res.Body.String(), "Own content") || strings.Contains(res.Body.String(), "source_only") {
		t.Fatalf("mount: %d %s", res.Code, res.Body.String())
	}

	var lists struct {
		Widgets map[string][]struct{ Data map[string]any }
	}
	if err := json.Unmarshal(res.Body.Bytes(), &lists); err != nil {
		t.Fatal(err)
	}
	if len(lists.Widgets["main"]) != 3 {
		t.Fatal(res.Body.String())
	}
	firstKey, ok := lists.Widgets["main"][1].Data["cursor_parameter"].(string)
	if !ok {
		t.Fatal(res.Body.String())
	}
	secondKey, ok := lists.Widgets["main"][2].Data["cursor_parameter"].(string)
	if !ok || firstKey == secondKey {
		t.Fatal(res.Body.String())
	}
	cursor := lists.Widgets["main"][1].Data["next_cursor"].(string)
	repo.queries = nil
	res = request("/mirror?" + url.QueryEscape(firstKey) + "=" + url.QueryEscape(cursor))
	sort.Slice(repo.queries, func(i, j int) bool { return repo.queries[i].Limit < repo.queries[j].Limit })
	if res.Code != 200 || len(repo.queries) != 2 || repo.queries[0].Cursor != cursor || repo.queries[1].Cursor != "" || repo.queries[0].SiteID != 1 || repo.queries[0].LibraryID != 10 || !strings.Contains(res.Body.String(), `"url":"/mirror/story"`) {
		t.Fatalf("independent cursors: %+v %s", repo.queries, res.Body.String())
	}
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	for _, mutate := range []func(){func() { r := repo.byID[10]; r.IsPublic = false; repo.byID[10] = r }, func() { r := repo.byID[20]; r.IsPublic = false; repo.byID[20] = r; repo.byPath["/mirror"] = r }, func() { repo.item.IsPublic = false }, func() { repo.item.PublishedAt = &future }, func() { repo.item.UnpublishedAt = &past }} {
		source, mount, item := repo.byID[10], repo.byID[20], repo.item
		mutate()
		res = request("/mirror/story")
		if res.Code != 404 {
			t.Fatalf("unpublished: %d %s", res.Code, res.Body.String())
		}
		repo.byID[10], repo.byID[20], repo.item = source, mount, item
		repo.byPath["/mirror"] = mount
	}
	repo.item.Title = "Updated source"
	res = request("/mirror/story")
	if res.Code != 200 || !strings.Contains(res.Body.String(), "Updated source") {
		t.Fatalf("stale source: %s", res.Body.String())
	}
}
