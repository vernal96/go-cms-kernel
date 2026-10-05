---
name: go-cms-resource-revisions
description: Use for GO CMS resource revision/history architecture and implementation: resource version counters, immutable snapshots, optimistic locking, revision persistence, restore/rollback, revision authors, history retention/purge, PostgreSQL storage, LibraryItem revision policy, and admin/API history behavior.
---

# GO CMS Resource Revisions

Follow root `AGENTS.md`. Load `go-cms-resources` for resource identity/lifecycle/storage invariants, `go-cms-authorization` when history permissions change, `go-cms-api` when revision endpoints change, and `go-cms-admin-ui` when the admin history UI changes. Load `go-cms-administration` for global destructive history maintenance.

Inspect the current resource service/repositories, resource widgets, common resource identity, PostgreSQL adapter/migrations, relevant management API, authorization checks and focused tests before changing revision behavior. Do not assume the resource is stored in a single SQL row.

## Core model

Revisions are append-only snapshots of versionable resource state. They are not event sourcing and they are not an audit log.

The current resource remains the source of truth for reads/rendering. A revision exists to answer:

```text
what did resource X look like at version N?
who created that version?
can version N be restored under the current CMS rules?
```

Do not rebuild current state by replaying revisions.

## Version identity

A version counter belongs to the current resource identity and must be monotonic.

Conceptually:

```text
Resource.Version = current persisted version
Revision.Version = version produced by that mutation
```

Do not renumber or reset `Resource.Version` when revision rows are purged. If resource version 28 has all history deleted, the next successful versionable mutation creates version 29.

Use the version counter for optimistic locking. Mutations based on stale expected versions must fail explicitly (HTTP should normally surface this as conflict) rather than silently overwrite newer edits.

## Revision snapshot

Use a dedicated revision snapshot DTO/value object rather than serializing the entire persistence entity blindly.

A core resource snapshot should contain the state needed to reconstruct the resource content under current validation, including where applicable:

- resource/tree placement inputs such as parent identity and slug;
- resource type, template and content type;
- title/menu title/annotation/content;
- media/resource/external-link references;
- publication/search/menu/sitemap flags;
- sort/order inputs;
- publication timestamps;
- template field values;
- type settings;
- resource widget bindings, including semantic widget code/area/order/presentation/params.

Do not restore transient/audit metadata such as `CreatedAt`, `UpdatedAt`, `CreatedBy`, `UpdatedBy`, `DeletedAt` or `DeletedBy` from a snapshot.

Do not blindly restore a previously materialized route/path when current code can derive it from semantic inputs such as parent + slug. Re-run current path/tree validation during restore.

Do not couple core revision snapshots to optional feature packages such as SEO/forms/search. Optional modules own their own persistence/history unless and until a generic revision-contributor contract is justified by multiple real consumers.

## Widget identity

Widgets are versionable resource state even though they are mutated through separate repository/service paths.

A restore should reconstruct widget semantics (code, area, position, presentation, params), not resurrect historical database primary keys/binding IDs merely because they appeared in an old row.

Every widget create/update/delete/reorder path that changes the logical resource state must participate in the resource version/revision transaction.

## Revision author

Every persisted revision records the actor that produced it.

Keep a nullable/stable user reference where possible and also preserve a small immutable display snapshot (for example display name) so historical attribution survives later account deletion or renaming.

Do not treat the current resource `UpdatedBy` as a substitute for revision authorship; each revision needs its own author metadata.

## Restore semantics

Restore never rewrites history in place.

Example:

```text
v1 v2 v3 v4 v5
restore v2
=> create v6 whose snapshot/state is based on v2
```

Record the restore kind and source version when useful. Keep old versions immutable.

Before persistence, project the old snapshot into a candidate current resource and validate it using the current SiteRuntime/profile/resource type/template/field/widget/file/media/path rules. If an old template/field/widget no longer exists or violates current rules, reject restore with a clear error instead of inserting invalid state.

