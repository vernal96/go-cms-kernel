package widget

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

func bindingRuntime(t *testing.T, definitions []field.Definition) *Runtime {
	t.Helper()
	catalog, err := Compile([]Source{{Module: testModule("test"), Widgets: []Widget{Functional{
		Description: Definition{Reference: NewRef("echo"), Label: "Echo", Description: "Echo parameters", Fields: definitions},
		Render: func(_ context.Context, _ RenderInput, params map[string]any) (map[string]any, error) {
			return params, nil
		},
	}}}}, nil, standardResolver())
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := catalog.Widget("test_echo")
	return runtime
}

func TestParamBindingsPreserveWholeTypedValues(t *testing.T) {
	definitions := []field.Definition{
		{Key: "text", Label: "Text", Type: field.TypeString},
		{Key: "number", Label: "Number", Type: field.TypeInteger},
		{Key: "file", Label: "File", Type: field.TypeFile},
		{Key: "media", Label: "Media", Type: field.TypeMedia},
		{Key: "list", Label: "List", Type: field.TypeString, Options: field.StringOptions{Multiple: true}},
		{Key: "json", Label: "JSON", Type: field.TypeJSON},
		{Key: "rows", Label: "Rows", Type: field.TypeRepeater, Options: field.RepeaterOptions{Fields: []field.Definition{{Key: "name", Label: "Name", Type: field.TypeString}}}},
	}
	schema, err := field.CompilePersistent(definitions, standardResolver())
	if err != nil {
		t.Fatal(err)
	}
	values, err := schema.Validate(map[string]any{"text": "literal {{ resource.title }}", "number": int64(42), "file": int64(7), "media": int64(8), "list": []any{"a", "b"}, "json": map[string]any{"key": "value"}, "rows": []any{map[string]any{"name": "first"}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := bindingRuntime(t, definitions)
	bindings := ParamBindings{}
	for _, def := range definitions {
		bindings[def.Key] = ResourceField(def.Key)
	}
	normalized, err := runtime.NormalizeConfiguration(nil, bindings, schema)
	if err != nil || len(normalized) != 0 {
		t.Fatalf("configuration = %#v, %v", normalized, err)
	}
	instance, err := runtime.NewResolved(nil, bindings, schema, ResourceValues{Fields: values})
	if err != nil {
		t.Fatal(err)
	}
	got, err := instance.Render(context.Background(), RenderInput{})
	if err != nil || !reflect.DeepEqual(got, values) {
		t.Fatalf("typed values changed: %#v != %#v (%v)", got, values, err)
	}
	got["json"].(map[string]any)["key"] = "changed"
	got["rows"].([]any)[0].(map[string]any)["name"] = "changed"
	if values["json"].(map[string]any)["key"] != "value" || values["rows"].([]any)[0].(map[string]any)["name"] != "first" {
		t.Fatal("render mutated source snapshot")
	}
}

func TestParamBindingsRejectInvalidConfiguration(t *testing.T) {
	schema, err := field.CompilePersistent([]field.Definition{
		{Key: "plain", Label: "Plain", Type: field.TypeString},
		{Key: "rich", Label: "Rich", Type: field.TypeTextarea},
		{Key: "many", Label: "Many", Type: field.TypeString, Options: field.StringOptions{Multiple: true}},
	}, standardResolver())
	if err != nil {
		t.Fatal(err)
	}
	runtime := bindingRuntime(t, []field.Definition{{Key: "text", Label: "Text", Type: field.TypeString, Required: boolPointer(true)}, {Key: "required", Label: "Required", Type: field.TypeString, Required: boolPointer(true)}})
	tests := []struct {
		name     string
		params   map[string]any
		bindings ParamBindings
	}{
		{"unknown target", map[string]any{"required": "yes"}, ParamBindings{"absent": ResourceField("plain")}},
		{"unknown field", map[string]any{"required": "yes"}, ParamBindings{"text": ResourceField("absent")}},
		{"unknown property", map[string]any{"required": "yes"}, ParamBindings{"text": ResourceProperty("password")}},
		{"semantic type", map[string]any{"required": "yes"}, ParamBindings{"text": ResourceField("rich")}},
		{"multiplicity", map[string]any{"required": "yes"}, ParamBindings{"text": ResourceField("many")}},
		{"conflict", map[string]any{"text": "literal", "required": "yes"}, ParamBindings{"text": ResourceField("plain")}},
		{"required literal", nil, ParamBindings{"text": ResourceField("plain")}},
		{"nested source", map[string]any{"required": "yes"}, ParamBindings{"text": ResourceField("rows.0.name")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := runtime.NormalizeConfiguration(tt.params, tt.bindings, schema); !errors.Is(err, ErrInvalidParams) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParamBindingsDeferConstraintsAndReadCurrentResource(t *testing.T) {
	runtime := bindingRuntime(t, []field.Definition{{Key: "title", Label: "Title", Type: field.TypeString, Required: boolPointer(true), Rules: []string{"min=3"}}})
	bindings := ParamBindings{"title": ResourceProperty("title")}
	if _, err := runtime.NormalizeConfiguration(nil, bindings, nil); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"first", "second"} {
		instance, err := runtime.NewResolved(nil, bindings, nil, ResourceValues{Properties: map[string]any{"title": value}})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := instance.Render(context.Background(), RenderInput{})
		if got["title"] != value {
			t.Fatalf("stale value: %#v", got)
		}
	}
	for _, values := range []ResourceValues{{}, {Properties: map[string]any{"title": "x"}}} {
		if _, err := runtime.NewResolved(nil, bindings, nil, values); !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("invalid bound value: %v", err)
		}
	}
}
