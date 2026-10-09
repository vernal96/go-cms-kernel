package template

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

type testResolver map[field.TypeCode]field.Type

type transientType struct{}

func (transientType) Code() field.TypeCode { return "transient" }
func (transientType) Compile(field.CompileContext, any) (field.ValueType, error) {
	return transientValue{}, nil
}

type transientValue struct{}

func (transientValue) Normalize(value any) (any, error) { return value, nil }
func (transientValue) Empty(value any) bool             { return value == nil }
func (transientValue) Validate(any) error               { return nil }
func (transientValue) Example() any                     { return "temporary" }

func (r testResolver) FieldType(code field.TypeCode) (field.Type, bool) {
	value, exists := r[code]
	return value, exists
}

func TestResourceTemplateRequiresPersistentFieldType(t *testing.T) {
	fields := resolver()
	fields["transient"] = transientType{}
	definition := field.Definition{Key: "session", Type: "transient", Label: "Session"}
	if _, err := field.Compile([]field.Definition{definition}, fields); err != nil {
		t.Fatalf("generic field compilation rejected transient type: %v", err)
	}
	_, err := Compile([]Definition{{Code: "article", Label: "Article", Fields: []field.Definition{definition}}}, fields)
	if err == nil || !strings.Contains(err.Error(), `template "article"`) || !strings.Contains(err.Error(), `field "session"`) || !strings.Contains(err.Error(), `type "transient"`) {
		t.Fatalf("persistent template error = %v", err)
	}
}

func TestCompilePreservesOpaqueIconStrings(t *testing.T) {
	for _, test := range []struct {
		name string
		icon string
	}{
		{name: "empty", icon: ""},
		{name: "arbitrary", icon: "not/a-valid-icon"},
		{name: "bare icon name", icon: "house"},
		{name: "whitespace", icon: "   "},
	} {
		t.Run(test.name, func(t *testing.T) {
			icon := test.icon
			catalog, err := Compile([]Definition{{Code: "page", Label: "Page", Icon: icon}}, resolver())
			if err != nil {
				t.Fatalf("compile icon %q: %v", icon, err)
			}
			runtime, exists := catalog.Template("page")
			if !exists || runtime.Definition().Icon != icon {
				t.Fatalf("compiled icon = %q, want %q", runtime.Definition().Icon, icon)
			}
		})
	}
}

func TestResourceTemplateValidatesAndClonesEditorTabs(t *testing.T) {
	definition := Definition{
		Code:  "article",
		Label: "Article",
		Fields: []field.Definition{
			{Key: "title", Type: field.TypeString, Label: "Title"},
			{Key: "color", Type: field.TypeString, Label: "Color"},
		},
		EditorTabs: []field.EditorTab{
			{Code: "content", Label: "Content", Fields: []string{"title"}},
			{Code: "appearance", Label: "Appearance", Fields: []string{"color"}},
		},
	}
	catalog, err := Compile([]Definition{definition}, resolver())
	if err != nil {
		t.Fatal(err)
	}
	definition.EditorTabs[0].Fields[0] = "changed"
	runtime, _ := catalog.Template("article")
	exposed := runtime.Definition()
	exposed.EditorTabs[0].Fields[0] = "mutated"
	if frozen := runtime.Definition(); frozen.EditorTabs[0].Fields[0] != "title" {
		t.Fatalf("editor tabs share mutable state: %#v", frozen.EditorTabs)
	}

	_, err = Compile([]Definition{{
		Code: "invalid", Label: "Invalid",
		Fields:     []field.Definition{{Key: "title", Type: field.TypeString, Label: "Title"}},
		EditorTabs: []field.EditorTab{{Code: "main", Label: "Main"}},
	}}, resolver())
	if err == nil || !strings.Contains(err.Error(), "not assigned") {
		t.Fatalf("invalid editor tabs error = %v", err)
	}
}

func resolver() testResolver {
	result := testResolver{}
	for _, value := range field.StandardTypes() {
		result[value.Code()] = value
	}
	return result
}

type catalogWidget struct {
	reference widget.Ref
}

func (w catalogWidget) Definition() widget.Definition {
	return widget.Definition{
		Reference:   w.reference,
		Label:       "Test widget",
		Description: "Template test widget",
	}
}

func (catalogWidget) New(map[string]any) (widget.Instance, error) {
	return catalogWidgetInstance{}, nil
}

type catalogWidgetInstance struct{}

func (catalogWidgetInstance) Render(context.Context, widget.RenderInput) (map[string]any, error) {
	return map[string]any{}, nil
}

