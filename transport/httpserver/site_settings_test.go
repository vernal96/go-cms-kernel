package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	appkernel "github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user/adapters/argon2id"
	"github.com/vernal96/go-cms-kernel/security"
	httpserver "github.com/vernal96/go-cms-kernel/transport/httpserver"
)

func TestSiteSettingsHTTPPrivacyAndUpdates(t *testing.T) {
	ctx := context.Background()
	repo := &publicationSiteRepository{items: []site.Site{
		{ID: 1, ProfileCode: "dev", Domain: "first.test", Locale: "en-US", IsPublic: true, Settings: map[string]any{"name": "First", "secret": "hidden"}},
		{ID: 2, ProfileCode: "dev", Domain: "second.test", Locale: "en-US", IsPublic: true, Settings: map[string]any{"name": "Second", "secret": "other"}},
		{ID: 3, ProfileCode: "dev", Domain: "empty.test", Locale: "en-US", IsPublic: true, Settings: map[string]any{"secret": "hidden"}},
		{ID: 4, ProfileCode: "dev", Domain: "private.test", Locale: "en-US", IsPublic: false},
	}}
	application, err := appkernel.New(ctx, appkernel.Definition{
		Logger: loggerFactory{}, PasswordHasher: argon2id.Factory{}, SiteAccessPolicy: admin.AllowAllSitesPolicy{}, EventBus: eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{Connector: connectorFactory{}, Adapters: []kernel.ModuleDatabaseFactory{databaseFactory{sites: repo, access: privilegedUserAccessRepository{}}}},
		Profiles: []kernel.Profile{{Code: "dev", Modules: []kernel.ProfileModule{{Module: core.Module{}}, {Module: admin.Module{}}}, Params: []field.Definition{
			{Key: "name", Label: "Name", Type: field.TypeString, Public: true},
			{Key: "secret", Label: "Secret", Type: field.TypeString},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	if err := application.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	handler, err := newTestHandler(application, httpserver.WithAccessTokens(staticAccessTokens{verified: security.User(1)}))
	if err != nil {
		t.Fatal(err)
	}
	request := func(host, path string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "http://"+host+path, nil)
		if authenticated {
			req.Header.Set("Authorization", "Bearer admin")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	check := func(host, name string) {
		t.Helper()
		for _, path := range []string{"/site", "/_cms/runtime"} {
			for _, authenticated := range []bool{false, true} {
				res := request(host, path, authenticated)
				if res.Code != 200 || res.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("%s %s: %d %s", host, path, res.Code, res.Body.String())
				}
				var data struct {
					Settings map[string]any `json:"settings"`
				}
				if err := json.Unmarshal(res.Body.Bytes(), &data); err != nil {
					t.Fatal(err)
				}
				want := map[string]any{}
				if name != "" {
					want["name"] = name
				}
				if !reflect.DeepEqual(data.Settings, want) {
					t.Fatalf("%s: %#v", path, data.Settings)
				}
			}
		}
	}
	check("first.test", "First")
	check("second.test", "Second")
	check("empty.test", "")
	for _, path := range []string{"/site?unknown=1", "/site?x=1&x=2", "/site?%zz"} {
		if res := request("first.test", path, false); res.Code != 400 {
			t.Fatalf("query %s: %d", path, res.Code)
		}
	}
	for _, path := range []string{"/site", "/_cms/runtime"} {
		if res := request("missing.test", path, false); res.Code != 404 {
			t.Fatalf("unknown site: %d", res.Code)
		}
		if res := request("private.test", path, false); res.Code != 403 {
			t.Fatalf("private site: %d", res.Code)
		}
	}
	_, err = application.Sites().Update(ctx, security.System(), site.UpdateInput{ID: 1, ProfileCode: "dev", Domain: "first.test", Locale: "en-US", IsPublic: true, Settings: map[string]any{"name": "Updated", "secret": "still hidden"}})
	if err != nil {
		t.Fatal(err)
	}
	check("first.test", "Updated")
	check("second.test", "Second")
}
