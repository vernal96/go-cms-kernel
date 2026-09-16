package postgres

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

func TestPostgresGroupSiteAccessUnionReplacementAndCascades(t *testing.T) {
	host := os.Getenv("CMS_TEST_POSTGRES_HOST")
	if host == "" {
		t.Skip("set CMS_TEST_POSTGRES_HOST to run the PostgreSQL integration test")
	}
	port := 5432
	if value := os.Getenv("CMS_TEST_POSTGRES_PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("parse CMS_TEST_POSTGRES_PORT: %v", err)
		}
		port = parsed
	}
	sslMode := os.Getenv("CMS_TEST_POSTGRES_SSL_MODE")
	if sslMode == "" {
		sslMode = "disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connector, err := connectorpostgres.New(ctx, connectorpostgres.Config{
		Code:            kernel.ConnectionCode("group-site-access-integration"),
		Host:            host,
		Port:            port,
		Database:        os.Getenv("CMS_TEST_POSTGRES_DB"),
		User:            os.Getenv("CMS_TEST_POSTGRES_USER"),
		Password:        os.Getenv("CMS_TEST_POSTGRES_PASSWORD"),
		SSLMode:         sslMode,
		MaxConns:        4,
		ConnectTimeout:  5 * time.Second,
		ConnMaxLifetime: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connector.Close() })
	database, err := NewDatabase(connector)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewManager().Up(ctx, migrations.Plan{
		Connection: string(connector.Code()), Target: connector, Source: database.MigrationSources()[0],
	}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	cleanup := func(cleanupCtx context.Context) {
		_, _ = connector.Pool().Exec(cleanupCtx, `
DELETE FROM core.users WHERE login = 'site-access-user';
DELETE FROM core.groups WHERE code IN ('site-access-one', 'site-access-two');
DELETE FROM core.sites WHERE domain IN ('site-access-one.test', 'site-access-two.test');
`)
	}
	cleanup(ctx)
	t.Cleanup(func() { cleanup(context.Background()) })

	var siteOne, siteTwo site.ID
	if err := connector.Pool().QueryRow(ctx, `
INSERT INTO core.sites (profile_code, domain, locale, settings)
VALUES ('dev', 'site-access-one.test', 'ru-RU', '{}') RETURNING id;
`).Scan(&siteOne); err != nil {
		t.Fatal(err)
	}
	if err := connector.Pool().QueryRow(ctx, `
INSERT INTO core.sites (profile_code, domain, locale, settings)
VALUES ('dev', 'site-access-two.test', 'ru-RU', '{}') RETURNING id;
`).Scan(&siteTwo); err != nil {
		t.Fatal(err)
	}
	groups := database.Groups()
	first, err := groups.Create(ctx, nil, group.Group{Code: "site-access-one", Name: "Site access one"}, nil, []group.SiteAccess{{SiteID: siteOne, CanView: true, CanEdit: true}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := groups.Create(ctx, nil, group.Group{Code: "site-access-two", Name: "Site access two"}, nil, []group.SiteAccess{
		{SiteID: siteOne, CanView: true, CanEdit: true, CanDelete: true},
		{SiteID: siteTwo, CanView: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var userID security.UserID
	if err := connector.Pool().QueryRow(ctx, `
INSERT INTO core.users (login, email, password_hash, name)
VALUES ('site-access-user', 'site-access-user@example.test', 'hash', 'Site Access User')
RETURNING id;
`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := connector.Pool().Exec(ctx, `
INSERT INTO core.user_groups (user_id, group_id)
VALUES ($1, $2), ($1, $3);
`, userID, first.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	viewIDs, err := groups.EffectiveSiteIDs(ctx, userID, group.SiteAccessView)
	if err != nil || len(viewIDs) != 2 {
		t.Fatalf("view ids = %#v, %v", viewIDs, err)
	}
	allowed, err := groups.UserHasSiteAccess(ctx, userID, siteOne, group.SiteAccessDelete)
	if err != nil || !allowed {
		t.Fatalf("combined delete = %t, %v", allowed, err)
	}

	current, err := groups.ByID(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement := []group.SiteAccess{{SiteID: siteTwo, CanView: true, CanEdit: true}}
	if _, err := groups.Update(ctx, nil, current, nil, &replacement); err != nil {
		t.Fatal(err)
	}
	firstGrants, err := groups.SiteAccesses(ctx, first.ID)
	if err != nil || len(firstGrants) != 1 || firstGrants[0].SiteID != siteTwo {
		t.Fatalf("replacement = %#v, %v", firstGrants, err)
	}
	if err := groups.Delete(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	assertGroupSiteAccessCount(t, ctx, connector, "group_id = $1", second.ID, 0)
	if _, err := connector.Pool().Exec(ctx, `DELETE FROM core.sites WHERE id = $1;`, siteTwo); err != nil {
		t.Fatal(err)
	}
	assertGroupSiteAccessCount(t, ctx, connector, "site_id = $1", siteTwo, 0)
}

func assertGroupSiteAccessCount(t *testing.T, ctx context.Context, connector *connectorpostgres.Connector, predicate string, value any, want int) {
	t.Helper()
	var count int
	if err := connector.Pool().QueryRow(ctx, `SELECT count(*) FROM core.group_site_access WHERE `+predicate+`;`, value).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("group site access count = %d, want %d", count, want)
	}
}
