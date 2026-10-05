# Go CMS Kernel Agent Instructions

## Branch and compatibility

- Work only on `main` unless the user explicitly requests another branch.
- Treat the current files on `main` as the source of truth.
- The project is pre-production: do not add legacy compatibility, fallback
  paths, transitional APIs, or dead compatibility code unless requested.

## Ownership boundaries

- The module root owns generic contracts, registries, lifecycle, and runtime
  mechanics.
- Built-in modules own their domain services, repositories, migrations, and
  module runtime behavior.
- Connectors own concrete infrastructure clients; repositories know CMS domain
  entities and depend on connectors only at the adapter boundary.
- Consuming applications own project profiles, selections, bindings,
  configuration, and executable entrypoints.
- Keep runtime state site-scoped and preserve explicit module dependencies.

## Change discipline

- Keep changes coherent and avoid unrelated refactors.
- Pass `context.Context` through blocking, I/O, and lifecycle operations.
- Prefer explicit constructors, factories, interfaces, and registries over
  reflection or mutable global state.
- Return explicit errors and do not hide failures behind silent fallbacks.

## Validation

- Run focused package tests while iterating.
- For cross-cutting changes run `go test ./...`, `go vet ./...`,
  `go build ./...`, and `go mod tidy -diff` before completion.
- Report service-dependent integration tests as skipped when their environment
  is unavailable.


## Skill routing

The canonical GO CMS backend/domain Codex skills live in this repository under `.codex/skills/`.

- Load the smallest applicable skill set; usually one domain skill is enough.
- Combine a workflow skill with a domain skill only when both scopes materially apply.
- Do not load every skill proactively.
- `go-cms-requirements`: resolve material requirements before non-trivial or ambiguous feature/architecture work.
- `go-cms-development`: reusable backend architecture, extension/composition, repositories, adapters, connectors and registries.
- `go-cms-runtime-integrity`: site/profile runtime build, reload, publication and coherence.
- `go-cms-cache`: cache stores, aliases, keys/tags, TTL and invalidation/coherence.
- `go-cms-filesystem`: disks/drivers, bindings, CMS files/folders and storage selection.
- `go-cms-images`: image processing, edited derivatives, thumbnails and restore/delete semantics.
- `go-cms-events-jobs`: events, jobs, outbox, retries, idempotency and delivery semantics.
- `go-cms-templating`: reusable interpolation/rendering and escaping.
- `go-cms-mail`: mail templates, transports, jobs, history and admin/API integration.
- `go-cms-forms`: forms, fields, submissions, triggers/actions and Forms admin/API.
- `go-cms-architecture-review`: architecture/refactor/PR/commit review.
- `go-cms-admin-ui`: backend-driven admin extensibility/navigation/frontend plugin integration.
- `go-cms-administration`: protected global system administration.
- `go-cms-api`: HTTP APIs, DTOs, validation, pagination/filter/sort and transport errors.
- `go-cms-authorization`: groups/roles/permissions and site-scoped authorization.
- `go-cms-widgets`: widget definitions, layouts, persistence, editing and rendering.
- `go-cms-resources`: resource types/tree/identity/routing/storage/lifecycle.
- `go-cms-resource-revisions`: version counters, immutable snapshots, locking, restore and retention.
- `go-cms-resource-fields`: typed resource/template fields, persistence, filtering and indexing.
