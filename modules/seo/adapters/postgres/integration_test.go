package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/migrations"
	corepostgres "github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/seo"
)

func TestPostgresMigrationsRepositoryScopeAndCascade(t *testing.T) {
	host := os.Getenv("CMS_TEST_SEO_POSTGRES_HOST")
	if host == "" {
		t.Skip("set CMS_TEST_SEO_POSTGRES_HOST to run the SEO PostgreSQL integration test")
	}
	port := 5432
	if value := os.Getenv("CMS_TEST_SEO_POSTGRES_PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("parse CMS_TEST_SEO_POSTGRES_PORT: %v", err)
		}
		port = parsed
	}
	sslMode := os.Getenv("CMS_TEST_SEO_POSTGRES_SSL_MODE")
	if sslMode == "" {
		sslMode = "disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	connector, err := connectorpostgres.New(ctx, connectorpostgres.Config{
		Code:            kernel.ConnectionCode("seo-integration"),
		Host:            host,
		Port:            port,
		Database:        os.Getenv("CMS_TEST_SEO_POSTGRES_DB"),
		User:            os.Getenv("CMS_TEST_SEO_POSTGRES_USER"),
		Password:        os.Getenv("CMS_TEST_SEO_POSTGRES_PASSWORD"),
		SSLMode:         sslMode,
		MaxConns:        4,
		ConnectTimeout:  5 * time.Second,
		ConnMaxLifetime: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connector.Close() })
	coreDatabase, err := corepostgres.NewDatabase(connector)
	if err != nil {
		t.Fatal(err)
	}
	database, err := NewDatabase(connector)
	if err != nil {
		t.Fatal(err)
	}
	manager := migrations.NewManager()
	corePlan := migrations.Plan{
		Connection: string(connector.Code()), Target: connector,
		Source: coreDatabase.MigrationSources()[0],
	}
	seoPlan := migrations.Plan{
		Connection: string(connector.Code()), Target: connector,
		Source: database.MigrationSources()[0],
	}
	if err := manager.Up(ctx, corePlan); err != nil {
		t.Fatalf("core migrations up: %v", err)
	}
	if err := manager.Up(ctx, seoPlan); err != nil {
		t.Fatalf("SEO migration up: %v", err)
	}
	if err := manager.Down(ctx, seoPlan, 1); err != nil {
		t.Fatalf("SEO migration down: %v", err)
	}
	var table *string
	if err := connector.Pool().QueryRow(
		ctx,
		`SELECT to_regclass('seo.resource_metadata')::text;`,
	).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table == nil || *table != "seo.resource_metadata" {
		t.Fatalf("table after identity migration down = %#v", table)
	}
	if err := manager.Up(ctx, seoPlan); err != nil {
		t.Fatalf("SEO migration restore: %v", err)
	}

	suffix := time.Now().UnixNano()
	var firstSiteID, secondSiteID site.ID
	for index, target := range []*site.ID{&firstSiteID, &secondSiteID} {
		err := connector.Pool().QueryRow(ctx, `
INSERT INTO core.sites (profile_code, domain, locale, settings, is_public)
VALUES ('seo-test', $1, 'ru-RU', '{}'::jsonb, true)
RETURNING id;
`, fmt.Sprintf("seo-%d-%d.example.test", suffix, index)).Scan(target)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = connector.Pool().Exec(
			context.Background(),
			`DELETE FROM core.sites WHERE id = ANY($1::bigint[]);`,
			[]int64{int64(firstSiteID), int64(secondSiteID)},
		)
	})
	var resourceID resource.ID
	if err := connector.Pool().QueryRow(ctx, `
WITH entity AS (
    INSERT INTO core.resource_entities (site_id, storage_kind)
    VALUES ($1, 'tree')
    RETURNING id
)
INSERT INTO core.resources (id, site_id, type, title, slug, path, type_settings)
VALUES ((SELECT id FROM entity), $1, 'page', 'Page', '', '/', '{}'::jsonb)
RETURNING id;
`, firstSiteID).Scan(&resourceID); err != nil {
		t.Fatal(err)
	}
	repository := database.ResourceMetadata()
	stored, err := repository.Save(ctx, seo.Metadata{
		ResourceID:          resourceID,
		SiteID:              firstSiteID,
		TitleTemplate:       "{{ resource.title }}",
		DescriptionTemplate: "{{ resource.annotation }}",
		RobotsIndex:         true,
		RobotsFollow:        true,
	})
	if err != nil || stored.ResourceID != resourceID || stored.SiteID != firstSiteID {
		t.Fatalf("save = %#v, %v", stored, err)
	}
	used, err := repository.UsedByResources(ctx, firstSiteID, []resource.ID{resourceID})
	if err != nil || !used {
		t.Fatalf("SEO usage = %t, %v", used, err)
	}
	used, err = repository.UsedByResources(ctx, secondSiteID, []resource.ID{resourceID})
	if err != nil || used {
		t.Fatalf("cross-site SEO usage = %t, %v", used, err)
	}
	if _, err := repository.ByResource(ctx, secondSiteID, resourceID); !errors.Is(err, seo.ErrNotFound) {
		t.Fatalf("cross-site read error = %v", err)
	}
	if _, err := repository.Save(ctx, seo.Metadata{
		ResourceID:    resourceID,
		SiteID:        secondSiteID,
		TitleTemplate: "invalid",
	}); !errors.Is(err, seo.ErrInvalidReference) {
		t.Fatalf("cross-site save error = %v", err)
	}
	if _, err := connector.Pool().Exec(ctx, `UPDATE core.resource_entities SET site_id=$2 WHERE id=$1;`, resourceID, secondSiteID); err != nil {
		t.Fatalf("cascade resource site ownership: %v", err)
	}
	if moved, err := repository.ByResource(ctx, secondSiteID, resourceID); err != nil || moved.SiteID != secondSiteID {
		t.Fatalf("moved SEO metadata = %#v, %v", moved, err)
	}
	if _, err := repository.ByResource(ctx, firstSiteID, resourceID); !errors.Is(err, seo.ErrNotFound) {
		t.Fatalf("old-site SEO metadata error = %v", err)
	}
	if _, err := connector.Pool().Exec(
		ctx,
		`DELETE FROM core.resource_entities WHERE id = $1;`,
		resourceID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ByResource(ctx, secondSiteID, resourceID); !errors.Is(err, seo.ErrNotFound) {
		t.Fatalf("metadata after permanent resource delete = %v", err)
	}
}
