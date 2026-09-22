# Resource and widget caches

Bind `core.DurableCacheAlias` to a store for URL identities, resource records,
and widget configuration. Bind `core.HotCacheAlias` for widget results and
menus. Both aliases may use the same physical store. Module/site namespaces
remain distinct; dependency invalidation crosses participating aliases/stores.

The PostgreSQL adapter implements `resource.CacheReadRepository` for lightweight
route lookup and a single owner-scoped query for missing widget configurations.

## Read path

1. `route:v1:<site>:<path hash>` contains a `resource.RouteTarget`: storage kind,
   entity ID, site ID, and library ID for a library item. It contains no content
   or authorization/publication decision. Failed lookups are not cached.
2. `resource:id:v3:<id>` or `library-item:id:v1:<id>` contains entity fields and
   an ordered list of widget references. Each reference carries binding ID,
   area, position, and a configuration digest. It does not embed widget params.
3. `widget:config:v1:<site>:<owner>:<binding>:<digest>` contains an immutable
   configuration. Missing configurations are fetched together. A digest mismatch
   causes a fresh entity snapshot to be loaded; mixed snapshots are not returned.
4. `widget:result:v1:...` holds successful public widget output. Keys include
   the compiled runtime generation, site/resource input, binding identity,
   parameters, field bindings, and resource values. Reordering persisted widgets
   does not change their result identity. Static template widgets use their
   template placement identity instead of a persisted binding ID.

Publication, deletion, permission and preview checks still run on requests.
Preview bypasses result caching. A warm content cache does not imply a request
performs no SQL: authorization, extensions and uncached widgets may query data.

## Widget result policy

Instances opt in by implementing `widget.ResultCacheable`. A positive TTL
promises that the result is public and depends only on the supplied render
input, params and declared dependency tags. Do not opt in when actor identity,
request headers, cookies or other undeclared context values affect the result.
`widget.Functional.CachePolicy` provides the same opt-in for functional widgets.

The core content and HTML widgets opt in with a five-minute TTL. The resource
list declares the site's resource collection tag and exposes the earliest
scheduled publication/unpublication through `ResultCacheDeadline`; this bounds
the result's lifetime even if no write occurs at that time. An instance may
implement this interface when rendering discovers an earlier expiry boundary.

Errors, nil results and unencodable results are not cached. Each widget retains
independent error handling. A new site runtime generation cannot reuse outputs
from an old module implementation/configuration. Result caches are consequently
not shared between separately compiled runtime instances.

## Mutations and concurrency

- Content edits invalidate the entity and collection results, preserving URL
  identity. Widget edits invalidate the owning entity/layout; immutable unchanged
  widget keys and unrelated result keys remain usable.
- Route changes invalidate the site's URL namespace. Tree moves/deletes/restores,
  revision restoration and file cascades additionally invalidate affected site
  entity snapshots. Site transfer invalidates both sites.
- Library item slug/publication-date changes, moves and lifecycle operations
  invalidate the relevant URL namespace. The library's own publication window
  gates item delivery.
- PostgreSQL entity/cache loads use repeatable-read snapshots for fields,
  widgets and version. Partial widget misses verify configuration digests.
- `cache.PreparedStore` captures dependency tokens **before** authoritative
  reads. Redis/filesystem prepared writes preserve those tokens: a mutation
  during loading makes a late fill stale immediately. Stores without this
  capability do not receive tagged prepared fills; the operation remains usable
  from its authoritative source and the unsupported capability is observable.
- Redis `GetMany` pipelines entries and distinct dependency-token reads. Core
  reads all referenced configurations together and batches results per layout
  area. An unavailable/corrupt cache falls back to authoritative reads.

Repository TTL defaults to five minutes. Immutable config entries are reclaimed
by TTL (and filesystem pruning); they need not be synchronously deleted on edits.
Cache invalidation is still fail-open as in the existing repository policy;
observe invalidation failures and restore/clear a failed cache before relying
on it again. Direct SQL writes bypass application invalidation and are not a
supported online mutation path.

## Verification

Focused tests cover warm reads without repeated entity/route queries, partial
widget misses, mixed snapshots, corrupted entries, URL/site isolation, result
identity, dependency changes, render failures, and invalidation during cache fill.

`app.TestRoutingCachePostgresRedisLifecycle` uses real application services,
PostgreSQL transactions, Redis, and the compiled HTTP resource handler. Run it
against an isolated database with `CMS_TEST_POSTGRES_HOST`,
`CMS_TEST_POSTGRES_PORT`, `CMS_TEST_POSTGRES_DB`, `CMS_TEST_POSTGRES_USER`,
`CMS_TEST_POSTGRES_PASSWORD`, and `CMS_TEST_REDIS_ADDR` configured:

```sh
go test ./app -run '^TestRoutingCachePostgresRedisLifecycle$' -count=1
```
