package template

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

type Code string

// Item seals project-facing layouts to typed declarations owned by this
// package. Compiler-only discriminators never leak into project code.
type Item interface {
	isTemplateItem()
}

// Widget declares one static widget. Omitted presentation values mean the
// implicit defaults: default view, 12 columns, zero margins and enabled.
type Widget struct {
	Widget        widget.Ref
	View          widget.View
	Columns       int
	MarginTop     int
	MarginBottom  int
	Params        map[string]any
	ParamBindings widget.ParamBindings
}

func (Widget) isTemplateItem() {}

// ResourceWidgets marks where persisted resource widget bindings are inserted.
type ResourceWidgets struct{}

func (ResourceWidgets) isTemplateItem() {}

type Layout struct {
	Body    []Item
	Sidebar []Item
}

type Definition struct {
	Code       Code
	Label      string
	Icon       string
	Fields     []field.Definition
	EditorTabs []field.EditorTab
	Layout     Layout
}

type compiledItemKind uint8

const (
	compiledWidget compiledItemKind = iota + 1
	compiledResourceWidgets
)

type compiledItem struct {
	kind          compiledItemKind
	key           string
	code          widget.Code
	presentation  widget.Presentation
	params        map[string]any
	paramBindings widget.ParamBindings
}

type compiledLayout struct {
	body    []compiledItem
	sidebar []compiledItem
}

type Runtime struct {
	definition Definition
	schema     *field.Schema
	compiled   *compiledLayout
}

func (r *Runtime) Definition() Definition {
	if r == nil {
		return Definition{}
	}
	return CloneDefinition(r.definition)
}

func (r *Runtime) FieldSchema() *field.Schema {
	if r == nil {
		return nil
	}
	return r.schema
}

type Catalog struct {
	order    []Code
	runtimes map[Code]*Runtime
}

func Compile(definitions []Definition, resolver field.TypeResolver) (*Catalog, error) {
	if resolver == nil {
		return nil, errors.New("template field type resolver is nil")
	}

	definitions = CloneDefinitions(definitions)
	catalog := &Catalog{
		order:    make([]Code, 0, len(definitions)),
		runtimes: make(map[Code]*Runtime, len(definitions)),
	}

	for index, definition := range definitions {
		if definition.Code == "" || strings.TrimSpace(string(definition.Code)) != string(definition.Code) {
			return nil, fmt.Errorf("template at index %d has invalid code %q", index, definition.Code)
		}
		if definition.Label == "" || strings.TrimSpace(definition.Label) != definition.Label {
			return nil, fmt.Errorf("template %q has invalid label %q", definition.Code, definition.Label)
		}
		definition.Icon = strings.TrimSpace(definition.Icon)
		if strings.ContainsAny(definition.Icon, " /\\") {
			return nil, fmt.Errorf("template %q has invalid icon %q", definition.Code, definition.Icon)
		}
		if _, exists := catalog.runtimes[definition.Code]; exists {
			return nil, fmt.Errorf("duplicate template code %q", definition.Code)
		}
		if err := validateLayout(definition.Code, definition.Layout); err != nil {
			return nil, err
		}

		schema, err := field.CompilePersistent(definition.Fields, resolver)
		if err != nil {
			return nil, fmt.Errorf("compile template %q fields: %w", definition.Code, err)
		}
		if err := field.ValidateEditorTabs(definition.Fields, definition.EditorTabs); err != nil {
			return nil, fmt.Errorf("compile template %q editor tabs: %w", definition.Code, err)
		}

		catalog.order = append(catalog.order, definition.Code)
		catalog.runtimes[definition.Code] = &Runtime{definition: definition, schema: schema}
	}

	return catalog, nil
}

func validateLayout(code Code, layout Layout) error {
	for _, area := range []struct {
		code  widget.AreaCode
		items []Item
	}{
		{code: widget.AreaBody, items: layout.Body},
		{code: widget.AreaSidebar, items: layout.Sidebar},
	} {
		slots := 0
		for index, item := range area.items {
			switch declaration := item.(type) {
			case ResourceWidgets:
				slots++
			case Widget:
				if declaration.Widget.IsZero() {
					return fmt.Errorf("template %q %s widget at index %d has empty reference", code, area.code, index)
				}
				if err := effectivePresentation(declaration).Validate(); err != nil {
					return fmt.Errorf("template %q %s widget at index %d: %w", code, area.code, index, err)
				}
			default:
				return fmt.Errorf("template %q %s item at index %d has unsupported type %T", code, area.code, index, item)
			}
		}
		if slots > 1 {
			return fmt.Errorf("template %q %s contains duplicate resource widget slots", code, area.code)
		}
	}
	return nil
}

