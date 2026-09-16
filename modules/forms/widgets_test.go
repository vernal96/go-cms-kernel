package forms

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type widgetRepository struct {
	*repositoryStub
	queries []PageQuery
}

func (r *widgetRepository) FormByID(_ context.Context, siteID site.ID, id FormID) (Form, error) {
	if siteID != r.detail.Form.SiteID || id != r.detail.Form.ID {
		return Form{}, ErrNotFound
	}
	return r.detail.Form, nil
}
func (r *widgetRepository) FormByCode(ctx context.Context, siteID site.ID, code string, enabled bool) (Form, error) {
	if code != r.detail.Form.Code || enabled && !r.detail.Form.Enabled {
		return Form{}, ErrNotFound
	}
	return r.FormByID(ctx, siteID, r.detail.Form.ID)
}
func (r *widgetRepository) ListPublicResults(ctx context.Context, siteID site.ID, id FormID, query PageQuery) (PublicResultsPage, error) {
	form, err := r.FormByID(ctx, siteID, id)
	if err != nil {
		return PublicResultsPage{}, err
	}
	if !form.Enabled {
		return PublicResultsPage{}, ErrNotFound
	}
	r.queries = append(r.queries, query)
	return PublicResultsPage{Columns: []PublicResultColumn{}, Items: []PublicResult{}, Pagination: PublicResultPagination{Page: query.Page, PerPage: query.PerPage, Total: 0, Pages: 0}}, nil
}

func widgetTestService(t *testing.T) (*Service, *widgetRepository, *widget.Catalog) {
	t.Helper()
	stub := &repositoryStub{detail: publicHTTPFormDetail()}
	s, _ := publicHTTPService(t, stub)
	repo := &widgetRepository{repositoryStub: stub}
	s.repository = repo
	catalog, err := widget.Compile([]widget.Source{{Module: widget.ModuleDescriptor{Code: "forms", Label: "Формы", Description: "Формы"}, Widgets: (&Runtime{service: s}).Widgets()}}, nil, formsFieldResolver())
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, catalog
}

func TestFormWidgetSchemaMatchesPublicHTTPAndFollowsFormIdentity(t *testing.T) {
	s, repo, catalog := widgetTestService(t)
	runtime, ok := catalog.Widget("forms_form")
	if !ok {
		t.Fatal("Forms widget not contributed")
	}
	instance, err := runtime.New(map[string]any{"form_id": float64(9)})
	if err != nil {
		t.Fatal(err)
	}
	input := widget.RenderInput{Site: widget.SiteSnapshot{ID: 5}}
	for _, code := range []string{"feedback", "renamed"} {
		repo.detail.Form.Code = code
		output, err := instance.Render(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		router := chi.NewRouter()
		router.Get("/forms/{code}", (&publicFormsHTTP{service: s}).schema)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/forms/"+code, nil))
		schema, _ := json.Marshal(output["form"])
		var a, b any
		if response.Code != http.StatusOK {
			t.Fatal(response.Body.String())
		}
		if err := json.Unmarshal(schema, &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(response.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) || output["submit_url"] != "/forms/"+code+"/submit" {
			t.Fatalf("schema mismatch: %s %s", schema, response.Body.String())
		}
	}
	input.Site.ID = 6
	if _, err := instance.Render(context.Background(), input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("site isolation: %v", err)
	}
	input.Site.ID = 5
	repo.detail.Form.Enabled = false
	if _, err := instance.Render(context.Background(), input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled form: %v", err)
	}
	repo.detail.Form.Enabled = true
	repo.detail.Form.ID = 10
	if _, err := instance.Render(context.Background(), input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted form: %v", err)
	}
}

func TestResultsWidgetsValidateParamsAndPaginateIndependently(t *testing.T) {
	_, repo, catalog := widgetTestService(t)
	runtime, _ := catalog.Widget("forms_results")
	for _, params := range []map[string]any{{}, {"form_id": 0}, {"form_id": 9, "per_page": 0}, {"form_id": 9, "per_page": 101}, {"form_id": 9, "per_page": 1.5}} {
		if _, err := runtime.New(params); err == nil {
			t.Fatalf("accepted invalid params: %#v", params)
		}
	}
	for _, params := range []map[string]any{{"form_id": 9}, {"form_id": 9, "per_page": 3}} {
		instance, err := runtime.New(params)
		if err != nil {
			t.Fatal(err)
		}
		result, err := instance.Render(context.Background(), widget.RenderInput{Site: widget.SiteSnapshot{ID: 5}})
		if err != nil {
			t.Fatal(err)
		}
		if result["results_url"] != "/forms/feedback/results" {
			t.Fatal(result)
		}
	}
	if len(repo.queries) != 2 || repo.queries[0].PerPage != 20 || repo.queries[1].PerPage != 3 {
		t.Fatal(repo.queries)
	}
}

func TestPublicResultsHTTPPaginationAndIsolation(t *testing.T) {
	s, _, _ := widgetTestService(t)
	router := chi.NewRouter()
	contribution, err := (&Runtime{service: s}).HTTP().Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registrar := &formsRouteRegistrar{router: router}
	if err := contribution.Routes(registrar); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(registrar.names, []string{"forms.schema", "forms.results", "forms.submit"}) {
		t.Fatalf("module routes: %v", registrar.names)
	}
	for _, path := range []string{"/forms/feedback/results", "/forms/feedback/results?page=2&per_page=3"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
	for _, query := range []string{"page=0", "page=-1", "page=bad", "page=", "page=1&page=2", "per_page=0", "per_page=101", "page=9223372036854775807&per_page=100"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/forms/feedback/results?"+query, nil))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d", query, response.Code)
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/forms/missing/results", nil))
	if response.Code != 404 {
		t.Fatalf("missing form: %d", response.Code)
	}
}

func TestTransientFieldsCannotBePublished(t *testing.T) {
	for _, typ := range []field.TypeCode{FieldTypeCaptcha, FieldTypeUpload} {
		if err := validateFormField(FormField{FormID: 1, Code: "test", Label: "Test", Type: typ, ShowOnSite: true}, formsFieldResolver()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", typ, err)
		}
	}
}

type formsRouteRegistrar struct {
	httptransport.Registrar
	router chi.Router
	names  []string
}

func (r *formsRouteRegistrar) Route(route httptransport.Route) error {
	r.names = append(r.names, route.Name)
	r.router.Method(route.Method, route.Pattern, route.Handler)
	return nil
}
