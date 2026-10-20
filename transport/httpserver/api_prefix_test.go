package httpserver_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

type apiResourceRepository struct {
	searchRouteRepository
	lookups []string
}

func (r *apiResourceRepository) ByID(ctx context.Context, id resource.ID) (resource.Resource, error) {
	if id == 11 {
		_, library, err := r.ResolveLibraryItemRoute(ctx, 1, "/news/release")
		return library, err
	}
	return r.resourceRepository.ByID(ctx, id)
}

func (r *apiResourceRepository) ByPath(ctx context.Context, siteID site.ID, path string) (resource.Resource, error) {
	r.lookups = append(r.lookups, path)
	return r.resourceRepository.ByPath(ctx, siteID, path)
}

func (r *apiResourceRepository) BreadcrumbCandidates(ctx context.Context, siteID site.ID, id resource.ID, includeHome bool) ([]resource.BreadcrumbCandidate, error) {
	if id == 11 {
		items := []resource.BreadcrumbCandidate{{ID: 11, Title: "News", MenuTitle: "Новости", Link: "/news", IsPublic: true}}
		if includeHome {
			items = append([]resource.BreadcrumbCandidate{{ID: 9, Title: "Home", Link: "/", IsPublic: true}}, items...)
		}
		return items, nil
	}
	return r.searchRouteRepository.resourceRepository.BreadcrumbCandidates(ctx, siteID, id, includeHome)
}

func (r *apiResourceRepository) ResolveLibraryItemRoute(_ context.Context, siteID site.ID, path string) (resource.LibraryItem, resource.Resource, error) {
	if siteID != 1 || path != "/news/release" {
		return resource.LibraryItem{}, resource.Resource{}, resource.ErrNotFound
	}
	return resource.LibraryItem{ID: 12, SiteID: 1, LibraryID: 11, Title: "Release", Slug: "release", IsPublic: true},
		resource.Resource{ID: 11, SiteID: 1, Type: resourcetype.Library, Path: stringPointer("/news"), IsPublic: true, TypeSettings: map[string]any{"item_url_pattern": "/{slug}"}}, nil
}

