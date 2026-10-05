---
name: go-cms-widgets
description: Use for GO CMS widget definitions/references, module widget providers, template/resource widget composition, dynamic area layout, widget views/presentation, widget persistence/admin editing, or public widget rendering.
---

# GO CMS Widgets

Follow root instructions and load `go-cms-development` only if the task also changes reusable backend architecture. Inspect the affected widget/template/resource/runtime path and focused tests; do not automatically scan the entire widget pipeline for a local change.

## Core model

Keep these concepts separate:

```text
Widget definition/reference -> reusable typed identity/metadata
Widget runtime              -> site/module-scoped implementation with dependencies
Resource binding            -> persisted widget instance on a resource
Placement                   -> composed template + resource layout item
Rendered widget             -> public request result
```

A widget belongs to a module. Final widget availability comes only from modules enabled in the current site's runtime. Never use a mutable global widget registry/active-site singleton.

## Typed declarations

Project/profile/template Go declarations should use typed widget/view objects, not manually reproduce compiled/global codes or `Kind` discriminators.

Prefer conceptually:

```go
template.Widget{Widget: corewidgets.Content}
template.ResourceWidgets{}
```

rather than raw `Widget: "core_content"`, technical keys or `Kind` fields. Codes remain valid inside registries, persistence, APIs, logs and compiled catalogs.

A reusable widget reference is not the site-scoped runtime implementation. Module runtime/catalog compilation connects the reference to the implementation.

## Layout composition

Areas are an ordered `template.Layout` of `template.Area` declarations with Code, Label and AdminSpan. Area is placement, not widget type. `default` is reserved for the implicit/recovery container.

Templates may contain static typed widgets and an explicit resource-widget slot. Replace the slot in place so ordering is preserved; do not append resource widgets blindly. Allow at most one resource-widget slot per area; nil Items implies a slot, explicitly empty Items does not.

Expose declared areas and their editability through backend metadata. Without declarations, provide default. Recover bindings from missing/non-editable areas in default without rewriting their stored codes; restoring the area restores untouched bindings.

## Views, params and presentation

- Custom views are typed profile declarations associated with a widget; templates reference the view object directly.
- `default` view is implicit. Do not require explicit default-view declarations or default-presentation boilerplate in project/template code.
- Widget business configuration belongs to validated `Params`/field definitions.
- Common presentation is separate: view, columns, margins and enabled state. Keep backend names framework-neutral rather than Tailwind/Bootstrap-specific.
- Widget implementations capture infrastructure/services during module runtime build. `Render` receives request/resource context, not an unrestricted service locator.

## Identity and ownership

A persisted resource binding needs its own stable ID because the same widget type may occur multiple times. Compiled widget code identifies widget type, not binding instance.

Compiled catalogs must retain explicit owning-module metadata; do not recover ownership by parsing global widget-code strings.

## Persistence and mutation

Persist enough stable data for resource widgets: resource ID, binding ID, widget code, area, position, view, presentation and params. Enforce valid area/ranges, deterministic per-area order and transactional move/reorder behavior.

Admin mutations use stable binding IDs and validate against the final site/profile runtime:

- widget is available;
- template permits the target area;
- params are valid;
- selected view belongs to the widget and is available;
- presentation values are valid.

Reuse resource-update authorization unless a separate permission model is explicitly required.

## Admin metadata/UI

Backend metadata should let the editor work without hard-coded module/widget knowledge: widget/module labels, fields, editor/summary metadata, available views and supported areas.

The editor provides dynamic area lists, add/edit/delete/reorder/move, module grouping/search, dynamic fields and common presentation controls. Use the frontend UI stack already present in the repository; do not add another framework for widget controls without an explicit request.

## Public rendering

Keep the pipeline conceptually:

```text
resolve prebuilt SiteRuntime
 -> resource/template
 -> compose template items + resource bindings
 -> dynamic area placements
 -> resolve widget runtime
 -> normalize/validate params + presentation
 -> Render(ctx, site/resource snapshots)
 -> public area-code-to-widget-array output
```

Return an object keyed by dynamic area codes, without container labels or admin widths. Include stable frontend-useful widget identity/presentation metadata; do not leak raw params unless the widget deliberately renders them as data. Disabled resource widgets remain persisted/editable but are omitted from public rendering.

Preserve per-widget failure isolation when already supported so one broken widget does not unnecessarily fail the whole page.

The core content widget remains the minimal canonical widget: no required params/fields, implicit default view, content from the resource render snapshot, provided through the normal core module runtime path.

## Tests

Pick focused tests for the changed invariant: typed declaration compilation, template-slot ordering, per-site availability, view ownership/defaults, persistence/reorder transactionality, runtime validation, admin metadata or public rendering. Run broader backend/frontend checks once near completion only when the change spans those layers.
