package management

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type managementHTTPMediaService struct{}

func (managementHTTPMediaService) Create(context.Context, security.Actor, media.CreateInput) (media.Media, error) {
	return media.Media{}, errors.New("not implemented")
}
func (managementHTTPMediaService) Get(context.Context, security.Actor, media.ID) (media.Media, error) {
	return media.Media{}, media.ErrNotFound
}
func (managementHTTPMediaService) Resolve(context.Context, security.Actor, media.ID) (media.ResolvedMedia, error) {
	return media.ResolvedMedia{}, media.ErrNotFound
}
func (managementHTTPMediaService) Update(context.Context, security.Actor, media.UpdateInput) (media.Media, error) {
	return media.Media{}, errors.New("not implemented")
}
func (managementHTTPMediaService) Delete(context.Context, security.Actor, media.ID) error {
	return errors.New("not implemented")
}

var _ media.Service = managementHTTPMediaService{}

type managementAdministrator bool

func (a managementAdministrator) IsAdministrator(context.Context, security.Actor) (bool, error) {
	return bool(a), nil
}

func TestAdministrationRevisionAPIDeniesDirectNonAdministrator(t *testing.T) {
	t.Parallel()
	router := chi.NewRouter()
	registerContentRoutes(router, nil, &Resources{administrator: managementAdministrator(false)})
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		request := httptest.NewRequest(method, "/administration/resource-revisions", nil)
		request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, body = %s", method, response.Code, response.Body.String())
		}
	}
}

func TestManagementHTTPListSiteOptionsAndErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		url             string
		wantExcludeID   site.ID
		denied          map[permission.Code]error
		repositoryError error
		wantStatus      int
		wantCode        string
	}{
		{name: "success", url: "/sites/options?page=1&per_page=10", wantStatus: http.StatusOK},
		{name: "exclude current site", url: "/sites/options?exclude_id=7", wantExcludeID: 7, wantStatus: http.StatusOK},
		{name: "bad pagination", url: "/sites/options?page=zero", wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
		{name: "forbidden", url: "/sites/options", denied: map[permission.Code]error{SiteReadPermission: security.ErrForbidden}, wantStatus: http.StatusForbidden, wantCode: "forbidden"},
		{name: "database failure", url: "/sites/options", repositoryError: errors.New("pq: secret database detail"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &managementSiteRepository{page: site.Page{
				Items: []site.Site{{ID: 1, Domain: "example.com", ProfileCode: "dev", Locale: "ru-RU"}},
				Total: 1,
			}, err: test.repositoryError}
			management := &Sites{
				authorization: authorization{
					authorizer: managementAuthorizer{denied: test.denied},
					policy:     scopedPolicy{scope: SiteAccessScope{All: true}},
				},
				repository: repository,
			}
			router := chi.NewRouter()
			registerContentRoutes(router, management, nil)
			request := httptest.NewRequest(http.MethodGet, test.url, nil)
			request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if test.wantExcludeID > 0 && (repository.query.ExcludeID == nil || *repository.query.ExcludeID != test.wantExcludeID) {
				t.Fatalf("exclude id = %#v", repository.query.ExcludeID)
			}
			if strings.Contains(response.Body.String(), "secret database detail") {
				t.Fatalf("database error leaked: %s", response.Body.String())
			}
			if test.wantCode != "" {
				var envelope struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Error.Code != test.wantCode {
					t.Fatalf("error code = %q", envelope.Error.Code)
				}
			}
		})
	}
}

func TestManagementHTTPWritesStructuredFieldErrors(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	writeManagementError(response, ValidationError{
		Message: "request data is invalid",
		Fields:  []FieldValidationError{{Key: "page_title", Rule: "required", Param: ""}},
	})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", response.Code)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Fields []FieldValidationError `json:"fields"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "validation_failed" || len(envelope.Error.Details.Fields) != 1 ||
		envelope.Error.Details.Fields[0].Key != "page_title" {
		t.Fatalf("error = %#v", envelope)
	}
}

func TestManagementHTTPWritesRouteMaintenanceConflict(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	writeManagementError(response, resource.ErrRouteMutationRequiresMaintenance)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"route_mutation_requires_maintenance"`) {
		t.Fatalf("response = %d, %s", response.Code, response.Body.String())
	}
}

func TestManagementHTTPWritesStableSiteTransferConflicts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		err  error
		code string
	}{
		{err: resource.ErrIncompatibleTargetSite, code: "incompatible_target_site"},
		{err: resource.ErrCrossSiteReference, code: "cross_site_reference"},
		{err: resource.ErrRouteConflict, code: "route_conflict"},
		{err: resource.ErrInvalidTree, code: "invalid_tree"},
	} {
		response := httptest.NewRecorder()
		writeManagementError(response, test.err)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"`+test.code+`"`) {
			t.Fatalf("%s response = %d, %s", test.code, response.Code, response.Body.String())
		}
	}
}

