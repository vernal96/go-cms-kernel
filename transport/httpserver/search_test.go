package httpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	appkernel "github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user/adapters/argon2id"
	"github.com/vernal96/go-cms-kernel/modules/search"
)

type searchTestDatabase struct{ engine search.Engine }

type searchRouteRepository struct {
	resourceRepository
	resource.LibraryItemRepository
}

func (searchRouteRepository) ResolveLibraryItemRoute(context.Context, site.ID, string) (resource.LibraryItem, resource.Resource, error) {
	return resource.LibraryItem{}, resource.Resource{}, resource.ErrNotFound
}

func (searchTestDatabase) ModuleCode() kernel.ModuleCode                             { return search.ModuleCode }
func (d searchTestDatabase) Search() search.Engine                                   { return d.engine }
func (d searchTestDatabase) Build(kernel.DBConnector) (kernel.ModuleDatabase, error) { return d, nil }

type siteRecordingEngine struct {
	mu    sync.Mutex
	calls []site.ID
}

func (e *siteRecordingEngine) Search(_ context.Context, query search.Query) (search.Page, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, query.SiteID)
	return search.Page{Items: []search.Item{{ID: resource.ID(query.SiteID), Title: "Одинаковый текст", URL: "/same", StorageKind: resource.StorageTree}}, Pagination: search.Pagination{Total: 1}}, nil
}

func TestSearchHTTPUsesHostScopedRuntime(t *testing.T) {
	engine := &siteRecordingEngine{}
	sites := &publicationSiteRepository{items: []site.Site{
		{ID: 1, ProfileCode: "searching", Domain: "first.example.test", Locale: "ru-RU", IsPublic: true},
		{ID: 2, ProfileCode: "searching", Domain: "second.example.test", Locale: "ru-RU", IsPublic: true},
		{ID: 3, ProfileCode: "searching", Domain: "private.example.test", Locale: "ru-RU", IsPublic: false},
		{ID: 4, ProfileCode: "plain", Domain: "plain.example.test", Locale: "ru-RU", IsPublic: true},
	}}
	app, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger: loggerFactory{}, PasswordHasher: argon2id.Factory{}, SiteAccessPolicy: admin.AllowAllSitesPolicy{}, EventBus: eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{Connector: connectorFactory{}, Adapters: []kernel.ModuleDatabaseFactory{databaseFactory{sites: sites, resources: searchRouteRepository{}}, searchTestDatabase{engine: engine}}},
		Profiles: []kernel.Profile{
			{Code: "searching", Modules: []kernel.ProfileModule{{Module: core.Module{}}, {Module: search.Module{}}, {Module: admin.Module{}}}},
			{Code: "plain", Modules: []kernel.ProfileModule{{Module: core.Module{}}, {Module: admin.Module{}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler, err := newTestHandler(app)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	for _, test := range []struct {
		host   string
		status int
		id     resource.ID
	}{
		{"FIRST.EXAMPLE.TEST.:8080", 200, 1}, {"second.example.test", 200, 2},
		{"private.example.test", 403, 0}, {"plain.example.test", 404, 0}, {"missing.example.test", 404, 0},
	} {
		request, err := http.NewRequest(http.MethodGet, server.URL+"/search?q=identical&site_id=999&preview=true", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = test.host
		request.Header.Set("Origin", "https://second.example.test")
		request.Header.Set("Referer", "https://second.example.test/")
		request.Header.Set("X-Forwarded-Host", "second.example.test")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != test.status {
			_ = response.Body.Close()
			t.Fatalf("%s: %d", test.host, response.StatusCode)
		}
		if test.status == 200 {
			var page search.Page
			err = json.NewDecoder(response.Body).Decode(&page)
			if err != nil || len(page.Items) != 1 || page.Items[0].ID != test.id || page.Pagination.Total != 1 || page.Pagination.PerPage != 20 {
				_ = response.Body.Close()
				t.Fatalf("response: %#v %v", page, err)
			}
		}
		_ = response.Body.Close()
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if fmt.Sprint(engine.calls) != "[1 2]" {
		t.Fatalf("engine sites = %v", engine.calls)
	}
}
