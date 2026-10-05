---
name: go-cms-resource-fields
description: Use for GO CMS template/resource field definitions and values, field persistence, field storage kinds, filtering/sorting/indexing, migration away from resource settings JSONB, file/reference field persistence, or field query adapter work for ordinary resources and LibraryItems.
---

# GO CMS Resource Fields

Follow root `AGENTS.md`. Load `go-cms-resources` when resource identity, Library/LibraryItem storage or resource lifecycle is also affected. Load `go-cms-development` for cross-package reusable architecture changes.

Preserve the existing separation between field **definitions/schema** and field **values**. Field definitions belong to templates/code/runtime metadata; persisted field values belong to resource instances.

## Core rule

Template field values must not be stored in generic resource `Settings` JSONB.

Keep these concepts separate:

```text
Field Definition   typed declaration in template/module/project code
Field Schema       compiled validation/normalization metadata
Field Values       persisted values of one ResourceEntity
Type Settings      resource-type configuration, not template fields
```

A resource-facing domain/API model may still expose field values conveniently as:

```go
Fields map[string]any
```

but the persistence adapter should store queryable scalar values in a typed form.

Do not mix Library configuration such as URL patterns/default templates into `Fields`; those belong to the resource type settings model.

## Reuse current field validation

Use `Definition.Validators` for configurable constraints; resolve module-contributed types from the site runtime and compile them with the field schema. Keep intrinsic type normalization and validation in `ValueType`, and `Required` in the schema. Do not reintroduce string `Rules` or pure validation constraints in structural options.

The normal write flow remains conceptually:

```text
input Fields map
 -> template FieldSchema.Validate/Normalize
 -> validate references/files
 -> persist normalized typed field values
```

Do not duplicate field normalization rules in PostgreSQL or the admin frontend.

A field type used in persisted template fields must expose valid storage semantics at template/schema compilation time. Do not allow a template to compile successfully and fail only on the first resource save because its custom `ValueType` lacks a supported storage kind.

## Storage kind abstraction

Field type codes are extensible and must not be hard-coded into SQL adapters one-by-one.

Introduce a small infrastructure-neutral storage/value kind contract associated with the compiled field value type, conceptually:

```text
string
integer
float
boolean
timestamp
reference
json
```

Multiple semantic field types can share one storage kind, for example:

```text
string / textarea / email / phone / radio / scalar select -> string
int                                                   -> integer
float                                                 -> float
checkbox                                              -> boolean
file/resource reference                               -> reference
json                                                  -> json
```

A future custom field type may define its own semantic validation/editor while reusing a standard storage kind. Keep storage kind about persistence/comparison semantics, not admin presentation.

Do not leak PostgreSQL column names into field type contracts.

## Typed field-value persistence

Use a dedicated repository/table keyed by stable resource identity rather than storing all fields in one JSONB object.

A suitable PostgreSQL shape is conceptually:

```text
resource_id
site_id                when required for integrity/scoping
field_key
position               0 for scalar, ordinal for multi-value
value_kind
value_string
value_integer
value_float
value_boolean
value_timestamp
value_reference
value_json
```

Exact naming may follow repository conventions, but preserve these invariants:

- one stable owning resource identity;
- field key is explicit;
- scalar values are typed;
- multi-value fields have deterministic order/position;
- only the column appropriate to the storage kind is populated;
- values for ordinary TreeResources and LibraryItems use the same field-value abstraction.

Avoid creating physical SQL columns dynamically for every project-defined field.

## Multi-value fields

Do not serialize a multi-select/list into one opaque JSON value when its members are expected to be filterable individually.

Represent filterable multi-values as multiple rows with the same `(resource_id, field_key)` and deterministic `position`.

For scalar fields, use exactly one logical value with position `0`.

Keep JSON storage for genuinely structured/opaque data where relational filtering is not part of the field contract.

Define negative multi-value predicates using set semantics, not per-row inequality. For example, `roles NOT IN ["editor"]` means the resource must have no matching `editor` row; it must not pass merely because another row such as `author` is different. Implement negative membership/equality with `NOT EXISTS` (or an equivalent correct relational form) where required.

Also define behavior for missing fields explicitly. Do not let SQL `NULL`/absence produce accidental semantics that differ between scalar and multi-value fields.

## Query contract

Preserve adapter-neutral field paths such as:

```text
resource.field.<key>
```

Do not expose SQL/EAV details to callers.

The resource query layer should continue to express filters/sorts semantically. PostgreSQL translates them into typed field-value predicates/joins/`EXISTS` expressions using the field's storage kind.

Examples:

```text
resource.field.salary >= 150000
resource.field.city == "Moscow"
resource.field.remote == true
```

must compare typed columns, not cast JSONB text at runtime.

This vocabulary must be reusable by both ordinary resource queries and high-volume LibraryItem queries. Do not build a second weaker LibraryItem filter model that cannot query typed template fields.

Validate that an operator is compatible with the field/storage kind before hitting persistence. Ordering operators require an orderable storage kind.

When sorting by field values, define deterministic behavior for missing values and add resource ID as a stable tie-breaker.

If the same field key can resolve to incompatible storage kinds across the queried template scope, reject/resolve the ambiguity explicitly rather than performing unsafe casts or silently choosing one kind.

## Indexing

The purpose of dedicated typed persistence is to make filtering/sorting/indexing predictable.

Prefer bounded generic indexes appropriate to common scalar access patterns, e.g. by field key + typed value and/or owning site/library scope where justified by real queries.

Do not create one SQL index automatically for every field declaration during routine runtime startup.

