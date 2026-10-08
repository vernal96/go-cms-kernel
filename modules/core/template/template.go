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

// Area is a named widget container. Nil Items implies a resource-widget slot;
// an explicit empty slice declares an empty, non-editable container.
type Area struct {
	Code      widget.AreaCode
	Label     string
	AdminSpan int
	Items     []Item
}

type Layout []Area

// AreaDescriptor describes editor layout, never public widget output.
type AreaDescriptor struct {
	Code                    widget.AreaCode `json:"code"`
	Label                   string          `json:"label"`
	AdminSpan               int             `json:"admin_span"`
	SupportsResourceWidgets bool            `json:"supports_resource_widgets"`
}

func areaItems(area Area) []Item {
	if area.Items == nil {
		return []Item{ResourceWidgets{}}
	}
	return area.Items
}

func defaultArea() Area {
	return Area{Code: widget.AreaDefault, Label: "Страница сайта", AdminSpan: 24}
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

type compiledLayout map[widget.AreaCode][]compiledItem

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
	seen := make(map[widget.AreaCode]bool)
	for _, area := range layout {
		if !widget.ValidArea(area.Code) || area.Code == widget.AreaDefault || seen[area.Code] {
			return fmt.Errorf("template %q has invalid or duplicate area %q", code, area.Code)
		}
		seen[area.Code] = true
		if strings.TrimSpace(area.Label) == "" || area.Label != strings.TrimSpace(area.Label) {
			return fmt.Errorf("template %q area %q has invalid label", code, area.Code)
		}
		if area.AdminSpan < 0 || area.AdminSpan > 24 {
			return fmt.Errorf("template %q area %q admin span must be between 1 and 24", code, area.Code)
		}
		slots := 0
		for index, item := range areaItems(area) {
			switch declaration := item.(type) {
			case ResourceWidgets:
				slots++
			case Widget:
				if declaration.Widget.IsZero() {
					return fmt.Errorf("template %q %s widget at index %d has empty reference", code, area.Code, index)
				}
				if err := effectivePresentation(declaration).Validate(); err != nil {
					return fmt.Errorf("template %q %s widget at index %d: %w", code, area.Code, index, err)
				}
			default:
				return fmt.Errorf("template %q %s item at index %d has unsupported type %T", code, area.Code, index, item)
			}
		}
		if slots > 1 {
			return fmt.Errorf("template %q %s contains duplicate resource widget slots", code, area.Code)
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

// Every template can retain recovered bindings in default, even when its
// declared containers contain only static widgets.
func (r *Runtime) SupportsResourceWidgets() bool { return r != nil }

func (r *Runtime) Areas() []AreaDescriptor {
	if r == nil {
		return nil
	}
	areas := r.definition.Layout
	if len(areas) == 0 {
		areas = Layout{defaultArea()}
	}
	result := make([]AreaDescriptor, 0, len(areas))
	for _, area := range areas {
		span := area.AdminSpan
		if span == 0 {
			span = 24
		}
		result = append(result, AreaDescriptor{Code: area.Code, Label: area.Label, AdminSpan: span, SupportsResourceWidgets: hasResourceWidgets(areaItems(area))})
	}
	return result
}

func (r *Runtime) AllowsResourceArea(area widget.AreaCode) bool {
	if r == nil {
		return false
	}
	if area == widget.AreaDefault {
		return true
	}
	for _, current := range r.definition.Layout {
		if current.Code == area {
			return hasResourceWidgets(areaItems(current))
		}
	}
	return false
}

// ResolveArea projects a stored binding without changing its original area.
func (r *Runtime) ResolveArea(area widget.AreaCode) widget.AreaCode {
	if r.AllowsResourceArea(area) {
		return area
	}
	return widget.AreaDefault
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
	result := make(compiledLayout)
	for _, area := range layout {
		items, err := compileArea(code, area.Code, areaItems(area), widgets, schema)
		if err != nil {
			return nil, err
		}
		result[area.Code] = items
	}
	result[widget.AreaDefault] = []compiledItem{{kind: compiledResourceWidgets}}
	return &result, nil
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
	byArea := make(map[widget.AreaCode][]widget.Binding)
	for _, binding := range bindings {
		if !widget.ValidArea(binding.Area) || binding.Position < 0 {
			return nil, fmt.Errorf("resource widget %d has invalid area or position", binding.ID)
		}
		area := runtime.ResolveArea(binding.Area)
		byArea[area] = append(byArea[area], widget.CloneBinding(binding))
	}
	// Validate positions in their stored containers before merging recovered areas.
	storedPositions := make(map[widget.AreaCode][]int)
	for _, binding := range bindings {
		storedPositions[binding.Area] = append(storedPositions[binding.Area], binding.Position)
	}
	for area, positions := range storedPositions {
		sort.Ints(positions)
		for i, position := range positions {
			if position != i {
				return nil, fmt.Errorf("resource widgets in %q have non-contiguous positions", area)
			}
		}
	}
	result := make(widget.Placements)
	for _, descriptor := range runtime.Areas() {
		result[descriptor.Code] = []widget.Placement{}
	}
	if len(byArea[widget.AreaDefault]) > 0 {
		result[widget.AreaDefault] = []widget.Placement{}
	}
	for area := range result {
		current := byArea[area]
		sort.SliceStable(current, func(i, j int) bool {
			left, right := current[i], current[j]
			if left.Area != right.Area {
				if left.Area == widget.AreaDefault {
					return true
				}
				if right.Area == widget.AreaDefault {
					return false
				}
				return left.Area < right.Area
			}
			if left.Position != right.Position {
				return left.Position < right.Position
			}
			return left.ID < right.ID
		})
		for i := range current {
			current[i].Position = i
		}
		placements, err := composeArea(area, (*runtime.compiled)[area], current)
		if err != nil {
			return nil, err
		}
		result[area] = placements
	}
	return result, nil
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
	definition.Layout = append(Layout(nil), definition.Layout...)
	for i := range definition.Layout {
		definition.Layout[i].Items = cloneItems(definition.Layout[i].Items)
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
