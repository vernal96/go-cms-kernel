---
name: go-cms-admin-ui
description: Use for GO CMS admin extensibility: backend-driven navigation/manifests, module-contributed admin items, permission-aware composition, semantic routes/icons, site-scoped admin UI, and frontend admin plugin registration. Not for ordinary isolated Vue component styling.
---

# GO CMS Admin UI

Follow root instructions and load `go-cms-development` only when the task also changes reusable backend architecture.

## Discovery

Inspect only the admin backend/frontend files touched by the requested capability plus their direct contracts/tests. Do not automatically read all admin, core, runtime and frontend files for every UI change.

## Boundary

Backend decides **what is available**; frontend decides **how it renders**.

Backend owns module/site availability, navigation structure/order, permissions, scope and semantic route/icon IDs. Frontend owns Vue components, Vue Router records, concrete icons, forms/tabs and interaction.

Do not send Vue component names/import paths/executable JS from the backend. Use semantic values such as `forms.list` and `forms`.

## Module extensibility

- `admin` must not import optional feature modules to discover their UI.
- Optional modules contribute through a small generic capability/provider. Do not expand the mandatory module interface with empty admin methods.
- Adding a feature module should not require editing `admin` or hard-coding a new item in the shell.
- Runtime-dependent contributions should come from the final site/module runtime; installed package does not mean enabled for every site.

## Navigation scope and permissions

Support the semantic distinction:

- global: application-wide items such as sites/users/files when appropriate;
- site: items available only when the selected site's final runtime enables the contributing module.

No mutable global current-site/menu registry.

Filter navigation on the backend using the current actor/authorizer. Remove unavailable children and empty grouping parents. Menu filtering is not authorization: target endpoints must still enforce permissions independently.

Ordering and duplicate handling must be deterministic and validated.

## API and frontend plugin contract

Expose frontend-neutral navigation/manifest data. The backend returns semantic identity, label, order, scope, permission-derived visibility, route ID and icon ID—not implementation paths.

The frontend shell resolves those IDs through installed plugins. Keep one obvious declarative plugin composition point. Module-owned screens/routes/icons belong to their plugin; shell-owned login/dashboard/fallback routes may remain in the shell.

Expected build model:

```text
new frontend package/code -> install + rebuild
backend navigation/config change -> no frontend rebuild
enable/disable already installed module for a site -> no frontend rebuild
```

Do not introduce runtime remote-JS loading, Module Federation or microfrontends unless explicitly requested.

If backend navigation references a route/plugin absent from the current frontend build, fail gracefully: omit/disable it or show a controlled unsupported state and emit a useful development warning. Do not crash the admin shell or route elsewhere silently.

On site switch, preserve global items and refresh site-scoped items from the backend without rebuilding the whole frontend app.

## Tests and validation

Choose tests matching the feature:

- backend: contribution collection/order/duplicates, permission filtering, global-vs-site availability;
- frontend: generic menu rendering, semantic route resolution, site refresh and missing-plugin behavior.

Do not over-test Element Plus internals. Run focused backend/frontend checks while iterating and full build/test only near completion when the change spans those applications.
