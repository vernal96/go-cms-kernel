# Widget areas

Templates declare an ordered `template.Layout` of `template.Area` values:

```go
Layout: template.Layout{
    {Code: "main", Label: "Основная область", AdminSpan: 16},
    {Code: "aside", Label: "Боковая область", AdminSpan: 8},
    {Code: "footer", Label: "Подвал"},
},
```

Codes must match `[a-z][a-z0-9_-]*` and be unique within the template. `default`
is reserved. Labels are required. `AdminSpan` is 1–24; zero/omitted means 24.
The editor uses Element Plus rows/columns, keeps declaration order, wraps at
24 columns and uses full-width columns below the `md` breakpoint (992px).
Container widths are independent of widget presentation `columns` (1–12).

Nil `Items` inserts a `template.ResourceWidgets{}` slot. An explicit `Items`
list retains exact ordering of static typed widgets and at most one slot:

```go
{Code: "main", Label: "Страница", Items: []template.Item{
    template.Widget{Widget: corewidgets.Content},
    template.ResourceWidgets{},
}},
```

An explicit empty list or static-only list has no resource-widget slot.
Its container remains visible, but does not offer adding resource widgets.

## Default and removed areas

With no declared areas, `default` (label `Страница сайта`, width 24) is always
available, including when empty. With declared areas it appears last only
while it holds bindings, including disabled ones. Existing bindings in areas
that are missing or no longer have a resource slot are displayed/rendered in
`default`. Their stored `area` remains unchanged until an explicit move.
Restoring an area therefore restores its bindings automatically.

Default bindings come first, then recovered bindings ordered by original area
code, position and ID. Composition never writes to the database. Cached
bindings retain their original area; resolution uses the current template.
Resource/template saves, history restore and site transfer preserve those codes.

## HTTP contracts

Management `widget_areas` contains the ordered declared descriptors (or a single
system default descriptor for a template without areas):

```json
[{"code":"main","label":"Основная область","admin_span":16,"supports_resource_widgets":true}]
```

Management binding `area` is the persisted code. Clients group bindings whose
code is absent from editable descriptors into `default`, appending that container
when needed. CRUD URLs and optimistic version checks are unchanged. Creation
requires an editable declared area or `default`. Full reorder requests preserve
original area codes of untouched bindings; a changed code must be a valid target.
Moving a recovered widget into default explicitly stores `area: "default"`.
Recovered bindings follow the explicit default bindings until moved.

Public `widgets` is a map from area code to the existing rendered-widget arrays.
Declared empty containers remain `[]`; `default` follows the rules above.
Disabled bindings make default visible but are not rendered. A resource without
a template returns an empty map. Container labels and widths are never public.
JSON object member order is not a layout contract.

This replaces the fixed Body/Sidebar declarations and management metadata.
The development migration is corrected directly; an already-migrated development
database must be recreated before testing the new schema. No legacy aliases or
conversion commands are provided.