func effectivePresentation(declaration Widget) widget.Presentation {
	columns := declaration.Columns
	if columns == 0 {
		columns = 12
	}
	return widget.Presentation{
		View:         declaration.View.Code(),
		Columns:      columns,
		MarginTop:    declaration.MarginTop,
		MarginBottom: declaration.MarginBottom,
		Enabled:      true,
	}
}

func (r *Runtime) SupportsResourceWidgets() bool {
	return len(r.ResourceAreas()) > 0
}

func (r *Runtime) ResourceAreas() []widget.AreaCode {
	if r == nil {
		return nil
	}
	result := make([]widget.AreaCode, 0, 2)
	if hasResourceWidgets(r.definition.Layout.Body) {
		result = append(result, widget.AreaBody)
	}
	if hasResourceWidgets(r.definition.Layout.Sidebar) {
		result = append(result, widget.AreaSidebar)
	}
	return result
}

func (r *Runtime) AllowsResourceArea(area widget.AreaCode) bool {
	for _, current := range r.ResourceAreas() {
		if current == area {
			return true
		}
	}
	return false
}

func hasResourceWidgets(items []Item) bool {
	for _, item := range items {
		if _, ok := item.(ResourceWidgets); ok {
			return true
		}
	}
	return false
}

type widgetResolver interface {
	Resolve(widget.Ref) (*widget.Runtime, widget.Code, bool)
}

// CompileWidgets is the site-scoped step performed after module runtimes have
// contributed the final widget catalog. It resolves references to compiled
// codes and materializes static presentation defaults outside the request path.
func (c *Catalog) CompileWidgets(widgets widgetResolver) (*Catalog, error) {
	if c == nil || widgets == nil {
		return nil, errors.New("template widget catalog is unavailable")
	}
	result := &Catalog{
		order:    append([]Code(nil), c.order...),
		runtimes: make(map[Code]*Runtime, len(c.runtimes)),
	}
	for _, code := range c.order {
		source := c.runtimes[code]
		layout, err := compileLayout(code, source.definition.Layout, widgets, source.schema)
		if err != nil {
			return nil, err
		}
		result.runtimes[code] = &Runtime{
			definition: CloneDefinition(source.definition),
			schema:     source.schema,
			compiled:   layout,
		}
	}
	return result, nil
}

func compileLayout(code Code, layout Layout, widgets widgetResolver, schema *field.Schema) (*compiledLayout, error) {
	body, err := compileArea(code, widget.AreaBody, layout.Body, widgets, schema)
	if err != nil {
		return nil, err
	}
	sidebar, err := compileArea(code, widget.AreaSidebar, layout.Sidebar, widgets, schema)
	if err != nil {
		return nil, err
	}
	return &compiledLayout{body: body, sidebar: sidebar}, nil
}

func compileArea(
	templateCode Code,
	area widget.AreaCode,
	items []Item,
	widgets widgetResolver,
	schema *field.Schema,
) ([]compiledItem, error) {
	result := make([]compiledItem, 0, len(items))
	for index, item := range items {
		switch declaration := item.(type) {
		case ResourceWidgets:
			result = append(result, compiledItem{kind: compiledResourceWidgets})
		case Widget:
			runtime, code, exists := widgets.Resolve(declaration.Widget)
			if !exists {
				return nil, fmt.Errorf(
					"template %q %s widget at index %d references unavailable widget %q",
					templateCode,
					area,
					index,
					declaration.Widget,
				)
			}
			if err := runtime.ValidateView(declaration.View); err != nil {
				return nil, fmt.Errorf("template %q %s widget at index %d: %w", templateCode, area, index, err)
			}
			presentation := effectivePresentation(declaration)
			if err := runtime.ValidatePresentation(presentation); err != nil {
				return nil, fmt.Errorf("template %q %s widget at index %d: %w", templateCode, area, index, err)
			}
			params, err := runtime.NormalizeConfiguration(declaration.Params, declaration.ParamBindings, schema)
			if err != nil {
				return nil, fmt.Errorf("template %q %s widget at index %d: %w", templateCode, area, index, err)
			}
			result = append(result, compiledItem{
				kind:          compiledWidget,
				key:           fmt.Sprintf("template:%s:%s:%d", templateCode, area, index),
				code:          code,
				presentation:  presentation,
				params:        params,
				paramBindings: widget.CloneParamBindings(declaration.ParamBindings),
			})
		default:
			return nil, fmt.Errorf("template %q %s item at index %d has unsupported type %T", templateCode, area, index, item)
		}
	}
	return result, nil
}