func TestAPIPrefixKeepsContentPathsAndLibraryResolution(t *testing.T) {
	order := []string{}
	sectionID := resource.ID(8)
	repo := &apiResourceRepository{searchRouteRepository: searchRouteRepository{
		resourceRepository: resourceRepository{byPath: map[string]resource.Resource{
			"/":             {ID: 9, SiteID: 1, Type: resourcetype.Page, Path: stringPointer("/"), IsPublic: true},
			"/section":      {ID: sectionID, SiteID: 1, Type: resourcetype.Page, Title: "Section", MenuTitle: "Section menu", Path: stringPointer("/section"), IsPublic: true},
			"/section/page": {ID: 10, SiteID: 1, ParentID: &sectionID, Type: resourcetype.Page, Title: "Page", Path: stringPointer("/section/page"), IsPublic: true},
		}},
	}}
	app := newTransportTestAppWithSettings(t, transportModule{code: "transport", resourceType: transportResourceType{code: "test"}, order: &order}, repo, kernel.ProfileSettings{IncludeHomeInBreadcrumbs: true})
	handler, err := newTestHandler(app)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ url, path string }{
		{"/api/resources/resolve?source=home", "/"},
		{"/api/resources/resolve/section/%70age?source=menu", "/section/page"},
		{"/api/resources/resolve/news/release?source=search", "/news/release"},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.url, nil)
			request.Host = "example.com"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var payload struct {
				Resource struct {
					Path        string `json:"path"`
					Breadcrumbs []struct {
						Title string `json:"title"`
						Link  string `json:"link"`
					} `json:"breadcrumbs"`
				} `json:"resource"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Resource.Path != test.path || repo.lookups[len(repo.lookups)-1] != test.path || payload.Resource.Breadcrumbs == nil {
				t.Fatalf("resource path = %q, breadcrumbs = %#v, lookups = %v", payload.Resource.Path, payload.Resource.Breadcrumbs, repo.lookups)
			}
			if len(payload.Resource.Breadcrumbs) == 0 || payload.Resource.Breadcrumbs[len(payload.Resource.Breadcrumbs)-1].Link != test.path {
				t.Fatalf("resource breadcrumbs = %#v", payload.Resource.Breadcrumbs)
			}
			homeCount := 0
			for _, breadcrumb := range payload.Resource.Breadcrumbs {
				if breadcrumb.Link == "/" {
					homeCount++
				}
			}
			if homeCount != 1 {
				t.Fatalf("home breadcrumb count = %d: %#v", homeCount, payload.Resource.Breadcrumbs)
			}
			if test.path == "/section/page" && (len(payload.Resource.Breadcrumbs) != 3 || payload.Resource.Breadcrumbs[1].Title != "Section menu") {
				t.Fatalf("nested breadcrumbs = %#v", payload.Resource.Breadcrumbs)
			}
			if request.URL.RequestURI() != test.url {
				t.Fatalf("outer request URL was mutated: %s", request.URL.RequestURI())
			}
		})
	}
	appWithoutHome := newTransportTestAppWithSettings(t, transportModule{code: "transport", resourceType: transportResourceType{code: "test"}, order: &order}, repo, kernel.ProfileSettings{})
	handlerWithoutHome, err := newTestHandler(appWithoutHome)
	if err != nil {
		t.Fatal(err)
	}
	requestWithoutHome := httptest.NewRequest(http.MethodGet, "/api/resources/resolve/news/release", nil)
	requestWithoutHome.Host = "example.com"
	responseWithoutHome := httptest.NewRecorder()
	handlerWithoutHome.ServeHTTP(responseWithoutHome, requestWithoutHome)
	var withoutHome struct {
		Resource struct {
			Breadcrumbs []struct {
				Title string `json:"title"`
				Link  string `json:"link"`
			} `json:"breadcrumbs"`
		} `json:"resource"`
	}
	if responseWithoutHome.Code != http.StatusOK || json.Unmarshal(responseWithoutHome.Body.Bytes(), &withoutHome) != nil {
		t.Fatalf("without-home response = %d: %s", responseWithoutHome.Code, responseWithoutHome.Body.String())
	}
	if len(withoutHome.Resource.Breadcrumbs) != 2 || withoutHome.Resource.Breadcrumbs[0].Link != "/news" || withoutHome.Resource.Breadcrumbs[1].Link != "/news/release" {
		t.Fatalf("without-home breadcrumbs = %#v", withoutHome.Resource.Breadcrumbs)
	}
	requestWithoutHome = httptest.NewRequest(http.MethodGet, "/api/resources/resolve/section/page", nil)
	requestWithoutHome.Host = "example.com"
	responseWithoutHome = httptest.NewRecorder()
	handlerWithoutHome.ServeHTTP(responseWithoutHome, requestWithoutHome)
	if responseWithoutHome.Code != http.StatusOK ||
		json.Unmarshal(responseWithoutHome.Body.Bytes(), &withoutHome) != nil ||
		len(withoutHome.Resource.Breadcrumbs) != 2 ||
		withoutHome.Resource.Breadcrumbs[0].Link != "/section" {
		t.Fatalf("ordinary nested chain without home: status=%d breadcrumbs=%#v body=%s", responseWithoutHome.Code, withoutHome.Resource.Breadcrumbs, responseWithoutHome.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/resources/resolve/missing", nil)
	request.Host = "example.com"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing resource status = %d: %s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/api/about", "/api/resources/resolve/"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = "example.com"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("legacy or trailing-slash path %q status = %d", path, response.Code)
		}
	}

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	getRequest, err := http.NewRequest(http.MethodGet, server.URL+"/api/resources/resolve/section/page", nil)
	if err != nil {
		t.Fatal(err)
	}
	getRequest.Host = "example.com"
	getResponse, err := http.DefaultClient.Do(getRequest)
	if err != nil {
		t.Fatal(err)
	}
	getBody, err := io.ReadAll(getResponse.Body)
	_ = getResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	headRequest, err := http.NewRequest(http.MethodHead, server.URL+"/api/resources/resolve/section/page", nil)
	if err != nil {
		t.Fatal(err)
	}
	headRequest.Host = "example.com"
	headResponse, err := http.DefaultClient.Do(headRequest)
	if err != nil {
		t.Fatal(err)
	}
	headBody, err := io.ReadAll(headResponse.Body)
	_ = headResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if headResponse.StatusCode != getResponse.StatusCode || len(headBody) != 0 ||
		headResponse.Header.Get("Content-Type") != getResponse.Header.Get("Content-Type") ||
		headResponse.Header.Get("Content-Length") != getResponse.Header.Get("Content-Length") ||
		len(getBody) == 0 {
		t.Fatalf("GET status/headers/body = %d/%v/%d; HEAD = %d/%v/%d", getResponse.StatusCode, getResponse.Header, len(getBody), headResponse.StatusCode, headResponse.Header, len(headBody))
	}
}

func TestAPIManagementNamespacesNeverReachPublicResources(t *testing.T) {
	order := []string{}
	repo := &apiResourceRepository{}
	app := newTransportTestApp(t, transportModule{code: "transport", resourceType: transportResourceType{code: "test"}, order: &order}, repo)
	handler, err := newTestHandler(app)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"auth", "admin", "sites", "site-profiles", "files", "media", "administration", "_cms", "api"} {
		for _, suffix := range []string{"", "/missing"} {
			path := "/api/" + prefix + suffix
			t.Run(path, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.Host = "example.com"
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusNotFound && response.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d: %s", response.Code, response.Body.String())
				}
				if len(repo.lookups) != 0 || len(order) != 0 {
					t.Fatalf("management reached public dispatch: lookups %v, middleware %v", repo.lookups, order)
				}
			})
		}
	}
}

func TestPublicAPIMethodMismatchKeeps405AndAllow(t *testing.T) {
	order := []string{}
	app := newTransportTestApp(t, transportModule{code: "transport", resourceType: transportResourceType{code: "test"}, order: &order}, &apiResourceRepository{})
	handler, err := newTestHandler(app)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/site", "/api/menu", "/api/custom"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Host = "example.com"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("%s: status = %d, Allow = %q", path, response.Code, response.Header().Get("Allow"))
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/resources/resolve/article", nil)
	request.Host = "example.com"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("resource resolve: status = %d, Allow = %q", response.Code, response.Header().Get("Allow"))
	}

}

func TestPublicAPIPreservesModuleRouteParametersAndRequest(t *testing.T) {
	calls := 0
	module := compilerModule{code: "public_api", contribution: httptransport.Contribution{
		Routes: func(registrar httptransport.Registrar) error {
			return registrar.Group("/submission", nil, func(registrar httptransport.Registrar) error {
				return registrar.Route(httptransport.Route{
					Method: http.MethodPost, Pattern: "/{code}",
					Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
						calls++
						body, err := io.ReadAll(request.Body)
						runtime, exists := core.SiteRuntimeFromContext(request.Context())
						actor, _ := httptransport.ActorFromContext(request.Context())
						if err != nil || string(body) != "payload" || request.URL.Path != "/submission/feedback" ||
							chi.URLParam(request, "code") != "feedback" || request.URL.Query().Get("from") != "page" ||
							request.Header.Get("Content-Type") != "text/plain" || !exists || runtime.Site().ID != 1 || actor != security.Guest() {
							t.Errorf("request context changed: URL %s, actor %#v, body %q, runtime %v, err %v", request.URL, actor, body, exists, err)
						}
						response.WriteHeader(http.StatusNoContent)
					}),
				})
			})
		},
	}}
	handler, err := newTestHandler(newTransportTestApp(t, module, &apiResourceRepository{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path   string
		status int
	}{
		{"/api/submission/feedback?from=page", http.StatusNoContent},
		{"/submission/feedback?from=page", http.StatusNotFound},
		{"/api/api/submission/feedback?from=page", http.StatusNotFound},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader("payload"))
		request.Host = "example.com"
		request.Header.Set("Content-Type", "text/plain")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s: status = %d: %s", test.path, response.Code, response.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("module calls = %d", calls)
	}
}
