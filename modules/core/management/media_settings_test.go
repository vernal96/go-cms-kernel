package management

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type settingsHTTPRepository struct {
	media.Repository
	item media.Media
}

func (r *settingsHTTPRepository) ByID(_ context.Context, id media.ID) (media.Media, error) {
	if id != r.item.ID {
		return media.Media{}, media.ErrNotFound
	}
	return media.Clone(r.item), nil
}
func (r *settingsHTTPRepository) UpdateSettings(_ context.Context, _ *security.UserID, id media.ID, values map[string]any, expected time.Time) (media.Media, error) {
	if id != r.item.ID {
		return media.Media{}, media.ErrNotFound
	}
	if !expected.Equal(r.item.UpdatedAt) {
		return media.Media{}, media.ErrSettingsConflict
	}
	r.item.Params["settings"] = values
	r.item.UpdatedAt = r.item.UpdatedAt.Add(time.Second)
	return media.Clone(r.item), nil
}

type settingsHTTPModule struct{ service *media.SettingsService }

func (settingsHTTPModule) Code() kernel.ModuleCode       { return "core" }
func (settingsHTTPModule) ModuleCode() kernel.ModuleCode { return "core" }
func (settingsHTTPModule) Registry() kernel.ModuleRegistry {
	return kernel.ModuleRegistry{FieldTypes: field.StandardTypes()}
}
func (m settingsHTTPModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return m, nil
}
func (m settingsHTTPModule) MediaSettings() *media.SettingsService { return m.service }

func TestMediaSettingsHTTP(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := &settingsHTTPRepository{item: media.Media{ID: 5, Params: map[string]any{}, UpdatedAt: now}}
	catalog, err := media.CompileSettings([]media.SettingsDefinition{{Code: "image", Fields: []field.Definition{{Key: "alt", Label: "Alt", Type: field.TypeString, Rules: []string{"max=5"}}}}}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	denied := map[permission.Code]error{}
	auth := managementAuthorizer{denied: denied}
	service, err := media.NewSettingsService(catalog, repo, &thumbnailFiles{}, auth)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := kernel.NewProfileRuntimeFactory(extensionTestDatabaseResolver{}, kernel.RuntimeServices{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), EventBus: extensionTestBus{}})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "test", Modules: []kernel.ProfileModule{{Module: settingsHTTPModule{service}}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := site.NewRuntimeFromBlueprint(ctx, site.Site{ID: 7, ProfileCode: "test", Domain: "settings.test", Locale: "ru-RU", Settings: map[string]any{}}, blueprint)
	if err != nil {
		t.Fatal(err)
	}
	sites := &Sites{authorization: authorization{sites: extensionTestSites{runtime: runtime}, authorizer: auth, policy: extensionTestPolicy{}}}
	router := chi.NewRouter()
	registerContentRoutes(router, sites, nil)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(httptransport.WithActor(req.Context(), security.User(1)))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	path := "/sites/7/media/5/settings"
	get := call("GET", path+"?code=image", "")
	if get.Code != 200 {
		t.Fatal(get.Code, get.Body.String())
	}
	var state media.SettingsState
	if err := json.Unmarshal(get.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Fields) != 1 || !state.ExpectedUpdatedAt.Equal(now) {
		t.Fatal(state)
	}
	body := func(code, alt string) string {
		raw, _ := json.Marshal(map[string]any{"code": code, "values": map[string]any{"alt": alt}, "expected_updated_at": now})
		return string(raw)
	}
	for _, tc := range []struct {
		code, alt string
		status    int
	}{{"missing", "ok", 422}, {"image", "too long", 422}, {"image", "ok", 200}, {"image", "stale", 409}} {
		response := call("PUT", path, body(tc.code, tc.alt))
		if response.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, response.Code, response.Body.String())
		}
	}
	denied["core.media.update"] = security.ErrForbidden
	if r := call("PUT", path, body("image", "ok")); r.Code != http.StatusForbidden {
		t.Fatal(r.Code)
	}
	denied["core.media.read"] = security.ErrForbidden
	if r := call("GET", path+"?code=image", ""); r.Code != http.StatusForbidden {
		t.Fatal(r.Code)
	}
	delete(denied, "core.media.read")
	sites.policy = extensionTestPolicy{err: security.ErrForbidden}
	if r := call("GET", path+"?code=image", ""); r.Code != http.StatusForbidden {
		t.Fatal("site access ignored", r.Code)
	}
}
