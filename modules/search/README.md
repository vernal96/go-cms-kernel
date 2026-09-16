# Resource search

The optional `search` module depends on `core` and is enabled in the `dev`
profile. It contributes a public route to each site's prebuilt HTTP runtime.

## HTTP API

```http
GET /search?q=electricity&page=1&per_page=20
Host: example.com
```

```json
{
  "items": [
    {
      "id": 42,
      "storage_kind": "tree",
      "title": "Electricity",
      "url": "/electricity",
      "annotation": "An introduction",
      "score": 3
    }
  ],
  "pagination": { "page": 1, "per_page": 20, "total": 1 }
}
```

- `q` is trimmed and must contain 3–200 Unicode characters.
- `page` defaults to 1. `per_page` defaults to 20 and must be 1–50.
- Invalid input returns `400` with the standard JSON error envelope. No matches
  returns `200`, an empty `items` array and `total: 0`. A page beyond the last
  page retains the total count and has no items.
- `storage_kind` is `tree` or `library_item`; IDs use the shared resource identity.
- The existing dispatcher resolves the site from `Host` and checks site access.
  The service checks `core.resource.read`. `site_id`, `Origin`, `Referer`,
  forwarded host headers and preview parameters do not select another site or
  expand search visibility.
- Only public, searchable, undeleted resources with an active publication window
  and a routable type participate. LibraryItems additionally require an available,
  public library with an active publication window. A library's `is_searchable`
  controls the library itself, independently of its items.
- The URL uses the existing resource path or `EffectiveLibraryItemURL`, including
  configured date, ID and slug tokens. Full resource bodies are not returned.

## Engine and persistence

`Engine.Search(context.Context, Query) (Page, error)` is owned by this module.
The site-owned service supplies `Query.SiteID` and the current runtime's routable
resource types. Engine implementations must apply site and visibility filters
before counting and paginating; they must honor context cancellation.

The PostgreSQL adapter reads `core.resources` and `core.library_items` directly.
The `pgtrgm` connector borrows the application's PostgreSQL pool, owns no resource
SQL and never closes the pool. A read-only repeatable-read transaction shares
the snapshot and publication time between count and page. Its similarity setting
is transaction-local and is restored on commit, error or cancellation.

Matching uses case-insensitive literal substrings or `word_similarity` above
the `0.6` threshold. `%`, `_` and backslashes in input are literal characters.
No morphological analysis, stemming, custom template fields or rendered-widget
content is included. The stored title, annotation and content strings are searched.

Ranking orders exact titles first, then literal phrase matches, then approximate
matches. Within each group the score is the maximum of title similarity ×3,
annotation similarity ×2 and content similarity ×1; equal scores use ascending
resource ID. Scores are engine-specific relevance values, not probabilities.

Core migration `000021_resource_search` creates partial GIN indexes on combined
search text for tree resources and every LibraryItem partition. The query
rechecks fields individually after candidate selection. Existing writes,
restores, deletes, publication changes, URL changes and site transfers take
effect on the next search without a separate document store or reindex job.

Register an engine using the usual module database factory and add `search.Module{}`
to a profile. Backend startup continues to use the normal migration workflow;
the HTTP path does not initialize indexes or build runtimes.

## Verification

From `backend`, against an **isolated test database**, configure:

```bash
export CMS_TEST_SEARCH_POSTGRES_HOST=127.0.0.1
export CMS_TEST_SEARCH_POSTGRES_PORT=5432
export CMS_TEST_SEARCH_POSTGRES_DB=cms_search_test
export CMS_TEST_SEARCH_POSTGRES_USER=cms_search_test
# Supply CMS_TEST_SEARCH_POSTGRES_PASSWORD through your test environment.
go test ./connectors/pgtrgm ./kernel/modules/search/... ./kernel/transport/httpserver
```

The integration tests apply core migrations and create/remove their own sites.
They exercise real repository mutations, visibility, ranking, pagination,
cross-site transfers, snapshot consistency and pooled connection settings.
The HTTP test uses an actual HTTP server with the normal runtime dispatcher,
two sites sharing a profile, a private site, and a profile without search.

For the larger query-plan check:

```bash
CMS_TEST_SEARCH_EXPLAIN=1 go test ./kernel/modules/search/adapters/postgres \
  -run TestPostgresSearchExplain100K -v -count=1
```

Optionally set `CMS_TEST_SEARCH_EXPLAIN_DIR` to an existing directory to retain
JSON `EXPLAIN (ANALYZE, BUFFERS)` plans. This fixture has 50,000 tree resources and
50,000 LibraryItems, plus their library, and refreshes parent and leaf statistics.

Measured locally on PostgreSQL 18 in Docker on 2026-09-15: selective exact and
typo queries took about **54 ms** for count plus page. Both resource stores used
trigram indexes. A common term matching all 100,000 entries took about **7.3 s**;
computing relevance for nearly the entire corpus dominates that case. These are
fixture measurements, not a latency guarantee. Large common-term result sets
remain expensive even with a small `per_page` and should be included in capacity
testing for the real content distribution.
