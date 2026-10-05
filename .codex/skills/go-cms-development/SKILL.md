---
name: go-cms-development
description: Use for cross-package GO CMS backend implementation that changes architecture, reusable extension mechanisms, App/profile/module composition, repositories/adapters/connectors, routes, migrations, seeds, registries, caches or filesystems. Do not use for isolated local fixes when the affected package is sufficient.
---

# GO CMS Development

Follow root `AGENTS.md`. This skill adds only the workflow for reusable backend architecture work.

## Discovery

1. Inspect the declaration/entry point being changed.
2. Inspect the reusable registry/manager/factory/runtime that consumes it.
3. Inspect one nearby working pattern and focused tests.
4. Expand to other packages only when an actual dependency requires it.
5. Use `codebase-memory-mcp/get_architecture` only for broad cross-cutting work when these targeted reads cannot establish the dependency flow.

Do not reconstruct the whole repository before a scoped implementation.

## Ownership and lifetime

Before editing, classify behavior by owner:

- kernel: generic contracts, managers, registries, factories, lifecycle/runtime mechanics;
- module: domain behavior, services, repositories, module runtime and module-specific extension mechanics;
- connector/adapter: concrete infrastructure integration;
- project/internal: declarations, selections, bindings and genuinely project-specific behavior.

Classify state as application, profile, site, module or request scoped. Put it at the narrowest correct lifetime.

## Declarative project rule

For routine items under `backend/internal/`, project code should contain only unique identity/config/bindings/dependencies/business behavior. Reusable code owns common collection, registration, validation, ordering, build, reload and shutdown.

Use this test:

> If the next item of the same category is added tomorrow, which unchanged lines would be copied?

If the answer contains generic lifecycle/registration glue, move that mechanism to its reusable owner.

Apply this to profiles, modules, connectors/bindings, migrations, seeds, routes, middleware, permissions, templates/widgets and extension registrations.

## Modules and persistence

- Keep module domain contracts/services inside the module boundary.
- Keep cross-module dependencies explicit; do not turn `ModuleContext` into an unrestricted service locator.
- Preserve persistence direction:

```text
project selection -> connector -> module adapter -> repository contract/implementation -> service/runtime
```

- Connectors are low-level technology clients; repositories know domain entities.
- Project code selects factories/bindings instead of manually constructing every repository/service when reusable factories already have enough information.

## App, routes and lifecycle

- `App` assembles/owns/exposes subsystems; do not duplicate domain APIs as App forwarding methods.
- Route declarations contain route-specific data/handlers. Reusable HTTP code owns collection, prefixes, middleware composition, conflict detection and dispatch.
- Migration/seed declarations contain unique metadata/execution logic. Reusable subsystems own discovery/ordering/tracking/transactions/lifecycle.

Do not build a generic metaframework for a one-off behavior; abstract only repeatable mechanics that are already part of the extension model.

## Completion

- Keep changes incremental and avoid unrelated cleanup.
- Add focused tests for changed contracts/invariants.
- Run focused tests while iterating; use broad backend checks once near completion if the change is cross-package/cross-cutting.
- Final report: what changed, ownership/scope rationale, important files, commands actually run, and intentionally deferred work. Keep it concise.
