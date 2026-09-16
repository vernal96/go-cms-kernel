package widget

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

type Code string
type ViewCode string
type AreaCode string
type BindingID int64

type reference struct {
	code Code
}

// Ref is an immutable widget declaration reference. Its code is local to the
// module that provides the associated widget implementation; Catalog owns
// qualification into the compiled code used by runtimes, persistence and APIs.
type Ref struct {
	reference *reference
}

func NewRef(code Code) Ref {
	return Ref{reference: &reference{code: code}}
}

func (r Ref) Code() Code {
	if r.reference == nil {
		return ""
	}
	return r.reference.code
}

func (r Ref) IsZero() bool {
	return r.reference == nil
}

func (r Ref) String() string {
	return string(r.Code())
}

const (
	AreaBody    AreaCode = "body"
	AreaSidebar AreaCode = "sidebar"
	DefaultView ViewCode = "default"
)

var (
	ErrInvalidParams       = errors.New("invalid widget params")
	ErrInvalidPresentation = errors.New("invalid widget presentation")
	ErrInstanceFailed      = errors.New("widget instance failed")
)

type ModuleDescriptor struct {
	Code        string
	Label       string
	Description string
}

// View is a typed profile declaration associated with one widget reference.
// The zero value means the implicit default view in template declarations.
type View struct {
	widget Ref
	code   ViewCode
	label  string
}

func NewView(widget Ref, code ViewCode, label string) View {
	return View{widget: widget, code: code, label: label}
}

func (v View) Widget() Ref {
	return v.widget
}

func (v View) Code() ViewCode {
	return v.code
}

func (v View) Label() string {
	return v.label
}

func (v View) IsZero() bool {
	return v.widget.IsZero() && v.code == "" && v.label == ""
}

type Definition struct {
	Reference     Ref
	Code          Code
	Module        ModuleDescriptor
	Label         string
	Description   string
	Fields        []field.Definition
	EditorTabs    []field.EditorTab
	SummaryFields []string
	Views         []View
}

// Widget is a module-owned widget definition. Definition.Reference is the
// stable module-local identity. Catalog generates Definition.Code when it
// compiles the site/profile widget runtime.
type Widget interface {
	Definition() Definition
	New(map[string]any) (Instance, error)
}

// These neutral request snapshots keep rendering independent from runtime
// services and persistence models.
type SiteSnapshot struct {
	ID     int64
	Domain string
	Locale string
}

type ResourceSnapshot struct {
	ID      int64
	Title   string
	Content string
}

type RenderInput struct {
	Site     SiteSnapshot
	Resource ResourceSnapshot
}

type Instance interface {
	Render(context.Context, RenderInput) (map[string]any, error)
}

// Provider is implemented optionally by a module runtime.
type Provider interface {
	Widgets() []Widget
}

type Source struct {
	Module  ModuleDescriptor
	Widgets []Widget
}

type Presentation struct {
	View         ViewCode
	Columns      int
	MarginTop    int
	MarginBottom int
	Enabled      bool
}

func DefaultPresentation() Presentation {
	return Presentation{Columns: 12, Enabled: true}
}

func (p Presentation) Validate() error {
	if p.Columns < 1 || p.Columns > 12 {
		return fmt.Errorf("%w: columns must be between 1 and 12", ErrInvalidPresentation)
	}
	if p.MarginTop < 0 || p.MarginTop > 3 {
		return fmt.Errorf("%w: margin top must be between 0 and 3", ErrInvalidPresentation)
	}
	if p.MarginBottom < 0 || p.MarginBottom > 3 {
		return fmt.Errorf("%w: margin bottom must be between 0 and 3", ErrInvalidPresentation)
	}
	if strings.TrimSpace(string(p.View)) != string(p.View) {
		return fmt.Errorf("%w: view %q is invalid", ErrInvalidPresentation, p.View)
	}
	return nil
}

func NormalizeView(code ViewCode) ViewCode {
	if code == DefaultView {
		return ""
	}
	return code
}

func PublicView(code ViewCode) ViewCode {
	if code == "" {
		return DefaultView
	}
	return code
}

func ValidArea(code AreaCode) bool {
	return code == AreaBody || code == AreaSidebar
}

type Binding struct {
	ID            BindingID
	Code          Code
	Area          AreaCode
	Position      int
	Presentation  Presentation
	Params        map[string]any
	ParamBindings ParamBindings
}

type Order struct {
	ID       BindingID
	Area     AreaCode
	Position int
}

type Placement struct {
	Key           string
	BindingID     BindingID
	Code          Code
	Area          AreaCode
	Position      int
	Presentation  Presentation
	Params        map[string]any
	ParamBindings ParamBindings
}

type Placements struct {
	Body    []Placement
	Sidebar []Placement
}

