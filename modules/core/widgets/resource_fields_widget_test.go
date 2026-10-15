package widgets

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func TestResourceFieldsWidgetDefinitionAndRender(t *testing.T) {
	var declared widget.Widget
	for _, candidate := range All() {
		if candidate.Definition().Reference == ResourceFields {
			declared = candidate
			break
		}
	}
	if declared == nil {
		t.Fatal("resource fields widget is not registered")
	}
	definition := declared.Definition()
	if len(definition.Fields) != 1 {
		t.Fatalf("widget fields = %#v", definition.Fields)
	}
	selection := definition.Fields[0]
	if selection.Key != "fields" || selection.Type != field.TypeString || !selection.Required ||
		selection.Options != (field.StringOptions{Multiple: true}) || selection.Editor != "template-field-picker" {
		t.Fatalf("selection definition = %#v", selection)
	}

	instance, err := declared.New(map[string]any{"fields": []string{"title", "structured", "image", "rows", "missing"}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := instance.Render(context.Background(), widget.RenderInput{Resource: widget.ResourceSnapshot{
		Fields: map[string]any{
			"title": "Example", "structured": map[string]any{"enabled": true},
			"image": int64(17), "rows": []any{map[string]any{"name": "First"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"title": "Example", "structured": map[string]any{"enabled": true},
		"image": int64(17), "rows": []any{map[string]any{"name": "First"}},
		"missing": nil,
	}
	if !reflect.DeepEqual(data["fields"], want) {
		t.Fatalf("fields = %#v, want %#v", data["fields"], want)
	}
	if len(data) != 1 {
		t.Fatalf("rendered data = %#v", data)
	}
}

func TestResourceFieldsWidgetConfigurationValidation(t *testing.T) {
	catalog, err := widget.Compile([]widget.Source{{
		Module:  widget.ModuleDescriptor{Code: "core", Label: "Core"},
		Widgets: []widget.Widget{resourceFieldsWidget{}},
	}}, nil, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	runtime, exists := catalog.Widget("core_resource_fields")
	if !exists {
		t.Fatal("compiled resource fields widget not found")
	}
	source, err := field.Compile([]field.Definition{
		{Key: "title", Label: "Title", Type: field.TypeString},
		{Key: "structured", Label: "Structured", Type: field.TypeJSON},
		{Key: "tags", Label: "Tags", Type: field.TypeString, Options: field.StringOptions{Multiple: true}},
	}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]any{"fields": []string{"title", "structured"}}
	if _, err := runtime.NormalizeConfiguration(valid, nil, source); err != nil {
		t.Fatalf("valid template field selection rejected: %v", err)
	}

	tests := []struct {
		name     string
		params   map[string]any
		bindings widget.ParamBindings
		message  string
	}{
		{name: "empty selection", params: map[string]any{"fields": []string{}}, message: "required"},
		{name: "unknown field", params: map[string]any{"fields": []string{"missing"}}, message: "does not exist"},
		{name: "duplicate field", params: map[string]any{"fields": []string{"title", "title"}}, message: "more than once"},
		{name: "binding", params: map[string]any{}, bindings: widget.ParamBindings{"fields": widget.ResourceField("tags")}, message: "cannot be bound"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runtime.NormalizeConfiguration(test.params, test.bindings, source)
			if !errors.Is(err, widget.ErrInvalidParams) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want invalid params containing %q", err, test.message)
			}
		})
	}

	changedTemplate, err := field.Compile([]field.Definition{{Key: "title", Label: "Title", Type: field.TypeString}}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.NormalizeConfiguration(valid, nil, changedTemplate); !errors.Is(err, widget.ErrInvalidParams) || !strings.Contains(err.Error(), "structured") {
		t.Fatalf("stale field configuration error = %v", err)
	}
	if _, err := runtime.NormalizeStoredConfiguration(valid, nil, changedTemplate); err != nil {
		t.Fatalf("stale stored configuration should remain loadable for repair: %v", err)
	}
}

func TestResourceFieldsWidgetStaticTemplatePlacementUsesTemplateSchemaValidation(t *testing.T) {
	widgetCatalog, err := widget.Compile([]widget.Source{{
		Module:  widget.ModuleDescriptor{Code: "core", Label: "Core"},
		Widgets: []widget.Widget{resourceFieldsWidget{}},
	}}, nil, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	templates, err := template.Compile([]template.Definition{{
		Code: "page", Label: "Page",
		Fields: []field.Definition{{Key: "title", Label: "Title", Type: field.TypeString}},
		Layout: template.Layout{{Code: "body", Label: "Body", Items: []template.Item{template.Widget{
			Widget: ResourceFields,
			Params: map[string]any{"fields": []string{"stale"}},
		}}}},
	}}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := templates.CompileWidgets(widgetCatalog); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("static placement error = %v", err)
	}
}
