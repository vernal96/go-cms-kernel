package postgres

import (
	"fmt"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	sitepostgres "github.com/vernal96/go-cms-kernel/modules/core/site/adapters/postgres"
)

func TestPostgresResourceBreadcrumbCandidatesFollowAncestryAndHomePolicy(t *testing.T) {
	connector, database, ctx := openOutboxIntegrationDatabase(t)
	sites, err := sitepostgres.NewRepository(connector)
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	siteItem, err := sites.Create(ctx, nil, site.Site{
		ProfileCode: "dev",
		Name:        "Breadcrumb integration",
		Domain:      fmt.Sprintf("breadcrumbs-%d.test", suffix),
		Locale:      "en-US",
		Settings:    map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = connector.Pool().Exec(ctx, `DELETE FROM core.sites WHERE id = $1`, siteItem.ID) })

	resources := database.Resources()
	create := func(parentID *resource.ID, slug, path, title, menuTitle string, public, inMenu bool, publishedAt *time.Time) resource.Resource {
		t.Helper()
		item, err := resources.Create(ctx, nil, resource.Resource{
			SiteID: siteItem.ID, ParentID: parentID, Type: resourcetype.Page,
			Title: title, MenuTitle: menuTitle, Slug: slug, Path: &path,
			IsPublic: public, IsSearchable: true, InMenu: inMenu, InSitemap: true,
			PublishedAt: publishedAt,
		}, nil)
		if err != nil {
			t.Fatalf("create resource %q: %v", title, err)
		}
		return item
	}

	home := create(nil, "home", "/", "Home", "Главная", true, true, nil)
	news := create(nil, "news", "/news", "News", "Новости", true, false, nil)
	section := create(&news.ID, "section", "/news/section", "Section", "Раздел", true, true, nil)
	hidden := create(&section.ID, "hidden", "/news/section/hidden", "Hidden", "Скрытый", false, true, nil)
	deleted := create(&hidden.ID, "deleted", "/news/section/hidden/deleted", "Deleted", "Удалённый", true, true, nil)
	future := time.Now().UTC().Add(time.Hour)
	scheduled := create(&deleted.ID, "scheduled", "/news/section/hidden/deleted/scheduled", "Scheduled", "Запланированный", true, true, &future)
	current := create(&scheduled.ID, "article", "/news/section/hidden/deleted/scheduled/article", "Article", "Статья", true, true, nil)
	if _, err := connector.Pool().Exec(ctx, `UPDATE core.resources SET deleted_at = now() WHERE id = $1 AND site_id = $2`, deleted.ID, siteItem.ID); err != nil {
		t.Fatal(err)
	}

	repository, ok := resources.(resource.BreadcrumbRepository)
	if !ok {
		t.Fatal("PostgreSQL resources repository does not expose breadcrumb candidates")
	}
	withHome, err := repository.BreadcrumbCandidates(ctx, siteItem.ID, current.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	withoutHome, err := repository.BreadcrumbCandidates(ctx, siteItem.ID, current.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := candidateIDs(withHome), []resource.ID{home.ID, news.ID, section.ID, hidden.ID, deleted.ID, scheduled.ID, current.ID}; !equalResourceIDs(got, want) {
		t.Fatalf("include-home candidates = %v, want %v", got, want)
	}
	if got, want := candidateIDs(withoutHome), []resource.ID{news.ID, section.ID, hidden.ID, deleted.ID, scheduled.ID, current.ID}; !equalResourceIDs(got, want) {
		t.Fatalf("without-home candidates = %v, want %v", got, want)
	}
	visible := resource.VisibleBreadcrumbs(withHome, time.Now().UTC())
	if got, want := candidateIDs(visible), []resource.ID{home.ID, news.ID, section.ID, current.ID}; !equalResourceIDs(got, want) {
		t.Fatalf("visible include-home chain = %v, want %v", got, want)
	}
	visibleAfterSchedule := resource.VisibleBreadcrumbs(withHome, future.Add(time.Second))
	if got, want := candidateIDs(visibleAfterSchedule), []resource.ID{home.ID, news.ID, section.ID, scheduled.ID, current.ID}; !equalResourceIDs(got, want) {
		t.Fatalf("visible chain after scheduled publication = %v, want %v", got, want)
	}

	homeChild := create(&home.ID, "about", "/about", "About", "О сайте", true, true, nil)
	withHomeAncestor, err := repository.BreadcrumbCandidates(ctx, siteItem.ID, homeChild.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := candidateIDs(withHomeAncestor), []resource.ID{home.ID, homeChild.ID}; !equalResourceIDs(got, want) {
		t.Fatalf("home ancestor was duplicated: got %v, want %v", got, want)
	}
	withoutHomeAncestor, err := repository.BreadcrumbCandidates(ctx, siteItem.ID, homeChild.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := candidateIDs(withoutHomeAncestor), []resource.ID{home.ID, homeChild.ID}; !equalResourceIDs(got, want) {
		t.Fatalf("home ancestor was omitted with includeHome=false: got %v, want %v", got, want)
	}
}

func candidateIDs(candidates []resource.BreadcrumbCandidate) []resource.ID {
	ids := make([]resource.ID, len(candidates))
	for index, candidate := range candidates {
		ids[index] = candidate.ID
	}
	return ids
}

func equalResourceIDs(left, right []resource.ID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