func TestManagementHTTPTransferResourceValidatesPayload(t *testing.T) {
	t.Parallel()
	router := chi.NewRouter()
	registerContentRoutes(router, nil, &Resources{})
	for _, test := range []struct {
		body       string
		wantStatus int
	}{
		{body: `{}`, wantStatus: http.StatusUnprocessableEntity},
		{body: `{"target_site_id":0,"expected_version":1}`, wantStatus: http.StatusUnprocessableEntity},
		{body: `{"target_site_id":8,"expected_version":0}`, wantStatus: http.StatusUnprocessableEntity},
		{body: `{"target_site_id":8,"expected_version":1,"unexpected":true}`, wantStatus: http.StatusBadRequest},
	} {
		request := httptest.NewRequest(http.MethodPost, "/sites/7/resources/9/transfer", strings.NewReader(test.body))
		request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.wantStatus {
			t.Fatalf("body %s response = %d, %s", test.body, response.Code, response.Body.String())
		}
	}
}

func TestParseLibraryItemSemanticQuery(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet,
		`/items?filters=%5B%7B%22field%22%3A%22published_at%22%2C%22operator%22%3A%22gte%22%2C%22value%22%3A%222026-01-01T00%3A00%3A00Z%22%7D%2C%7B%22field%22%3A%22resource.field.rank%22%2C%22operator%22%3A%22in%22%2C%22value%22%3A%5B1%2C2%5D%7D%2C%7B%22field%22%3A%22is_public%22%2C%22operator%22%3A%22eq%22%2C%22value%22%3Atrue%7D%5D&sort=%5B%7B%22field%22%3A%22created_at%22%2C%22direction%22%3A%22desc%22%7D%5D`, nil)
	filters, sorts, err := parseLibraryItemSemanticQuery(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 3 || filters[0].Field != resource.FieldPublishedAt ||
		filters[1].Field != "resource.field.rank" || filters[2].Field != resource.FieldIsPublic {
		t.Fatalf("filters = %#v", filters)
	}
	values, ok := filters[1].Value.([]any)
	if !ok || len(values) != 2 {
		t.Fatalf("numeric set = %#v", filters[1].Value)
	}
	if _, ok := values[0].(json.Number); !ok {
		t.Fatalf("numeric type = %T", values[0])
	}
	if value, ok := filters[2].Value.(bool); !ok || !value {
		t.Fatalf("boolean value = %#v", filters[2].Value)
	}
	if len(sorts) != 1 || sorts[0].Field != resource.FieldCreatedAt || sorts[0].Direction != resource.SortDescending {
		t.Fatalf("sorts = %#v", sorts)
	}
}

func TestParseLibraryItemSemanticQueryRejectsUnknownJSON(t *testing.T) {
	t.Parallel()
	for _, query := range []string{
		`filters=%5B%7B%22field%22%3A%22title%22%2C%22operator%22%3A%22eq%22%2C%22value%22%3A%22x%22%2C%22kind%22%3A%22string%22%7D%5D`,
		`filters=%5B%7B%22field%22%3A%22unknown%22%2C%22operator%22%3A%22eq%22%2C%22value%22%3A%22x%22%7D%5D`,
		`sort=%5B%7B%22field%22%3A%22created_at%22%2C%22direction%22%3A%22desc%22%7D%5Dtrailing`,
	} {
		request := httptest.NewRequest(http.MethodGet, "/items?"+query, nil)
		if _, _, err := parseLibraryItemSemanticQuery(request); err == nil {
			t.Fatalf("accepted query %q", query)
		}
	}
}

func TestManagementHTTPWidgetRoutesValidateStableBindingID(t *testing.T) {
	t.Parallel()
	router := chi.NewRouter()
	registerContentRoutes(router, nil, &Resources{})
	request := httptest.NewRequest(
		http.MethodPatch,
		"/sites/7/resources/9/widgets/not-an-id",
		strings.NewReader(`{}`),
	)
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "widget_id is invalid") {
		t.Fatalf("response = %d, %s", response.Code, response.Body.String())
	}
}

