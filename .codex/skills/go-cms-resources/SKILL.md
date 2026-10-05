---
name: go-cms-resources
description: Use for GO CMS resource architecture: resource types, tree resources, resource identity, Library resources and large LibraryItem collections, resource paths/routing, resource repositories/storage, resource lifecycle/moves, resource admin metadata/capabilities, or cross-cutting resource integrations such as widgets/SEO/extensions.
---

# GO CMS Resources

Follow root `AGENTS.md`. Load `go-cms-development` as well when the task changes reusable cross-package architecture. Load `go-cms-resource-fields` when template field values, field persistence, filtering or indexing are involved.

Inspect the current resource type/service/repository contracts, the relevant adapter, direct extension consumers and focused tests before changing the model. Do not assume an older resource schema from prompt history when `main` differs.

## Resource model

Keep **resource identity**, **tree placement** and **physical storage** separate.

Conceptually:

```text
ResourceEntity / ResourceRef     stable CMS-wide resource identity
        |
        +-- TreeResource         ordinary resource stored in the site tree
        |     +-- page
        |     +-- link
        |     +-- resource_link
        |     +-- library
        |
        +-- LibraryItem          high-volume resource owned by one library
```

A `LibraryItem` is a resource for shared CMS behavior (template, fields, widgets where supported, SEO/extensions, files/media, publication/lifecycle/audit), but it is **not a node in the normal resource tree**.

Do not model a LibraryItem as an ordinary child via `ParentID`. Use an explicit `LibraryID`/library ownership relation so normal tree children and high-volume library items remain unambiguous.

Cross-cutting resource extensions must target the stable resource identity rather than a physical table that contains only tree resources. Avoid schemas such as `resource_id | library_item_id` duplicated across widgets, SEO, audit, files or future modules.

## Tree resources and resource types

`library` is a normal tree resource type and should behave broadly like a page where appropriate: it can have a route, template, content, fields, widgets/extensions and ordinary tree descendants used for navigation/menu structure.

A Library can therefore simultaneously have:

```text
Library tree node
  +-- ordinary TreeResource children
  +-- separate LibraryItem collection
```

Never mix these two collections in `ListChildren`, tree building or menu generation.

Resource type behavior belongs in reusable type metadata/contracts rather than frontend condition chains. Prefer backend capabilities such as:

- supports template/content/widgets/fields;
- owns a library item collection;
- type is mutable/immutable;
- default icon;
- type-specific editable properties/payload requirements where needed.

The admin frontend should consume those capabilities instead of accumulating `if type == page/link/library/...` logic.

Management/API code must also resolve registered resource types from the current site runtime/registry instead of hard-coding whitelists such as `page/link/library`.

A project/package resource type that is registered and exposed through metadata must be actually creatable/updatable through the management/API contract. Do not advertise a resource type while omitting required type-specific payload such as `target_resource_id`. If generic management needs type-owned properties, expose them through an explicit capability/schema/payload contract rather than adding built-in type conditionals back into transport code.

### Immutable Library type

A Library is created explicitly as type `library` and its type is immutable.

Enforce this in backend domain/service validation, not only in the admin UI:

- `library -> other type` is forbidden;
- `other type -> library` is forbidden through normal update/type-change operations.

Move, update allowed fields, soft-delete/restore and permanent delete remain separate operations.

## Resource data ownership

Keep three classes of data distinct:

```text
system properties   title, slug, publication state, audit, etc.
template fields     values defined by the selected template
type settings       behavior/configuration owned by the resource type
```

Do not store template field values inside generic resource `Settings`. Template fields use the resource-field persistence model from `go-cms-resource-fields`.

Type-specific configuration belongs to `TypeSettings` (or an equivalent clearly named domain concept). For Library this includes at least:

- item URL pattern;
- default item template.

Do not let template changes erase type settings.

## Library item templates

`DefaultItemTemplate` is a **creation default**, not dynamic inheritance.

When a LibraryItem is created without an explicit template, resolve the Library's current default and persist that concrete template on the item. Changing the Library default later must affect new items only; it must not silently reinterpret existing items under a different field schema.

An individual LibraryItem may use another valid template when the API/admin behavior explicitly permits it.

Validate a configured default template against the current site's template registry when the Library is normalized/updated. Do not accept an arbitrary string only to fail later when the first LibraryItem is created.

## Paths and URL patterns

Ordinary tree resources may continue to use their normal stored/validated path mechanics.

Do not materialize a full path for every LibraryItem if that would require rewriting a large collection when its Library moves. Resolve a LibraryItem route from the owning Library's current path plus the Library item URL pattern and item data.