func compileWidgets(t *testing.T, refs []widget.Ref, views []widget.View) *widget.Catalog {
	t.Helper()
	declarations := make([]widget.Widget, len(refs))
	for index, ref := range refs {
		declarations[index] = catalogWidget{reference: ref}
	}
	catalog, err := widget.Compile([]widget.Source{{
		Module:  widget.ModuleDescriptor{Code: "core", Label: "Core"},
		Widgets: declarations,
	}}, views, resolver())
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestCompileUsesTypedResourceWidgetsAndRejectsDuplicateSlot(t *testing.T) {
	definition := Definition{
		Code: "page", Label: "Page",
		Layout: Layout{{Code: "body", Label: "Основная область", Items: []Item{ResourceWidgets{}}}},
	}
	catalog, err := Compile([]Definition{definition}, resolver())
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := catalog.Template("page")
	if _, ok := runtime.Definition().Layout[0].Items[0].(ResourceWidgets); !ok {
		t.Fatalf("layout item = %T", runtime.Definition().Layout[0].Items[0])
	}

	_, err = Compile([]Definition{{
		Code: "duplicate", Label: "Duplicate",
		Layout: Layout{{Code: "body", Label: "Основная область", Items: []Item{ResourceWidgets{}, ResourceWidgets{}}}},
	}}, resolver())
	if err == nil || !strings.Contains(err.Error(), "duplicate resource widget slots") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompileWidgetsResolvesTypedReferencesDefaultsAndGeneratedKeys(t *testing.T) {
	before := widget.NewRef("before")
	after := widget.NewRef("after")
	navigation := widget.NewRef("navigation")
	catalog, err := Compile([]Definition{{
		Code: "page", Label: "Page",
		Layout: Layout{
			{Code: "body", Label: "Основная область", Items: []Item{
				Widget{Widget: before},
				ResourceWidgets{},
				Widget{Widget: after, Columns: 6, MarginTop: 1, MarginBottom: 2},
			}},
			{Code: "sidebar", Label: "Боковая область", Items: []Item{
				Widget{Widget: navigation},
				ResourceWidgets{},
			}},
		},
	}}, resolver())
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := catalog.CompileWidgets(compileWidgets(t, []widget.Ref{before, after, navigation}, nil))
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := compiled.Template("page")
	presentation := widget.DefaultPresentation()
	placements, err := Compose(runtime, []widget.Binding{
		{ID: 22, Code: "core_quote", Area: "body", Position: 0, Presentation: presentation},
		{ID: 41, Code: "core_contact", Area: "sidebar", Position: 0, Presentation: presentation},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := placementCodes(placements["body"]); got != "core_before,core_quote,core_after" {
		t.Fatalf("body = %s", got)
	}
	if got := placementCodes(placements["sidebar"]); got != "core_navigation,core_contact" {
		t.Fatalf("sidebar = %s", got)
	}
	if placements["body"][0].Key != "template:page:body:0" ||
		placements["body"][2].Key != "template:page:body:2" ||
		placements["sidebar"][0].Key != "template:page:sidebar:0" {
		t.Fatalf("static keys = %#v / %#v", placements["body"], placements["sidebar"])
	}
	defaults := placements["body"][0].Presentation
	if defaults.View != "" || defaults.Columns != 12 || defaults.MarginTop != 0 ||
		defaults.MarginBottom != 0 || !defaults.Enabled {
		t.Fatalf("implicit presentation = %#v", defaults)
	}
	custom := placements["body"][2].Presentation
	if custom.Columns != 6 || custom.MarginTop != 1 || custom.MarginBottom != 2 || !custom.Enabled {
		t.Fatalf("custom presentation = %#v", custom)
	}
	if placements["body"][1].Key != "resource-widget-22" || placements["sidebar"][1].Key != "resource-widget-41" {
		t.Fatalf("resource keys = %#v / %#v", placements["body"], placements["sidebar"])
	}
}

func TestAreasDescribeCompiledWidgetOrderWithoutConfiguration(t *testing.T) {
	before := widget.NewRef("before")
	after := before
	staticOnly := widget.NewRef("static_only")
	catalog, err := Compile([]Definition{{
		Code: "page", Label: "Page",
		Layout: Layout{
			{Code: "body", Label: "Body", Items: []Item{
				Widget{Widget: before},
				ResourceWidgets{},
				Widget{Widget: after},
			}},
			{Code: "static", Label: "Static only", Items: []Item{Widget{Widget: staticOnly}}},
		},
	}}, resolver())
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := catalog.CompileWidgets(compileWidgets(t, []widget.Ref{before, staticOnly}, nil))
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := compiled.Template("page")
	areas := runtime.Areas()
	want := []AreaDescriptor{
		{Code: "body", Label: "Body", AdminSpan: 24, SupportsResourceWidgets: true, Items: []AreaItem{
			{Kind: "widget", Code: "core_before"},
			{Kind: "resource_widgets"},
			{Kind: "widget", Code: "core_before"},
		}},
		{Code: "static", Label: "Static only", AdminSpan: 24, Items: []AreaItem{
			{Kind: "widget", Code: "core_static_only"},
		}},
	}
	if !reflect.DeepEqual(areas, want) {
		t.Fatalf("area metadata = %#v, want %#v", areas, want)
	}
}

func TestCompileWidgetsUsesTypedViewAndRejectsWrongOwner(t *testing.T) {
	content := widget.NewRef("content")
	gallery := widget.NewRef("gallery")
	article := widget.NewView(content, "article", "Article")
	slider := widget.NewView(gallery, "slider", "Slider")

	catalog, err := Compile([]Definition{{
		Code: "page", Label: "Page",
		Layout: Layout{{Code: "body", Label: "Основная область", Items: []Item{Widget{Widget: content, View: article}}}},
	}}, resolver())
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := catalog.CompileWidgets(compileWidgets(t, []widget.Ref{content, gallery}, []widget.View{article, slider}))
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := compiled.Template("page")
	placements, err := Compose(runtime, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(placements["body"]) != 1 || placements["body"][0].Presentation.View != "article" {
		t.Fatalf("placements = %#v", placements["body"])
	}

	invalid, err := Compile([]Definition{{
		Code: "invalid", Label: "Invalid",
		Layout: Layout{{Code: "body", Label: "Основная область", Items: []Item{Widget{Widget: content, View: slider}}}},
	}}, resolver())
	if err != nil {
		t.Fatal(err)
	}
	_, err = invalid.CompileWidgets(compileWidgets(t, []widget.Ref{content, gallery}, []widget.View{article, slider}))
	if err == nil || !strings.Contains(err.Error(), "belongs to widget") {
		t.Fatalf("wrong-owner error = %v", err)
	}
}

func TestCompileWidgetsRejectsUndeclaredCustomView(t *testing.T) {
	content := widget.NewRef("content")
	article := widget.NewView(content, "article", "Article")
	catalog, err := Compile([]Definition{{
		Code: "page", Label: "Page",
		Layout: Layout{{Code: "body", Label: "Основная область", Items: []Item{Widget{Widget: content, View: article}}}},
	}}, resolver())
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.CompileWidgets(compileWidgets(t, []widget.Ref{content}, nil))
	if err == nil || !strings.Contains(err.Error(), "no declared view") {
		t.Fatalf("undeclared view error = %v", err)
	}
}

func placementCodes(source []widget.Placement) string {
	values := make([]string, len(source))
	for index, placement := range source {
		values[index] = string(placement.Code)
	}
	return strings.Join(values, ",")
}

func TestTemplateWidgetBindingsCompileComposeAndResolvePerResource(t *testing.T) {
	ref := widget.NewRef("bound")
	catalog, err := widget.Compile([]widget.Source{{Module: widget.ModuleDescriptor{Code: "test", Label: "Test"}, Widgets: []widget.Widget{widget.Functional{
		Description: widget.Definition{Reference: ref, Label: "Bound", Description: "Bound", Fields: []field.Definition{{Key: "text", Label: "Text", Type: field.TypeString}}},
		Render: func(_ context.Context, _ widget.RenderInput, params map[string]any) (map[string]any, error) {
			return params, nil
		},
	}}}}, nil, resolver())
	if err != nil {
		t.Fatal(err)
	}
	bindings := widget.ParamBindings{"text": widget.ResourceField("headline")}
	definitions := []Definition{{Code: "bound", Label: "Bound", Fields: []field.Definition{{Key: "headline", Label: "Headline", Type: field.TypeString}}, Layout: Layout{{Code: "body", Label: "Основная область", Items: []Item{Widget{Widget: ref, ParamBindings: bindings}, ResourceWidgets{}}}}}}
	templates, err := Compile(definitions, resolver())
	if err != nil {
		t.Fatal(err)
	}
	bindings["text"] = widget.ResourceField("broken") // declarations are copied
	compiled, err := templates.CompileWidgets(catalog)
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := compiled.Template("bound")
	for _, value := range []string{"first", "second"} {
		placements, err := Compose(runtime, []widget.Binding{{ID: 1, Code: "test_bound", Area: "body", Presentation: widget.DefaultPresentation(), ParamBindings: widget.ParamBindings{"text": widget.ResourceField("headline")}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, placement := range placements["body"] {
			widgetRuntime, _ := catalog.Widget(placement.Code)
			instance, err := widgetRuntime.NewResolved(placement.Params, placement.ParamBindings, runtime.FieldSchema(), widget.ResourceValues{Fields: map[string]any{"headline": value}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := instance.Render(context.Background(), widget.RenderInput{})
			if err != nil || result["text"] != value {
				t.Fatalf("resolved value=%v, %v", result, err)
			}
			placement.ParamBindings["text"] = widget.ResourceField("broken")
		}
	}
	definitions[0].Layout[0].Items[0] = Widget{Widget: ref, ParamBindings: widget.ParamBindings{"text": widget.ResourceField("headline")}}
	definitions[0].Fields[0].Type = field.TypeTextarea
	templates, err = Compile(definitions, resolver())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = templates.CompileWidgets(catalog); err == nil {
		t.Fatal("incompatible template binding compiled")
	}
}