func TestManagementHTTPWidgetLifecycleUsesStableBindingID(t *testing.T) {
	t.Parallel()
	management, repository := contentHTTPWidgetFixture(t)
	router := chi.NewRouter()
	registerContentRoutes(router, nil, management)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		current := httptest.NewRequest(method, path, strings.NewReader(body))
		current = current.WithContext(httptransport.WithActor(current.Context(), security.User(1)))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, current)
		return response
	}

	createdResponse := request(http.MethodPost, "/sites/7/resources/9/widgets", `{
		"code":"feature_content","area":"body","view":"default","columns":12,
		"margin_top":0,"margin_bottom":0,"enabled":true,"params":{},"expected_version":1
	}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create response = %d, %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created ResourceWidget
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID <= 0 || created.View != widget.DefaultView || created.Area != widget.AreaBody {
		t.Fatalf("created widget = %#v", created)
	}

	updatedResponse := request(http.MethodPatch, "/sites/7/resources/9/widgets/1", `{
		"view":"default","columns":6,"margin_top":1,"margin_bottom":2,
		"enabled":false,"params":{},"expected_version":2
	}`)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update response = %d, %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	var updated ResourceWidget
	if err := json.NewDecoder(updatedResponse.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.ID != created.ID || updated.Columns != 6 || updated.Enabled {
		t.Fatalf("updated widget = %#v", updated)
	}

	orderResponse := request(http.MethodPut, "/sites/7/resources/9/widgets/order", `{
		"expected_version":3,"items":[{"id":1,"area":"sidebar","position":0}]
	}`)
	if orderResponse.Code != http.StatusOK {
		t.Fatalf("order response = %d, %s", orderResponse.Code, orderResponse.Body.String())
	}
	var ordered struct {
		Items []ResourceWidget `json:"items"`
	}
	if err := json.NewDecoder(orderResponse.Body).Decode(&ordered); err != nil {
		t.Fatal(err)
	}
	if len(ordered.Items) != 1 || ordered.Items[0].ID != created.ID || ordered.Items[0].Area != widget.AreaSidebar {
		t.Fatalf("ordered widgets = %#v", ordered.Items)
	}

	deletedResponse := request(http.MethodDelete, "/sites/7/resources/9/widgets/1", `{"expected_version":4}`)
	if deletedResponse.Code != http.StatusNoContent || len(repository.item.Widgets) != 0 {
		t.Fatalf("delete response = %d, %s; widgets = %#v", deletedResponse.Code, deletedResponse.Body.String(), repository.item.Widgets)
	}
}

func contentHTTPWidgetFixture(t *testing.T) (*Resources, *extensionTestResources) {
	t.Helper()
	profile := kernel.Profile{
		Code:    "widget-http",
		Modules: []kernel.ProfileModule{{Module: widgetMetadataModule{}}},
		Templates: []template.Definition{{
			Code: "page", Label: "Page",
			Layout: template.Layout{
				Body:    []template.Item{template.ResourceWidgets{}},
				Sidebar: []template.Item{template.ResourceWidgets{}},
			},
		}},
	}
	factory, err := kernel.NewProfileRuntimeFactory(extensionTestDatabaseResolver{}, kernel.RuntimeServices{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), EventBus: extensionTestBus{},
	})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	siteRuntime, err := site.NewRuntimeFromBlueprint(context.Background(), site.Site{
		ID: 7, ProfileCode: profile.Code, Domain: "example.com", Locale: "ru-RU", Settings: map[string]any{},
	}, blueprint)
	if err != nil {
		t.Fatal(err)
	}
	templateCode := template.Code("page")
	contentType := "html"
	path := "/"
	repository := &extensionTestResources{item: resource.Resource{
		ID: 9, SiteID: 7, Version: 1, Type: resourcetype.Page, Template: &templateCode,
		ContentType: &contentType, Title: "Home", Path: &path, Fields: map[string]any{},
	}}
	authorizer := managementAuthorizer{denied: map[permission.Code]error{}}
	resources, err := resource.NewService(
		repository,
		extensionTestSites{runtime: siteRuntime},
		managementHTTPMediaService{},
		authorizer,
	)
	if err != nil {
		t.Fatal(err)
	}
	return &Resources{
		authorization: authorization{
			sites: extensionTestSites{runtime: siteRuntime}, authorizer: authorizer,
			policy: extensionTestPolicy{},
		},
		resources: resources, resourceRepo: repository,
	}, repository
}
