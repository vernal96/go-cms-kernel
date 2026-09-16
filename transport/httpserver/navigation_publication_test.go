package httpserver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/adminui"
	appkernel "github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user/adapters/argon2id"
	"github.com/vernal96/go-cms-kernel/security"
)

type navigationPublicationModule struct{}

func (navigationPublicationModule) Code() kernel.ModuleCode { return "navigation_test" }
func (navigationPublicationModule) Build(_ context.Context, ctx kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	mode, _ := ctx.Scope().Settings()["mode"].(string)
	return navigationPublicationRuntime{mode}, nil
}

type navigationPublicationRuntime struct{ mode string }

func (navigationPublicationRuntime) ModuleCode() kernel.ModuleCode { return "navigation_test" }
func (r navigationPublicationRuntime) AdminNavigation() []adminui.NavigationItem {
	item := adminui.NavigationItem{Code: "navigation_test.page", Label: "Page", Route: "navigation_test.page", Scope: adminui.NavigationSite}
	switch r.mode {
	case "duplicate":
		return []adminui.NavigationItem{item, item}
	case "global":
		item.Code = "sites"
	case "permission":
		item.Permission = "navigation_test.unknown.read"
	}
	return []adminui.NavigationItem{item}
}
func navigationPublicationApp(t *testing.T, mode string) (*appkernel.App, *publicationSiteRepository) {
	t.Helper()
	repository := &publicationSiteRepository{items: []site.Site{{ID: 1, ProfileCode: "dev", Domain: "first.test", Locale: "en-US", IsPublic: true, Settings: map[string]any{"mode": mode}}}}
	a, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger: loggerFactory{}, PasswordHasher: argon2id.Factory{}, SiteAccessPolicy: admin.AllowAllSitesPolicy{}, EventBus: eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{Connector: connectorFactory{}, Adapters: []kernel.ModuleDatabaseFactory{databaseFactory{sites: repository}}},
		Profiles:     []kernel.Profile{{Code: "dev", Modules: []kernel.ProfileModule{{Module: core.Module{}}, {Module: admin.Module{}}, {Module: navigationPublicationModule{}}}, Params: []field.Definition{{Key: "mode", Label: "Mode", Type: field.TypeString}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a, repository
}
func TestNavigationCollisionsFailBootAndPreservePublishedRuntime(t *testing.T) {
	for _, mode := range []string{"duplicate", "global", "permission"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			broken, _ := navigationPublicationApp(t, mode)
			if err := broken.Boot(ctx); err == nil || !strings.Contains(err.Error(), "navigation") {
				t.Fatalf("boot accepted invalid navigation: %v", err)
			}
			a, repository := navigationPublicationApp(t, "")
			if err := a.Boot(ctx); err != nil {
				t.Fatal(err)
			}
			initial, _ := a.Sites().RuntimeByID(1)
			_, err := a.Sites().Update(ctx, security.System(), site.UpdateInput{ID: 1, ProfileCode: "dev", Domain: "changed.test", Locale: "en-US", IsPublic: true, Settings: map[string]any{"mode": mode}})
			if err == nil {
				t.Fatal("update accepted invalid navigation")
			}
			current, _ := a.Sites().RuntimeByID(1)
			if current != initial || repository.updateCalls != 0 {
				t.Fatal("failed candidate changed runtime or storage")
			}
			_, err = a.Sites().Create(ctx, security.System(), site.CreateInput{ProfileCode: "dev", Domain: "new.test", Locale: "en-US", IsPublic: true, Settings: map[string]any{"mode": mode}})
			if err == nil || len(repository.items) != 1 {
				t.Fatal("create accepted invalid navigation")
			}
			repository.items[0].Settings = map[string]any{"mode": mode}
			if err := a.ReloadSites(ctx); err == nil {
				t.Fatal("reload accepted invalid navigation")
			}
			current, _ = a.Sites().RuntimeByID(1)
			if current != initial {
				t.Fatal("failed reload replaced runtime")
			}
		})
	}
}
