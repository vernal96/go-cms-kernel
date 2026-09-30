package httpserver_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/go-chi/chi/v5"
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

func (r *apiResourceRepository) ByPath(ctx context.Context, siteID site.ID, path string) (resource.Resource, error) {
	r.lookups = append(r.lookups, path)
	return r.resourceRepository.ByPath(ctx, siteID, path)
}

func (r *apiResourceRepository) ResolveLibraryItemRoute(_ context.Context, siteID site.ID, path string) (resource.LibraryItem, resource.Resource, error) {
	if siteID != 1 || path != "/news/release" {
		return resource.LibraryItem{}, resource.Resource{}, resource.ErrNotFound
	}
	return resource.LibraryItem{ID: 12, SiteID: 1, LibraryID: 11, Title: "Release", IsPublic: true},
		resource.Resource{ID: 11, SiteID: 1, Type: resourcetype.Library, IsPublic: true}, nil
}

func TestAPIPrefixKeepsContentPathsAndLibraryResolution(t *testing.T) {
	order := []string{}
	repo := &apiResourceRepository{searchRouteRepository: searchRouteRepository{
		resourceRepository: resourceRepository{byPath: map[string]resource.Resource{
			"/section/page": {ID: 10, SiteID: 1, Type: resourcetype.Page, Path: stringPointer("/section/page"), IsPublic: true},
		}},
	}}
	app := newTransportTestApp(t, transportModule{code: "transport", resourceType: transportResourceType{code: "test"}, order: &order}, repo)
	handler, err := newTestHandler(app)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ url, path string }{
		{"/api/section/%70age?source=menu", "/section/page"},
		{"/api/news/release?source=search", "/news/release"},
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
				Resource struct{ Path string } `json:"resource"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Resource.Path != test.path || repo.lookups[len(repo.lookups)-1] != test.path {
				t.Fatalf("resource path = %q, lookups = %v", payload.Resource.Path, repo.lookups)
			}
			if request.URL.RequestURI() != test.url {
				t.Fatalf("outer request URL was mutated: %s", request.URL.RequestURI())
			}
		})
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