## Transaction boundary

A versionable mutation and its new revision must be atomic.

For a PostgreSQL adapter the logical transaction is:

```text
BEGIN
  update current resource state
  update dependent field/widget rows as required
  increment/check version
  insert revision snapshot + author metadata
COMMIT
```

If any part fails, roll back all parts. Do not perform `repository.Update()` and then append a revision in a separate transaction.

Keep SQL/JSONB/TOAST/transaction implementation inside the concrete adapter. Core contracts express semantic mutation/history operations, not PostgreSQL details.

## PostgreSQL storage

PostgreSQL is the default and preferred revision store because revisions must share an atomic transaction boundary with current resource persistence.

A practical adapter representation is one `resource_revisions` table with indexed metadata columns plus one `JSONB` snapshot. Do not normalize revision fields/widgets into many historical tables unless a demonstrated query requirement needs it.

Primary access patterns are bounded and resource-local:

```text
list metadata for one resource ordered by version desc
load one resource/version snapshot
count history
purge one resource history
purge all history
```

Do not fetch the JSONB snapshot when only listing revision metadata. Keep the list endpoint bounded/paginated.

Start without partitioning unless measured size/workload requires it. Do not introduce MongoDB/ClickHouse/S3 as the primary revision store merely because history can grow; that would sacrifice the simple database transaction and introduce distributed consistency work.

## Purge/retention

History is operational data and may be explicitly purged.

Support at least:

```text
purge history for one resource
purge all resource history globally
```

Do not expose arbitrary single-revision deletion by default; it creates holes without a strong product benefit.

Purging history must not alter current resource state or reset its version counter.

For PostgreSQL, a resource-local purge may use indexed `DELETE ... WHERE resource_id = ...`. A true global purge may use adapter-specific `TRUNCATE` semantics when safe/appropriate, but `TRUNCATE` must never leak into core contracts.

Expose counts/metadata required by the admin UI to show how many revisions will be deleted. The global destructive operation belongs under the protected administration boundary described by `go-cms-administration`.

Revision purge itself is not a new revision. A future audit module should record history purge operations separately.

## LibraryItem policy

The common stable resource identity should allow both tree resources and LibraryItems to participate in revisions.

Because Library collections may be extremely large or bulk-imported, revision creation for LibraryItems must be policy/configuration aware. Do not force millions of imported items to generate history merely because tree resources do.

The domain contract should not expose PostgreSQL partition details.

## API/admin behavior

Typical resource-local API shape:

```text
GET    /api/sites/{siteID}/resources/{resourceID}/revisions
GET    /api/sites/{siteID}/resources/{resourceID}/revisions/{version}
POST   /api/sites/{siteID}/resources/{resourceID}/revisions/{version}/restore
DELETE /api/sites/{siteID}/resources/{resourceID}/revisions
```

Use current repository route conventions if they differ; do not create an isolated API style.

Resource-local read/delete/restore must enforce both semantic history permissions and the target site's scope/ownership rules. Frontend visibility is not an authorization boundary.

The resource editor may expose an `History` tab when the actor can read history. Purge controls appear only when authorized and require a destructive confirmation.

## Tests

Cover at least:

- resource create/versionable update produces monotonically increasing revisions;
- stale expected version fails and does not overwrite current data;
- fields and widgets are captured/restored as logical state;
- resource + revision persistence is atomic on failure;
- restore creates a new version and never rewrites old history;
- restore is rejected when the historical snapshot is invalid under current runtime rules;
- revision author metadata is preserved;
- resource-local purge removes history but keeps current `Resource.Version`;
- global purge removes all history but keeps every current resource version;
- unauthorized/cross-site history operations are denied;
- LibraryItem revision policy does not accidentally create unbounded history during bulk workflows.

Run focused resource/adapter/API tests while iterating and broader backend/frontend validation once near completion when the task spans all layers.
