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
