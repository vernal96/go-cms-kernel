package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/adminui"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type navigationTestModule struct {
	items []adminui.NavigationItem
}

func (navigationTestModule) Code() kernel.ModuleCode { return "forms" }

func (m navigationTestModule) Build(
	context.Context,
	kernel.ModuleContext,
) (kernel.ModuleRuntime, error) {
	return navigationTestRuntime{items: m.items}, nil
}

type navigationTestRuntime struct {
	items []adminui.NavigationItem
}

type navigationAdministrator bool

func (a navigationAdministrator) IsAdministrator(context.Context, security.Actor) (bool, error) {
	return bool(a), nil
}

func TestNavigationComposerRequiresAdministratorVisibility(t *testing.T) {
	t.Parallel()
	catalog := corePermissionCatalog(t)
	profiles := []kernel.Profile{{Code: "dev", Modules: []kernel.ProfileModule{{Module: core.Module{}}}}}
	for _, test := range []struct {
		name    string
		admin   navigationAdministrator
		visible bool
	}{{"ordinary user", false, false}, {"built-in administrator", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			composer, err := newNavigationComposer(profiles, managementAuthorizer{denied: map[permission.Code]error{}}, catalog, test.admin)
			if err != nil {
				t.Fatal(err)
			}
			items, err := composer.compose(context.Background(), security.User(1), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := navigationContains(items, "administration"); got != test.visible {
				t.Fatalf("administration visible = %v, want %v: %#v", got, test.visible, items)
			}
		})
	}
}

func (navigationTestRuntime) ModuleCode() kernel.ModuleCode { return "forms" }
func (r navigationTestRuntime) AdminNavigation() []adminui.NavigationItem {
	return r.items
}

func TestNavigationComposerFiltersPermissionsAndEmptyParents(t *testing.T) {
	t.Parallel()

	catalog := corePermissionCatalog(t)
	composer, err := newNavigationComposer(
		[]kernel.Profile{{
			Code:    "dev",
			Modules: []kernel.ProfileModule{{Module: core.Module{}}},
		}},
		managementAuthorizer{denied: map[permission.Code]error{
			FileReadPermission:  security.ErrForbidden,
			UserReadPermission:  security.ErrForbidden,
			GroupReadPermission: security.ErrForbidden,
		}},
		catalog,
	)
	if err != nil {
		t.Fatal(err)
	}

	items, err := composer.compose(
		context.Background(),
		security.User(1),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Code != "sites" || items[0].Scope != adminui.NavigationGlobal {
		t.Fatalf("visible navigation = %#v", items)
	}
}

func TestNavigationComposerUsesOnlySelectedSiteRuntimeProviders(t *testing.T) {
	t.Parallel()

	catalog := corePermissionCatalog(t)
	composer, err := newNavigationComposer(
		[]kernel.Profile{{
			Code:    "global",
			Modules: []kernel.ProfileModule{{Module: core.Module{}}},
		}},
		managementAuthorizer{denied: map[permission.Code]error{}},
		catalog,
	)
	if err != nil {
		t.Fatal(err)
	}

	forms := []adminui.NavigationItem{{
		Code:  "forms",
		Label: "Формы",
		Icon:  "forms",
		Order: 400,
		Scope: adminui.NavigationSite,
		Children: []adminui.NavigationItem{{
			Code:       "forms.list",
			Label:      "Формы",
			Route:      "forms.list",
			Order:      100,
			Permission: SiteReadPermission,
			Scope:      adminui.NavigationSite,
		}},
	}}
	withForms := navigationSiteRuntime(t, 7, forms)
	withoutForms := navigationSiteRuntime(t, 8, nil)

	global, err := composer.compose(context.Background(), security.User(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if navigationContains(global, "forms") {
		t.Fatalf("global navigation contains site item: %#v", global)
	}
	selected, err := composer.compose(context.Background(), security.User(1), withForms)
	if err != nil {
		t.Fatal(err)
	}
	if !navigationContains(selected, "forms") || !navigationContains(selected, "forms.list") {
		t.Fatalf("selected navigation = %#v", selected)
	}
	unselected, err := composer.compose(context.Background(), security.User(1), withoutForms)
	if err != nil {
		t.Fatal(err)
	}
	if navigationContains(unselected, "forms") {
		t.Fatalf("navigation without module = %#v", unselected)
	}
}

func TestManagementHTTPNavigationSupportsOptionalSelectedSite(t *testing.T) {
	t.Parallel()

	catalog := corePermissionCatalog(t)
	composer, err := newNavigationComposer(
		[]kernel.Profile{{Code: "global", Modules: []kernel.ProfileModule{{Module: core.Module{}}}}},
		managementAuthorizer{denied: map[permission.Code]error{}},
		catalog,
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := navigationSiteRuntime(t, 7, []adminui.NavigationItem{{
		Code: "forms", Label: "Формы", Route: "forms.list", Icon: "forms",
		Order: 400, Permission: SiteReadPermission, Scope: adminui.NavigationSite,
	}})
	management := &Management{
		navigation: composer,
		authorizer: managementAuthorizer{denied: map[permission.Code]error{}},
		policy:     scopedPolicy{scope: SiteAccessScope{All: true}},
		sites:      extensionTestSites{runtime: runtime},
	}
	router := chi.NewRouter()
	registerManagementRoutes(router, management)

	request := httptest.NewRequest(http.MethodGet, "/navigation?site_id=7", nil)
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result Navigation
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 4 || result.Items[3].Code != "forms" || result.Items[3].Route != "forms.list" {
		t.Fatalf("response = %#v", result)
	}

	invalid := httptest.NewRecorder()
	router.ServeHTTP(
		invalid,
		httptest.NewRequest(http.MethodGet, "/navigation?site_id=invalid", nil),
	)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, body = %s", invalid.Code, invalid.Body.String())
	}
}

func navigationSiteRuntime(
	t *testing.T,
	id site.ID,
	items []adminui.NavigationItem,
) *site.Runtime {
	t.Helper()
	modules := []kernel.ProfileModule(nil)
	if items != nil {
		modules = append(modules, kernel.ProfileModule{
			Module: navigationTestModule{items: items},
		})
	}
	profile := kernel.Profile{
		Code:    kernel.ProfileCode("site-" + strconv.FormatInt(int64(id), 10)),
		Modules: modules,
	}
	factory, err := kernel.NewProfileRuntimeFactory(
		extensionTestDatabaseResolver{},
		kernel.RuntimeServices{
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			EventBus: extensionTestBus{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := site.NewRuntimeFromBlueprint(
		context.Background(),
		site.Site{
			ID: id, ProfileCode: profile.Code, Domain: "example.test",
			Locale: "ru-RU", Settings: map[string]any{},
		},
		blueprint,
	)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func corePermissionCatalog(t *testing.T) *permission.Catalog {
	t.Helper()
	definitions, err := permission.Definitions(
		string(core.ModuleCode),
		core.Module{}.Registry().PermissionEntities,
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := permission.NewCatalog(definitions)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func navigationContains(items []adminui.NavigationItem, code string) bool {
	for _, item := range items {
		if item.Code == code || navigationContains(item.Children, code) {
			return true
		}
	}
	return false
}