Avoid B-tree indexing large textarea/opaque JSON values by default. Search/full-text indexing is a separate capability and should not be conflated with ordinary equality/range filters.

If field metadata later gains `Filterable`, `Sortable`, `Searchable` or similar capabilities, treat them as semantic/query metadata rather than PostgreSQL-specific DDL flags.

For high-volume LibraryItems, inspect query shapes before finalizing composite indexes so library ownership/site scope participates where needed.

### PostgreSQL LibraryItem sort projection

For high-volume LibraryItem ordering, the PostgreSQL adapter may project Library ownership into typed field-value rows, for example through an adapter-owned `library_id` column used by indexes such as:

```text
(site_id, library_id, field_key, typed_value, resource_id)
```

Treat this value as a **denormalized PostgreSQL query projection**, not as domain ownership and not as a second source of truth. Canonical Library ownership remains the LibraryItem relation itself.

This projection must:

- remain invisible to Core/domain/API contracts;
- exist only where it measurably improves adapter query plans;
- be written atomically together with LibraryItem field values;
- be updated when a LibraryItem moves between Libraries;
- be restored correctly during revision restore or any mutation that can change Library ownership;
- be removed naturally with the owning field values/resource lifecycle;
- never be used to redefine `LibraryID`, tree parentage or resource identity.

When modifying LibraryItem write paths, explicitly review Create, field Update/template change, Move, permanent Delete and revision Restore so the projection cannot drift from canonical Library ownership.

Performance tests for high-volume custom sorting should include **multiple large Libraries on the same site using the same field key**, not only one Library. Prefer real PostgreSQL planner evidence (`EXPLAIN` / `EXPLAIN ANALYZE`) showing that unrelated Libraries do not dominate the scan.

Do not remove this projection as “duplicate data” merely because Library ownership also exists on `library_items`: its role is adapter-local query acceleration, while the canonical domain relationship remains unchanged.

## Writes and transactions

Updating resource/template fields must be atomic with the owning resource mutation when partial state would be invalid.

A repository may implement a replace/upsert strategy, but it must:

- remove values no longer present;
- preserve normalized multi-value order;
- reject duplicate/invalid scalar state;
- keep resource/site ownership integrity;
- update file/reference relations consistently;
- avoid leaving old values after a template change.

Do not silently preserve values whose field key no longer exists in the selected template unless an explicit archive feature is designed.

## Template changes

Changing a resource template changes the valid field schema.

The service must validate the resulting `Fields` against the new template and remove/reject incompatible old values deterministically. Admin confirmation may warn the user, but backend correctness cannot rely on the frontend clearing values.

`DefaultItemTemplate` for a Library affects creation only; once a LibraryItem stores a concrete template, its fields are validated against that template like any other resource.

## File and reference fields

Preserve existing file/media authorization and validation semantics when moving values out of JSONB.

A file/reference field should persist a stable referenced ID using the reference storage kind while any required FK/reference table remains adapter-owned. Do not weaken validation merely because storage changed.

When field values are replaced or a resource is permanently deleted, stale reference rows must be cleaned consistently.

## API and admin transport

Prefer explicit transport naming:

```json
{
  "template_code": "vacancy",
  "fields": {
    "salary": 150000,
    "city": "Moscow"
  },
  "type_settings": {}
}
```

Do not continue using `settings` as an ambiguous alias for template field values. This project is pre-production, so remove obsolete aliases/contracts instead of keeping transitional compatibility.

The admin dynamic field UI may continue working with a `Record<string, unknown>` map; persistence normalization is a backend concern.

## Pre-production schema changes

Root `AGENTS.md` defines the project-wide compatibility policy: there is no production data to preserve.

When replacing an old field persistence design such as `resources.settings` JSONB:

- change the current schema and code directly to the correct model;
- update or squash not-yet-production migrations when that yields a cleaner migration history;
- update seeds, fixtures and tests to the new schema;
- remove obsolete migration-only commands, staging tables, manifests, dual-write code and compatibility readers once the new model is chosen;
- do not build `prepare/audit/repair` pipelines merely to preserve developer databases;
- do not keep legacy JSONB data conversion logic solely because local rows may exist.

If destructive migration/reset is simpler and safer for the current architecture, prefer it. A developer database may be recreated.

Only design a real data migration when the user explicitly says that production/external data must be retained for that task.

## Resource identity

Field values should attach to the common stable resource identity used by both TreeResources and LibraryItems. Do not create separate parallel field tables such as `resource_fields` and `library_item_fields` unless a measured adapter constraint requires it.

This is what allows templates, filtering and extensions to treat LibraryItems as resources without duplicating field behavior.

## Tests

Add focused tests for changed invariants, including as applicable:

- schema validation remains the source of normalized values;
- persisted template field types always expose storage semantics at compile time;
- scalar storage kind maps to the correct typed value;
- multi-value ordering and positive filtering;
- multi-value negative filters use correct set semantics;
- missing-field semantics are deterministic;
- update removes stale values;
- template change rejects/removes incompatible fields;
- file/reference validation remains enforced;
- field filters use typed semantics for string/numeric/boolean/reference values;
- ordinary Resource and LibraryItem share the same field-value query vocabulary;
- no resource query path depends on obsolete JSONB `settings -> field_key` access;
- PostgreSQL LibraryItem field-sort projections remain synchronized across Create/Update/Move/Restore paths and stay adapter-local;
- multi-Library custom-sort planner tests prove that same-field rows from unrelated Libraries do not dominate the hot path;
- obsolete legacy field-migration compatibility code is removed when the schema is replaced.

Run focused package/adapter tests first, then broader backend integration tests when the change affects resource service, PostgreSQL and public/admin query paths.