Use a deliberately small URL-pattern DSL rather than arbitrary Go templates. Start with stable tokens that can be validated and routed efficiently, for example:

```text
{id}
{slug}
{year}
{month}
{day}
```

Patterns must be validated for deterministic route resolution and uniqueness. Keep the MVP rule simple where possible, e.g. unique item slug within a Library and/or require a unique token such as `{slug}` or `{id}`.

Routing must resolve both the Library tree resource itself and LibraryItem routes without loading the entire library collection.

### Route namespace invariant

A Library owns one URL namespace that may contain both ordinary tree descendants and effective LibraryItem routes. These routes must not silently shadow each other.

Enforce route collision checks in both directions:

- creating/updating/moving a LibraryItem must reject an effective URL already occupied by an ordinary tree resource;
- creating/updating/moving an ordinary tree resource beneath/into a Library namespace must reject a path already occupied by a LibraryItem;
- changing a Library URL pattern/path must validate the resulting namespace according to the chosen conflict strategy.

Do not rely on resolver order (`tree first`, `library item second`) as the uniqueness policy.

High-cardinality collision validation must remain bounded. **Never load every LibraryItem into Go to validate a Library path or `item_url_pattern` change.** Use set-based/indexed repository checks. If a particular pattern/path mutation cannot be proven safe without scanning millions of items, reject that online mutation with an explicit domain error or move it to an explicit maintenance operation instead of performing an O(N) in-memory pass under a long transaction/lock.

When patterns contain date tokens such as `{year}`, `{month}` or `{day}`, collision checks must validate the complete effective route, not merely match slug/id and assume the date portion is irrelevant. Two items/tree paths with the same slug but different date-derived routes are not a collision.

## Repository and storage boundaries

Core/domain code must not know about PostgreSQL partitions, HASH/RANGE clauses or physical partition names.

Expose domain-oriented repository operations, conceptually:

```text
Create / Get / Update / Delete
Query by Library
Move to another Library
Resolve route/item
```

Concrete adapters choose physical storage.

A PostgreSQL adapter may use a dedicated partitioned LibraryItem table while another adapter may use an ordinary table, collection or in-memory map. Do not leak `partition_at` or partition identifiers into public APIs/domain configuration unless there is a real domain requirement.

### PostgreSQL partitioning guidance

For the PostgreSQL adapter, prefer a bounded partition topology rather than one partition per Library. A suitable direction is bounded HASH partitioning by `library_id` with time RANGE subpartitioning using an adapter-owned partition timestamp/bucket.

The time subpartitions must be granular enough to provide useful pruning for high-volume date-centric libraries. Avoid one decade-sized catch-all partition for all normal production years. Prefer bounded yearly partitions initially; create finer ranges only when measurements justify them.

Use a safe bounded maintenance strategy for arbitrary valid dates (for example pre-created yearly partitions plus a controlled/default/fallback path). Partition DDL/maintenance belongs to migrations or a synchronized infrastructure maintenance mechanism, not unsynchronized request-time insert code.

The adapter may derive the internal partition timestamp from publication time with a stable fallback (for example creation time) so non-date-centric libraries such as vacancies still work without exposing partition configuration to CMS users.

When the adapter can prove equivalence, date predicates such as `published_at` ranges should also constrain the internal `partition_at` so PostgreSQL can prune yearly subpartitions. Keep this optimization entirely adapter-owned; do not expose `partition_at` in Core queries.

A default/fallback partition is a correctness mechanism, not a permanent replacement for partition maintenance. Keep a deliberate way to add future yearly partitions before the default bucket becomes the normal long-term destination.

Respect PostgreSQL uniqueness/primary-key constraints on partitioned tables. Preserve a stable globally addressable resource identity outside partition-specific composite keys when required.

Moving a LibraryItem between Libraries of the same site is a domain operation. The adapter may physically move the row between partitions, but callers should see only `Move` semantics.

If PostgreSQL can surface retryable serialization failures during partition-key updates/moves, handle them with a small bounded adapter-level retry where the operation is safe to retry. Do not leak SQLSTATE handling into Core.

## Moves and ownership invariants

A LibraryItem may belong only to a Library resource.

Before create/move, validate at least:

- target Library exists;
- target resource type is `library`;
- item and target Library belong to the same site;
- target Library is in a lifecycle state that accepts new/moved items;
- route/slug uniqueness remains valid.

Moving a Library tree node itself uses normal tree-cycle/site rules and must not require rewriting every LibraryItem's materialized path.

