package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/connectors/pgtrgm"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/migrations"
	corepostgres "github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/search"
)

type fixture struct {
	ctx          context.Context
	connector    *connectorpostgres.Connector
	engine       *Engine
	resources    resource.Repository
	libraryItems resource.LibraryItemRepository
	sites        [2]site.ID
	seq          int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	host := os.Getenv("CMS_TEST_SEARCH_POSTGRES_HOST")
	if host == "" {
		t.Skip("set CMS_TEST_SEARCH_POSTGRES_HOST to run PostgreSQL search integration tests")
	}
	port := 5432
	if raw := os.Getenv("CMS_TEST_SEARCH_POSTGRES_PORT"); raw != "" {
		var err error
		port, err = strconv.Atoi(raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	connector, err := connectorpostgres.New(ctx, connectorpostgres.Config{Code: "search-test", Host: host, Port: port,
		Database: os.Getenv("CMS_TEST_SEARCH_POSTGRES_DB"), User: os.Getenv("CMS_TEST_SEARCH_POSTGRES_USER"), Password: os.Getenv("CMS_TEST_SEARCH_POSTGRES_PASSWORD"),
		SSLMode: "disable", MaxConns: 4, ConnectTimeout: 5 * time.Second, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connector.Close() })
	database, err := corepostgres.NewDatabase(connector)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewManager().Up(ctx, migrations.Plan{Connection: string(connector.Code()), Target: connector, Source: database.MigrationSources()[0]}); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(connector)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{ctx: ctx, connector: connector, engine: engine, resources: database.Resources(), libraryItems: database.Resources().(resource.LibraryItemRepository)}
	for index := range f.sites {
		if err := connector.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,domain,locale,settings,is_public) VALUES('search-test',$1,'ru-RU','{}',true) RETURNING id`, fmt.Sprintf("search-%d-%d.example.test", time.Now().UnixNano(), index)).Scan(&f.sites[index]); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := connector.Pool().Exec(cleanup, `DELETE FROM core.sites WHERE id=ANY($1::bigint[])`, []int64{int64(f.sites[0]), int64(f.sites[1])}); err != nil {
			t.Errorf("cleanup search sites: %v", err)
		}
	})
	return f
}

func (f *fixture) create(t *testing.T, siteID site.ID, title string, change func(*resource.Resource)) resource.Resource {
	t.Helper()
	f.seq++
	slug := fmt.Sprintf("entry-%d", f.seq)
	path := "/" + slug
	item := resource.Resource{SiteID: siteID, Type: resourcetype.Page, Title: title, Slug: slug, Path: &path, IsPublic: true, IsSearchable: true, Fields: map[string]any{}}
	if change != nil {
		change(&item)
	}
	stored, err := f.resources.Create(f.ctx, nil, item, nil)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func (f *fixture) createItem(t *testing.T, library resource.Resource, title string, change func(*resource.LibraryItem)) resource.LibraryItem {
	t.Helper()
	f.seq++
	item := resource.LibraryItem{SiteID: library.SiteID, LibraryID: library.ID, Title: title, Slug: fmt.Sprintf("item-%d", f.seq), IsPublic: true, IsSearchable: true, Fields: map[string]any{}}
	if change != nil {
		change(&item)
	}
	stored, err := f.libraryItems.CreateLibraryItem(f.ctx, nil, item, false)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func (f *fixture) find(t *testing.T, siteID site.ID, text string, page, perPage int) search.Page {
	t.Helper()
	result, err := f.engine.Search(f.ctx, search.Query{SiteID: siteID, RouteTypes: []resourcetype.Code{resourcetype.Page, resourcetype.Library, resourcetype.Link, resourcetype.ResourceLink}, Input: search.Input{Text: text, Page: page, PerPage: perPage}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPostgresSearchVisibilityAndIsolation(t *testing.T) {
	f := newFixture(t)
	var validIndexes bool
	var leafIndexes int
	if err := f.connector.Pool().QueryRow(f.ctx, `SELECT bool_and(i.indisvalid), count(*) FILTER(WHERE p.isleaf) FROM pg_partition_tree('core.idx_library_items_search_text') p JOIN pg_index i ON i.indexrelid=p.relid`).Scan(&validIndexes, &leafIndexes); err != nil {
		t.Fatal(err)
	}
	if !validIndexes || leafIndexes != 104 {
		t.Fatalf("search indexes valid=%t leaves=%d", validIndexes, leafIndexes)
	}
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	valid := f.create(t, f.sites[0], "visibilitytoken", nil)
	f.create(t, f.sites[1], "visibilitytoken", nil)
	for _, change := range []func(*resource.Resource){
		func(r *resource.Resource) { r.IsPublic = false },
		func(r *resource.Resource) { r.IsSearchable = false },
		func(r *resource.Resource) { r.PublishedAt = &future },
		func(r *resource.Resource) { r.UnpublishedAt = &past },
		func(r *resource.Resource) { r.Path = nil },
		func(r *resource.Resource) { r.Type = "unregistered" },
	} {
		f.create(t, f.sites[0], "visibilitytoken", change)
	}
	deleted := f.create(t, f.sites[0], "visibilitytoken", nil)
	if err := f.resources.(resource.LifecycleRepository).SoftDelete(f.ctx, nil, deleted.ID); err != nil {
		t.Fatal(err)
	}
	library := f.create(t, f.sites[0], "Catalog", func(r *resource.Resource) {
		r.Type = resourcetype.Library
		r.IsSearchable = false
		r.TypeSettings = map[string]any{"item_url_pattern": "/{year}/{month}/{slug}"}
	})
	item := f.createItem(t, library, "visibilitytoken", func(i *resource.LibraryItem) { i.PublishedAt = &past })
	for _, change := range []func(*resource.LibraryItem){
		func(i *resource.LibraryItem) { i.IsPublic = false },
		func(i *resource.LibraryItem) { i.IsSearchable = false },
		func(i *resource.LibraryItem) { i.PublishedAt = &future },
		func(i *resource.LibraryItem) { i.UnpublishedAt = &past },
	} {
		f.createItem(t, library, "visibilitytoken", change)
	}
	deletedItem := f.createItem(t, library, "visibilitytoken", nil)
	if err := f.libraryItems.SoftDeleteLibraryItem(f.ctx, nil, deletedItem.ID); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*resource.Resource){
		func(r *resource.Resource) { r.IsPublic = false },
		func(r *resource.Resource) { r.PublishedAt = &future },
		func(r *resource.Resource) { r.UnpublishedAt = &past },
	} {
		parent := f.create(t, f.sites[0], "Catalog", func(r *resource.Resource) { r.Type = resourcetype.Library; change(r) })
		f.createItem(t, parent, "visibilitytoken", nil)
	}
	removedLibrary := f.create(t, f.sites[0], "Catalog", func(r *resource.Resource) { r.Type = resourcetype.Library })
	f.createItem(t, removedLibrary, "visibilitytoken", nil)
	if err := f.resources.(resource.LifecycleRepository).SoftDelete(f.ctx, nil, removedLibrary.ID); err != nil {
		t.Fatal(err)
	}
	result := f.find(t, f.sites[0], "visibilitytoken", 1, 1)
	if result.Pagination.Total != 2 || len(result.Items) != 1 || result.Items[0].ID != valid.ID {
		t.Fatalf("first page: %#v", result)
	}
	result = f.find(t, f.sites[0], "visibilitytoken", 2, 1)
	expectedURL, err := resource.EffectiveLibraryItemURL(library, item)
	if err != nil {
		t.Fatal(err)
	}
	if result.Pagination.Total != 2 || len(result.Items) != 1 || result.Items[0].ID != item.ID || result.Items[0].URL != expectedURL {
		t.Fatalf("item page: %#v; url %q", result, expectedURL)
	}
	if result := f.find(t, f.sites[0], "visibilitytoken", 9, 1); result.Pagination.Total != 2 || len(result.Items) != 0 {
		t.Fatalf("past last page: %#v", result)
	}
	if result := f.find(t, f.sites[1], "visibilitytoken", 1, 20); result.Pagination.Total != 1 {
		t.Fatalf("other site: %#v", result)
	}
}

func TestPostgresSearchRankingAndMutationLifecycle(t *testing.T) {
	f := newFixture(t)
	content := f.create(t, f.sites[0], "Article", func(r *resource.Resource) {
		r.Content = strings.Repeat("длинная статья ", 1000) + "электричество"
	})
	exact := f.create(t, f.sites[0], "Электричество", nil)
	title := f.create(t, f.sites[0], "Про электричество", nil)
	annotation := f.create(t, f.sites[0], "Another article", func(r *resource.Resource) { r.Annotation = "электричество" })
	for _, text := range []string{"ЭЛЕКТРИЧЕСТВО", "электричетсво"} {
		result := f.find(t, f.sites[0], text, 1, 20)
		if result.Pagination.Total != 4 {
			t.Fatalf("query %q: %#v", text, result)
		}
		if text == "ЭЛЕКТРИЧЕСТВО" {
			want := []resource.ID{exact.ID, title.ID, annotation.ID, content.ID}
			for index, id := range want {
				if result.Items[index].ID != id {
					t.Fatalf("ranking: %#v", result)
				}
			}
		}
	}
	f.create(t, f.sites[0], "Simple English phrase", nil)
	if got := f.find(t, f.sites[0], "english", 1, 20); got.Pagination.Total != 1 {
		t.Fatal(got)
	}
	f.create(t, f.sites[0], `wild%_\marker`, nil)
	f.create(t, f.sites[0], "ordinary words", nil)
	if got := f.find(t, f.sites[0], `%_\`, 1, 20); got.Pagination.Total != 1 {
		t.Fatalf("literal metacharacters: %#v", got)
	}
	f.create(t, f.sites[0], "ornithology", func(r *resource.Resource) { r.Annotation = "astrophysics" })
	if got := f.find(t, f.sites[0], "ornithology astrophysics", 1, 20); got.Pagination.Total != 0 {
		t.Fatalf("candidate index matched across fields: %#v", got)
	}

	item := f.create(t, f.sites[0], "synchronizationprobe", nil)
	updated := resource.Clone(item)
	updated.Title = "replacementmarker"
	updated.Slug = "renamed"
	path := "/renamed"
	updated.Path = &path
	updated, err := f.resources.Update(f.ctx, nil, item, updated, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.find(t, f.sites[0], "synchronizationprobe", 1, 20); got.Pagination.Total != 0 {
		t.Fatal(got)
	}
	if got := f.find(t, f.sites[0], "replacementmarker", 1, 20); got.Pagination.Total != 1 || got.Items[0].URL != path {
		t.Fatal(got)
	}
	lifecycle := f.resources.(resource.LifecycleRepository)
	if err := lifecycle.SoftDelete(f.ctx, nil, updated.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.find(t, f.sites[0], "replacementmarker", 1, 20); got.Pagination.Total != 0 {
		t.Fatal(got)
	}
	if err := lifecycle.Restore(f.ctx, nil, updated.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := f.find(t, f.sites[0], "replacementmarker", 1, 20); got.Pagination.Total != 1 {
		t.Fatal(got)
	}
	updated, err = f.resources.ByID(f.ctx, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.resources.(resource.SiteTransferRepository).TransferToSite(f.ctx, nil, updated.ID, f.sites[0], f.sites[1], updated.Version, "search-test", "search-test"); err != nil {
		t.Fatal(err)
	}
	if got := f.find(t, f.sites[0], "replacementmarker", 1, 20); got.Pagination.Total != 0 {
		t.Fatal(got)
	}
	if got := f.find(t, f.sites[1], "replacementmarker", 1, 20); got.Pagination.Total != 1 || got.Items[0].ID != updated.ID {
		t.Fatal(got)
	}

	library := f.create(t, f.sites[0], "movablecatalog", func(r *resource.Resource) {
		r.Type = resourcetype.Library
		r.TypeSettings = map[string]any{"item_url_pattern": "/{id}/{slug}"}
	})
	entry := f.createItem(t, library, "initialitemprobe", nil)
	next := entry
	next.Title = "transferreditemprobe"
	next.Slug = "updated-item"
	entry, err = f.libraryItems.UpdateLibraryItem(f.ctx, nil, entry, next, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.find(t, f.sites[0], "transferreditemprobe", 1, 20); got.Pagination.Total != 1 || !strings.HasSuffix(got.Items[0].URL, "/updated-item") {
		t.Fatal(got)
	}
	if err := f.libraryItems.SoftDeleteLibraryItem(f.ctx, nil, entry.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.find(t, f.sites[0], "transferreditemprobe", 1, 20); got.Pagination.Total != 0 {
		t.Fatal(got)
	}
	if err := f.libraryItems.RestoreLibraryItem(f.ctx, nil, entry.ID); err != nil {
		t.Fatal(err)
	}
	library, err = f.resources.ByID(f.ctx, library.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.resources.(resource.SiteTransferRepository).TransferToSite(f.ctx, nil, library.ID, f.sites[0], f.sites[1], library.Version, "search-test", "search-test"); err != nil {
		t.Fatal(err)
	}
	if got := f.find(t, f.sites[0], "transferreditemprobe", 1, 20); got.Pagination.Total != 0 {
		t.Fatal(got)
	}
	if got := f.find(t, f.sites[1], "transferreditemprobe", 1, 20); got.Pagination.Total != 1 || got.Items[0].ID != entry.ID {
		t.Fatal(got)
	}
}

func TestPostgresSearchExplain100K(t *testing.T) {
	if os.Getenv("CMS_TEST_SEARCH_EXPLAIN") != "1" {
		t.Skip("set CMS_TEST_SEARCH_EXPLAIN=1 for the 100,000-record query plan check")
	}
	f := newFixture(t)
	library := f.create(t, f.sites[0], "Benchmark library", func(r *resource.Resource) { r.Type = resourcetype.Library })
	// Bulk fixture creation deliberately bypasses domain writes; mutation behavior
	// is exercised above through the actual core repositories.
	for _, kind := range []string{"tree", "library_item"} {
		_, err := f.connector.Pool().Exec(f.ctx, `WITH entities AS (
 INSERT INTO core.resource_entities(site_id,storage_kind) SELECT $1,$2 FROM generate_series(1,50000) RETURNING id
)
`+map[string]string{
			"tree": `INSERT INTO core.resources(id,site_id,type,title,slug,path,annotation,content,is_public,is_searchable)
 SELECT id,$1,'page',CASE WHEN id%1000=0 THEN 'benchmarkneedle' ELSE 'Entry '||md5(id::text) END,'bench-'||id,'/bench-'||id,'Summary '||md5(id::text),repeat('Article content ',30)||md5(id::text),true,true FROM entities`,
			"library_item": fmt.Sprintf(`INSERT INTO core.library_items(id,site_id,library_id,partition_at,title,slug,annotation,content,is_public,is_searchable)
 SELECT id,$1,%d,now(),CASE WHEN id%%1000=0 THEN 'benchmarkneedle' ELSE 'Entry '||md5(id::text) END,'bench-'||id,'Summary '||md5(id::text),repeat('Article content ',30)||md5(id::text),true,true FROM entities`, library.ID),
		}[kind], f.sites[0], kind)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.connector.Pool().Exec(f.ctx, `ANALYZE core.resources; ANALYZE core.library_items`); err != nil {
		t.Fatal(err)
	}
	// ANALYZE on the parent builds inherited statistics; leaf statistics must
	// also be refreshed after bulk loading, as autovacuum would do normally.
	partitions, err := f.connector.Pool().Query(f.ctx, `SELECT c.relname FROM pg_partition_tree('core.library_items') p JOIN pg_class c ON c.oid=p.relid WHERE p.isleaf`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for partitions.Next() {
		var name string
		if err := partitions.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	partitions.Close()
	if err := partitions.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := f.connector.Pool().Exec(f.ctx, "ANALYZE "+pgx.Identifier{"core", name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"benchmarkneedle", "benchmarknedle", "Article"} {
		start := time.Now()
		page := f.find(t, f.sites[0], text, 1, 20)
		t.Logf("q=%q total=%d API-engine duration=%s", text, page.Pagination.Total, time.Since(start))
		if page.Pagination.Total == 0 {
			t.Fatal("benchmark query found no rows")
		}
		err := f.engine.connector.Read(f.ctx, 0.6, func(tx pgx.Tx) error {
			for _, statement := range []string{countSQL, pageSQL} {
				args := []any{f.sites[0], text, "%" + text + "%", []string{"page", "library"}}
				if statement == pageSQL {
					args = append(args, 20, 0)
				}
				var raw []byte
				if err := tx.QueryRow(f.ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+statement, args...).Scan(&raw); err != nil {
					return err
				}
				var plan []struct {
					Planning  float64 `json:"Planning Time"`
					Execution float64 `json:"Execution Time"`
				}
				if err := json.Unmarshal(raw, &plan); err != nil {
					return err
				}
				t.Logf("count=%t planning=%.2fms execution=%.2fms uses_index=%t", statement == countSQL, plan[0].Planning, plan[0].Execution, strings.Contains(string(raw), "Index Scan") || strings.Contains(string(raw), "Index Only Scan"))
				if text == "benchmarkneedle" && !strings.Contains(string(raw), "Index") {
					t.Error("selective query did not use indexes")
				}
				if path := os.Getenv("CMS_TEST_SEARCH_EXPLAIN_DIR"); path != "" {
					name := fmt.Sprintf("%s/search-%s-count-%t.json", path, text, statement == countSQL)
					if err := os.WriteFile(name, raw, 0600); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresSearchSnapshotAndTransactionSettings(t *testing.T) {
	f := newFixture(t)
	item := f.create(t, f.sites[0], "snapshotmarker", nil)
	args := []any{f.sites[0], "snapshotmarker", "%snapshotmarker%", []string{"page"}}
	err := f.engine.connector.Read(f.ctx, 0.6, func(tx pgx.Tx) error {
		var before, after int64
		if err := tx.QueryRow(f.ctx, countSQL, args...).Scan(&before); err != nil {
			return err
		}
		next := resource.Clone(item)
		next.IsSearchable = false
		if _, err := f.resources.Update(f.ctx, nil, item, next, nil); err != nil {
			return err
		}
		if err := tx.QueryRow(f.ctx, countSQL, args...).Scan(&after); err != nil {
			return err
		}
		if before != 1 || after != before {
			return fmt.Errorf("inconsistent snapshot: %d -> %d", before, after)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := f.find(t, f.sites[0], "snapshotmarker", 1, 20); result.Pagination.Total != 0 {
		t.Fatal(result)
	}

	// A single-connection pool makes reuse of transaction settings observable.
	config := f.connector.Pool().Config().ConnConfig
	connection, err := connectorpostgres.New(f.ctx, connectorpostgres.Config{Code: "settings-test", Host: config.Host, Port: int(config.Port), Database: config.Database, User: config.User, Password: config.Password, SSLMode: "disable", MaxConns: 1, ConnectTimeout: 5 * time.Second, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	trigrams, err := pgtrgm.New(connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Pool().Exec(f.ctx, `SELECT set_config('pg_trgm.word_similarity_threshold','0.81',false)`); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("deliberate callback failure")
	for _, fail := range []bool{false, true} {
		err := trigrams.Read(f.ctx, 0.17, func(tx pgx.Tx) error {
			var threshold string
			if err := tx.QueryRow(f.ctx, `SHOW pg_trgm.word_similarity_threshold`).Scan(&threshold); err != nil {
				return err
			}
			if threshold != "0.17" {
				return fmt.Errorf("transaction threshold = %s", threshold)
			}
			if fail {
				return failure
			}
			return nil
		})
		if (fail && !errors.Is(err, failure)) || (!fail && err != nil) {
			t.Fatal(err)
		}
		var threshold string
		if err := connection.Pool().QueryRow(f.ctx, `SHOW pg_trgm.word_similarity_threshold`).Scan(&threshold); err != nil {
			t.Fatal(err)
		}
		if threshold != "0.81" {
			t.Fatalf("pool setting leaked: %s", threshold)
		}
	}
	ctx, cancel := context.WithCancel(f.ctx)
	err = trigrams.Read(ctx, 0.17, func(tx pgx.Tx) error {
		cancel()
		_, err := tx.Exec(ctx, `SELECT 1`)
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error: %v", err)
	}
	var threshold string
	if err := connection.Pool().QueryRow(f.ctx, `SELECT coalesce(current_setting('pg_trgm.word_similarity_threshold',true),'')`).Scan(&threshold); err != nil {
		t.Fatal(err)
	}
	if threshold == "0.17" {
		t.Fatal("cancelled transaction leaked settings")
	}
}