func CloneBinding(binding Binding) Binding {
	binding.Params = cloneMap(binding.Params)
	binding.ParamBindings = CloneParamBindings(binding.ParamBindings)
	return binding
}

func CloneBindings(source []Binding) []Binding {
	if source == nil {
		return nil
	}
	result := make([]Binding, len(source))
	for index, binding := range source {
		result[index] = CloneBinding(binding)
	}
	return result
}

type Runtime struct {
	definition Definition
	schema     *field.Schema
	widget     Widget
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

func (r *Runtime) NormalizeParams(values map[string]any) (map[string]any, error) {
	if r == nil {
		return nil, errors.New("widget runtime is nil")
	}
	normalized, err := r.schema.Validate(values)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}
	return normalized, nil
}

func (r *Runtime) ValidatePresentation(presentation Presentation) error {
	if r == nil {
		return errors.New("widget runtime is nil")
	}
	presentation.View = NormalizeView(presentation.View)
	if err := presentation.Validate(); err != nil {
		return err
	}
	if presentation.View == "" {
		return nil
	}
	for _, view := range r.definition.Views {
		if view.Code() == presentation.View {
			return nil
		}
	}
	return fmt.Errorf("%w: widget %q has no view %q", ErrInvalidPresentation, r.definition.Code, presentation.View)
}

// ValidateView verifies a typed template view against the final profile
// catalog. Persisted/API presentation validation remains code-based.
func (r *Runtime) ValidateView(view View) error {
	if r == nil {
		return errors.New("widget runtime is nil")
	}
	if view.IsZero() {
		return nil
	}
	if view.Widget() != r.definition.Reference {
		return fmt.Errorf(
			"%w: view %q belongs to widget %q, not %q",
			ErrInvalidPresentation,
			view.Code(),
			view.Widget(),
			r.definition.Reference,
		)
	}
	for _, available := range r.definition.Views {
		if available == view {
			return nil
		}
	}
	return fmt.Errorf(
		"%w: widget %q has no declared view %q",
		ErrInvalidPresentation,
		r.definition.Code,
		view.Code(),
	)
}

func (r *Runtime) New(values map[string]any) (Instance, error) {
	normalized, err := r.NormalizeParams(values)
	if err != nil {
		return nil, err
	}
	instance, err := r.widget.New(normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInstanceFailed, err)
	}
	if isNil(instance) {
		return nil, fmt.Errorf("%w: widget returned nil instance", ErrInstanceFailed)
	}
	return instance, nil
}

type Catalog struct {
	order      []Code
	runtimes   map[Code]*Runtime
	references map[Ref]*Runtime
}

func Compile(sources []Source, views []View, resolver field.TypeResolver) (*Catalog, error) {
	if resolver == nil {
		return nil, errors.New("widget field type resolver is nil")
	}
	catalog := &Catalog{
		runtimes:   make(map[Code]*Runtime),
		references: make(map[Ref]*Runtime),
	}
	for sourceIndex, source := range sources {
		if err := validateModuleDescriptor(source.Module); err != nil {
			return nil, fmt.Errorf("widget source at index %d: %w", sourceIndex, err)
		}
		localCodes := make(map[Code]struct{}, len(source.Widgets))
		for widgetIndex, current := range source.Widgets {
			if isNil(current) {
				return nil, fmt.Errorf("module %q widget at index %d is nil", source.Module.Code, widgetIndex)
			}
			declaration := CloneDefinition(current.Definition())
			if declaration.Code != "" {
				return nil, fmt.Errorf(
					"module %q widget at index %d declares compiled code %q; use Reference",
					source.Module.Code,
					widgetIndex,
					declaration.Code,
				)
			}
			if declaration.Reference.IsZero() {
				return nil, fmt.Errorf("module %q widget at index %d has empty reference", source.Module.Code, widgetIndex)
			}
			localCode := declaration.Reference.Code()
			if localCode == "" || strings.TrimSpace(string(localCode)) != string(localCode) {
				return nil, fmt.Errorf("module %q widget at index %d has invalid reference code %q", source.Module.Code, widgetIndex, localCode)
			}
			if declaration.Label == "" || strings.TrimSpace(declaration.Label) != declaration.Label {
				return nil, fmt.Errorf("module %q widget %q has invalid label %q", source.Module.Code, localCode, declaration.Label)
			}
			if declaration.Description == "" || strings.TrimSpace(declaration.Description) != declaration.Description {
				return nil, fmt.Errorf("module %q widget %q has invalid description %q", source.Module.Code, localCode, declaration.Description)
			}
			if _, exists := localCodes[localCode]; exists {
				return nil, fmt.Errorf("module %q contains duplicate widget code %q", source.Module.Code, localCode)
			}
			localCodes[localCode] = struct{}{}
			globalCode := Code(source.Module.Code + "_" + string(localCode))
			if _, exists := catalog.runtimes[globalCode]; exists {
				return nil, fmt.Errorf("duplicate global widget code %q", globalCode)
			}
			if _, exists := catalog.references[declaration.Reference]; exists {
				return nil, fmt.Errorf("widget reference %q is provided more than once", declaration.Reference)
			}
			schema, err := field.Compile(declaration.Fields, resolver)
			if err != nil {
				return nil, fmt.Errorf("compile widget %q fields: %w", globalCode, err)
			}
			if err := validateEditorMetadata(declaration); err != nil {
				return nil, fmt.Errorf("compile widget %q editor metadata: %w", globalCode, err)
			}
			declaration.Code = globalCode
			declaration.Module = source.Module
			declaration.Views = nil
			catalog.order = append(catalog.order, globalCode)
			runtime := &Runtime{definition: declaration, schema: schema, widget: current}
			catalog.runtimes[globalCode] = runtime
			catalog.references[declaration.Reference] = runtime
		}
	}
	if err := catalog.addViews(views); err != nil {
		return nil, err
	}
	return catalog, nil
}