Treat moving a LibraryItem as an explicit command. If the admin UI exposes `update fields` and `move` as one apparent Save action, either provide one atomic backend operation or make the UI/state transition explicit and update the editor route/ownership after a successful move. Do not repeatedly issue no-op moves because the page still remembers the old Library ID.

## Lifecycle and permissions

Soft-deleting a Library should make its LibraryItems unavailable without issuing a massive row-by-row soft-delete solely to mirror the parent's state. Model effective availability from both item state and owning Library state.

An individually deleted LibraryItem remains deleted even if its Library is restored.

Permanent deletion of a Library must delete/clean its LibraryItems and cross-cutting resource extensions consistently. Use database cascades only where they preserve the domain lifecycle/audit invariants; otherwise orchestrate explicitly.

LibraryItem CRUD belongs to resource editing permissions. Do not require permission to delete the entire site merely to delete a resource inside a Library. Site-scoped access checks for LibraryItem create/update/delete/restore/move should align with the corresponding ordinary resource operation unless the product explicitly defines a stricter rule.

## Querying and pagination

Library collections are explicitly designed for high cardinality.

- Never load LibraryItems into the normal resource tree.
- Avoid repository APIs that require `ListAll` for a Library.
- Use bounded queries with deterministic ordering.
- Prefer cursor/keyset pagination for very large collections; do not design the hot path around deep `OFFSET` scans.
- Keep filters adapter-neutral and reuse the resource query vocabulary where it remains semantically valid.

LibraryItem queries must support the same meaningful typed field filtering/sorting vocabulary as ordinary resources when those fields are part of the item template. A high-volume Library must be able to express queries such as `salary >= 150000`, `city == "Moscow"`, publication/date sorting, etc. Do not build typed field indexes that are unreachable from the LibraryItem domain query contract.

If LibraryItem filtering/sorting is part of the public/admin HTTP use case, carry the same semantic query contract through the API layer. Do not stop at an internal Go repository API while HTTP exposes only `search/cursor/limit`.

High-volume text search must have an adapter-appropriate indexed strategy or deliberately bounded semantics. Avoid unindexed `lower(title) LIKE '%term%'` scans as the assumed long-term path for millions of rows.

The Library admin tab should expose server-side filtering/search rather than requiring the browser to load the collection.

## Admin behavior

A Library appears in the ordinary resource tree with a default list/library icon. If the selected template declares a non-empty icon, template icon metadata may override the type default using the repository's existing icon mechanism. An empty/unsupported template icon must not accidentally replace the Library default with a generic document icon.

A Library editor gains a `Resources` tab containing **LibraryItems only**, never ordinary tree descendants. MVP list columns:

- id;
- title;
- slug/code;
- publication/activity state;
- edit action.

Provide an `Add resource` action and bounded pagination. Creation/editing of a LibraryItem should reuse the normal resource editor concepts for shared fields, template fields and extensions where possible without pretending the item is a tree node.

A LibraryItem is not shown in the site tree and cannot be assigned an ordinary tree parent.

## Cross-cutting integrations

When introducing stable resource identity, inspect direct consumers such as:

- resource widgets;
- SEO/resource extensions;
- resource links/references;
- files/media references;
- audit/history;
- public rendering/query APIs.

Migrate them toward the common resource identity instead of special-casing LibraryItems in each module.

Do not broaden the change to unrelated modules unless an actual resource foreign key/contract requires it.

## Tests

Prefer focused tests for invariants including:

- Library type cannot be converted to/from another type;
- ordinary tree children and LibraryItems never mix;
- ordinary tree routes and LibraryItem effective routes cannot collide silently;
- date-token route checks compare complete effective routes;
- Library path/pattern validation does not enumerate the entire Library in application memory;
- LibraryItem can move only between Libraries on the same site;
- Library move changes effective item URLs without bulk item path rewrites;
- default item template is copied on creation, not dynamically inherited, and configured defaults resolve to an existing site template;
- deleted Library hides items while preserving individual item lifecycle state;
- LibraryItem CRUD uses resource-edit site permissions rather than site-deletion permission;
- Library field filters/sorts use typed field persistence and are reachable through intended API surfaces;
- large-list repository path is bounded/cursor-based;
- cross-cutting resource extensions work for both TreeResource and LibraryItem identity;
- PostgreSQL partition implementation remains behind the repository boundary, uses useful bounded time ranges and allows pruning for date-range queries where applicable;
- registered custom resource types are not rejected by hard-coded management whitelists and are not advertised without a usable payload contract.

Run focused backend tests while iterating. If the task spans resource domain, migrations, adapters, API and admin frontend, run the broader backend/frontend validation once near completion.