func Compose(runtime *Runtime, bindings []widget.Binding) (widget.Placements, error) {
	if runtime == nil {
		return widget.Placements{}, errors.New("template runtime is nil")
	}
	if runtime.compiled == nil {
		return widget.Placements{}, errors.New("template widget layout is not compiled")
	}
	byArea := map[widget.AreaCode][]widget.Binding{
		widget.AreaBody: {}, widget.AreaSidebar: {},
	}
	for _, binding := range bindings {
		if !widget.ValidArea(binding.Area) {
			return widget.Placements{}, fmt.Errorf("resource widget %d has invalid area %q", binding.ID, binding.Area)
		}
		if !runtime.AllowsResourceArea(binding.Area) {
			return widget.Placements{}, fmt.Errorf("template %q does not allow resource widgets in %q", runtime.definition.Code, binding.Area)
		}
		byArea[binding.Area] = append(byArea[binding.Area], widget.CloneBinding(binding))
	}
	for area := range byArea {
		sort.SliceStable(byArea[area], func(left, right int) bool {
			return byArea[area][left].Position < byArea[area][right].Position
		})
		for index, binding := range byArea[area] {
			if binding.Position != index {
				return widget.Placements{}, fmt.Errorf("resource widget %d in %q has position %d instead of %d", binding.ID, area, binding.Position, index)
			}
		}
	}
	body, err := composeArea(widget.AreaBody, runtime.compiled.body, byArea[widget.AreaBody])
	if err != nil {
		return widget.Placements{}, err
	}
	sidebar, err := composeArea(widget.AreaSidebar, runtime.compiled.sidebar, byArea[widget.AreaSidebar])
	if err != nil {
		return widget.Placements{}, err
	}
	return widget.Placements{Body: body, Sidebar: sidebar}, nil
}

func composeArea(area widget.AreaCode, items []compiledItem, bindings []widget.Binding) ([]widget.Placement, error) {
	result := make([]widget.Placement, 0, len(items)+len(bindings))
	for _, item := range items {
		switch item.kind {
		case compiledWidget:
			result = append(result, widget.Placement{
				Key: item.key, Code: item.code, Area: area,
				Presentation: item.presentation, Params: cloneMap(item.params), ParamBindings: widget.CloneParamBindings(item.paramBindings),
			})
		case compiledResourceWidgets:
			for _, binding := range bindings {
				result = append(result, widget.Placement{
					Key: fmt.Sprintf("resource-widget-%d", binding.ID), BindingID: binding.ID,
					Code: binding.Code, Area: area, Position: binding.Position,
					Presentation:  binding.Presentation,
					Params:        cloneMap(binding.Params),
					ParamBindings: widget.CloneParamBindings(binding.ParamBindings),
				})
			}
		default:
			return nil, fmt.Errorf("template area %q contains invalid compiled item", area)
		}
	}
	return result, nil
}

func (c *Catalog) Template(code Code) (*Runtime, bool) {
	if c == nil {
		return nil, false
	}
	runtime, exists := c.runtimes[code]
	return runtime, exists
}

func (c *Catalog) Definitions() []Definition {
	if c == nil {
		return nil
	}
	result := make([]Definition, 0, len(c.order))
	for _, code := range c.order {
		result = append(result, CloneDefinition(c.runtimes[code].definition))
	}
	return result
}

func CloneDefinition(definition Definition) Definition {
	definition.Fields = field.CloneDefinitions(definition.Fields)
	definition.EditorTabs = field.CloneEditorTabs(definition.EditorTabs)
	definition.Layout = Layout{
		Body:    cloneItems(definition.Layout.Body),
		Sidebar: cloneItems(definition.Layout.Sidebar),
	}
	return definition
}

func CloneDefinitions(source []Definition) []Definition {
	if source == nil {
		return nil
	}
	result := make([]Definition, len(source))
	for index, definition := range source {
		result[index] = CloneDefinition(definition)
	}
	return result
}

func cloneItems(source []Item) []Item {
	if source == nil {
		return nil
	}
	result := make([]Item, len(source))
	for index, item := range source {
		switch declaration := item.(type) {
		case Widget:
			declaration.Params = cloneMap(declaration.Params)
			declaration.ParamBindings = widget.CloneParamBindings(declaration.ParamBindings)
			result[index] = declaration
		case ResourceWidgets:
			result[index] = declaration
		default:
			result[index] = item
		}
	}
	return result
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
