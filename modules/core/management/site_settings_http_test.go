package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type siteSettingsHTTPModule struct{ settingsHTTPModule }

func (siteSettingsHTTPModule) Registry() (kernel.ModuleRegistry, error) {
	return kernel.ModuleRegistry{FieldTypes: field.StandardTypes(), ResourceTypes: resourcetype.StandardTypes()}, nil
}

type siteSettingsHTTPProfiles struct{ blueprint *kernel.ProfileBlueprint }

func (p siteSettingsHTTPProfiles) ProfileBlueprint(code kernel.ProfileCode) (*kernel.ProfileBlueprint, bool) {
	return p.blueprint, code == "settings"
}

type siteSettingsHTTPAccess struct{ managementAuthorizer }

func (siteSettingsHTTPAccess) IsGuestSubject(_ context.Context, actor security.Actor) (bool, error) {
	return actor.IsGuest(), nil
}

type siteSettingsHTTPRepository struct{ managementSiteRepository }

func (r *siteSettingsHTTPRepository) Create(_ context.Context, _ *security.UserID, item site.Site) (site.Site, error) {
	item.ID = site.ID(len(r.page.Items) + 1)
	r.page.Items = append(r.page.Items, item)
	return item, nil
}

type siteSettingsHTTPResources struct{ extensionTestResources }

func (*siteSettingsHTTPResources) ByPath(context.Context, site.ID, string) (resource.Resource, error) {
	return resource.Resource{}, resource.ErrNotFound
}
func (*siteSettingsHTTPResources) Create(_ context.Context, _ *security.UserID, item resource.Resource, _ resource.ValidateImageMedia) (resource.Resource, error) {
	item.ID = 1
	return item, nil
}

