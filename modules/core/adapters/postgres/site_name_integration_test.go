package postgres

import (
	"fmt"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/site"
	sitepostgres "github.com/vernal96/go-cms-kernel/modules/core/site/adapters/postgres"
)

func TestPostgresSiteNamePersistenceAndSearch(t *testing.T) {
	connector, _, ctx := openOutboxIntegrationDatabase(t)
	repository, err := sitepostgres.NewRepository(connector)
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	first, err := repository.Create(ctx, nil, site.Site{
		ProfileCode: "dev", Name: "Первый портал", Domain: fmt.Sprintf("site-name-%d-a.test", suffix),
		Locale: "ru-RU", Settings: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = connector.Pool().Exec(ctx, `DELETE FROM core.sites WHERE id = $1`, first.ID) })
	second, err := repository.Create(ctx, nil, site.Site{
		ProfileCode: "dev", Name: "Первый портал", Domain: fmt.Sprintf("site-name-%d-b.test", suffix),
		Locale: "ru-RU", Settings: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = connector.Pool().Exec(ctx, `DELETE FROM core.sites WHERE id = $1`, second.ID) })
	if first.Name != "Первый портал" || second.Name != first.Name {
		t.Fatalf("created names = %q, %q", first.Name, second.Name)
	}
	loaded, err := repository.FindByID(ctx, first.ID)
	if err != nil || loaded.Name != first.Name {
		t.Fatalf("loaded site = %#v, %v", loaded, err)
	}

	for _, search := range []string{"ПЕРВЫЙ", fmt.Sprintf("site-name-%d-a", suffix)} {
		page, err := repository.ListPage(ctx, site.ListQuery{
			Search: search, Page: 1, PerPage: 10, Scope: site.Scope{All: true},
		})
		if err != nil || page.Total == 0 {
			t.Fatalf("search %q = %#v, %v", search, page, err)
		}
	}

	first.Name = "Обновлённый портал"
	updated, err := repository.Update(ctx, nil, first)
	if err != nil || updated.Name != first.Name {
		t.Fatalf("updated site = %#v, %v", updated, err)
	}
	if _, err := connector.Pool().Exec(ctx, `INSERT INTO core.sites (profile_code, name, domain) VALUES ('dev', '  ', $1)`, fmt.Sprintf("site-name-%d-empty.test", suffix)); err == nil {
		t.Fatal("database accepted an empty site name")
	}
}