func validateModuleDescriptor(descriptor ModuleDescriptor) error {
	if descriptor.Code == "" || strings.TrimSpace(descriptor.Code) != descriptor.Code {
		return fmt.Errorf("invalid module code %q", descriptor.Code)
	}
	if descriptor.Label == "" || strings.TrimSpace(descriptor.Label) != descriptor.Label {
		return fmt.Errorf("module %q has invalid label %q", descriptor.Code, descriptor.Label)
	}
	if strings.TrimSpace(descriptor.Description) != descriptor.Description {
		return fmt.Errorf("module %q has invalid description %q", descriptor.Code, descriptor.Description)
	}
	return nil
}

func validateEditorMetadata(definition Definition) error {
	if err := field.ValidateEditorTabs(definition.Fields, definition.EditorTabs); err != nil {
		return err
	}
	fields := make(map[string]struct{}, len(definition.Fields))
	for _, current := range definition.Fields {
		fields[current.Key] = struct{}{}
	}
	seenSummary := make(map[string]struct{}, len(definition.SummaryFields))
	for _, fieldCode := range definition.SummaryFields {
		if _, exists := fields[fieldCode]; !exists {
			return fmt.Errorf("summary references unknown field %q", fieldCode)
		}
		if _, exists := seenSummary[fieldCode]; exists {
			return fmt.Errorf("summary field %q is duplicated", fieldCode)
		}
		seenSummary[fieldCode] = struct{}{}
	}
	return nil
}

func (c *Catalog) addViews(views []View) error {
	seen := make(map[Ref]map[ViewCode]struct{}, len(views))
	for index, declaration := range views {
		if declaration.IsZero() {
			return fmt.Errorf("widget view at index %d is empty", index)
		}
		if declaration.Widget().IsZero() {
			return fmt.Errorf("widget view at index %d has empty widget reference", index)
		}
		if declaration.Code() == "" || declaration.Code() == DefaultView || strings.TrimSpace(string(declaration.Code())) != string(declaration.Code()) {
			return fmt.Errorf("widget %q has invalid custom view code %q", declaration.Widget(), declaration.Code())
		}
		if declaration.Label() == "" || strings.TrimSpace(declaration.Label()) != declaration.Label() {
			return fmt.Errorf("widget %q view %q has invalid label %q", declaration.Widget(), declaration.Code(), declaration.Label())
		}
		runtime, exists := c.references[declaration.Widget()]
		if !exists {
			return fmt.Errorf("custom view references unavailable widget %q", declaration.Widget())
		}
		byCode := seen[declaration.Widget()]
		if byCode == nil {
			byCode = make(map[ViewCode]struct{})
			seen[declaration.Widget()] = byCode
		}
		if _, exists := byCode[declaration.Code()]; exists {
			return fmt.Errorf("widget %q has duplicate custom view %q", declaration.Widget(), declaration.Code())
		}
		byCode[declaration.Code()] = struct{}{}
		runtime.definition.Views = append(runtime.definition.Views, declaration)
	}
	return nil
}

func (c *Catalog) Widget(code Code) (*Runtime, bool) {
	if c == nil {
		return nil, false
	}
	runtime, exists := c.runtimes[code]
	return runtime, exists
}

// Resolve translates a typed declaration reference into its final compiled
// widget runtime and globally qualified code.
func (c *Catalog) Resolve(reference Ref) (*Runtime, Code, bool) {
	if c == nil {
		return nil, "", false
	}
	runtime, exists := c.references[reference]
	if !exists {
		return nil, "", false
	}
	return runtime, runtime.definition.Code, true
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
	definition.SummaryFields = append([]string(nil), definition.SummaryFields...)
	definition.Views = append([]View(nil), definition.Views...)
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

func CloneViews(source []View) []View {
	return append([]View(nil), source...)
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneParamValue(value)
	}
	return result
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