func TestSiteSettingsHTTPCreateIncompleteAndUpdateRequired(t *testing.T) {
	ctx := context.Background()
	factory, err := kernel.NewProfileRuntimeFactory(extensionTestDatabaseResolver{}, kernel.RuntimeServices{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), EventBus: extensionTestBus{}})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "settings", Modules: []kernel.Module{siteSettingsHTTPModule{}}, Params: []field.Definition{{Key: "logo", Type: field.TypeFile, Label: "Logo", Required: true}}})
	if err != nil {
		t.Fatal(err)
	}
	repo := &siteSettingsHTTPRepository{}
	access := siteSettingsHTTPAccess{}
	catalog, err := site.NewCatalog(repo, siteSettingsHTTPProfiles{blueprint}, access)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := resource.NewService(&siteSettingsHTTPResources{}, catalog, managementHTTPMediaService{}, access)
	if err != nil {
		t.Fatal(err)
	}
	service := &Sites{authorization: authorization{sites: catalog, authorizer: access, policy: AllowAllSitesPolicy{}}, repository: repo, resources: resources}
	router := chi.NewRouter()
	registerContentRoutes(router, service, nil)
	for index, settings := range []string{"", `,"settings":{}`, `,"settings":{"logo":null}`, `,"settings":{"logo":""}`} {
		body := fmt.Sprintf(`{"name":"New","domain":"site%d.test","profile_code":"settings","locale":"ru-RU","is_public":false%s}`, index, settings)
		request := httptest.NewRequest(http.MethodPost, "/sites", strings.NewReader(body))
		request = request.WithContext(httptransport.WithActor(ctx, security.User(1)))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("create %s: status %d, %s", body, response.Code, response.Body.String())
		}
		var created SiteDetails
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		if len(created.Site.Settings) != 0 {
			t.Fatalf("empty settings retained: %#v", created.Site.Settings)
		}
		body = fmt.Sprintf(`{"name":"Edited","domain":"site%d.test","profile_code":"settings","locale":"ru-RU","is_public":false,"settings":{}}`, index)
		request = httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/sites/%d", created.Site.ID), strings.NewReader(body))
		request = request.WithContext(httptransport.WithActor(ctx, security.User(1)))
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("update: status %d, %s", response.Code, response.Body.String())
		}
		var payload struct {
			Error struct {
				Code    string `json:"code"`
				Details struct {
					Fields []FieldValidationError `json:"fields"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Error.Code != "validation_failed" || len(payload.Error.Details.Fields) != 1 || payload.Error.Details.Fields[0].Key != "logo" || payload.Error.Details.Fields[0].Code != "required" {
			t.Fatalf("unexpected error envelope: %s", response.Body.String())
		}
	}
}

type siteSettingsHTTPFiles struct{ file.Service }

func (siteSettingsHTTPFiles) GetFile(_ context.Context, _ security.Actor, id file.ID) (file.File, error) {
	return file.File{ID: id, MIMEType: "text/plain", Storage: "private-storage-detail"}, nil
}

func TestSiteSettingsHTTPRejectsUnsupportedFileFormat(t *testing.T) {
	ctx := context.Background()
	factory, err := kernel.NewProfileRuntimeFactory(extensionTestDatabaseResolver{}, kernel.RuntimeServices{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), EventBus: extensionTestBus{}})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "settings", Modules: []kernel.Module{siteSettingsHTTPModule{}}, Params: []field.Definition{
		{Key: "logo", Type: field.TypeFile, Label: "Logo", Required: true, Options: field.FileOptions{MIMETypes: []string{"image/*"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	repo := &siteSettingsHTTPRepository{managementSiteRepository: managementSiteRepository{page: site.Page{Items: []site.Site{
		{ID: 1, ProfileCode: "settings", Name: "Existing", Domain: "existing.test", Locale: "ru-RU"},
	}}}}
	access := siteSettingsHTTPAccess{}
	catalog, err := site.NewCatalog(repo, siteSettingsHTTPProfiles{blueprint}, access, siteSettingsHTTPFiles{})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	previous, _ := catalog.RuntimeByID(1)
	service := &Sites{authorization: authorization{sites: catalog, authorizer: access, policy: AllowAllSitesPolicy{}}, repository: repo}
	router := chi.NewRouter()
	registerContentRoutes(router, service, nil)
	for _, requestPath := range []struct {
		method string
		path   string
	}{
		{http.MethodPatch, "/sites/1"},
		{http.MethodPost, "/sites"},
	} {
		t.Run(requestPath.method, func(t *testing.T) {
			request := httptest.NewRequest(requestPath.method, requestPath.path, strings.NewReader(`{"name":"Edited","domain":"changed.test","profile_code":"settings","locale":"ru-RU","is_public":false,"settings":{"logo":9}}`))
			request = request.WithContext(httptransport.WithActor(ctx, security.User(1)))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			var payload struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
					Details struct {
						Fields []FieldValidationError `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Error.Code != "validation_failed" || !strings.Contains(payload.Error.Message, "file format") || !strings.Contains(payload.Error.Message, "logo") || len(payload.Error.Details.Fields) != 1 {
				t.Fatalf("unexpected error envelope: %s", response.Body.String())
			}
			failure := payload.Error.Details.Fields[0]
			allowed, ok := failure.Params["allowed_mime_types"].([]any)
			if failure.Key != "logo" || failure.Code != "file_constraints" || failure.Params["mime_type"] != "text/plain" || !ok || len(allowed) != 1 || allowed[0] != "image/*" {
				t.Fatalf("unexpected field error: %#v", failure)
			}
			if strings.Contains(response.Body.String(), "private-storage-detail") || strings.Contains(response.Body.String(), "CMS management") {
				t.Fatalf("internal details leaked: %s", response.Body.String())
			}
			current, _ := catalog.RuntimeByID(1)
			if current != previous || len(repo.page.Items) != 1 || repo.page.Items[0].Name != "Existing" {
				t.Fatal("rejected file changed the stored site or runtime")
			}
		})
	}
}
