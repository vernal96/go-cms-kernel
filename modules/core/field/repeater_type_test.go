package field_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

func repeater(fields ...field.Definition) field.Definition {
	return field.Definition{Key: "slides", Type: field.TypeRepeater, Label: "Слайды", Options: field.RepeaterOptions{Fields: fields}}
}
func textDefinition() field.Definition {
	return field.Definition{Key: "title", Type: field.TypeString, Label: "Title"}
}
func TestRepeaterCompilation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options any
		valid   bool
	}{
		{"value", field.RepeaterOptions{Fields: []field.Definition{textDefinition()}, MaxItems: 10}, true},
		{"pointer", &field.RepeaterOptions{Fields: []field.Definition{textDefinition()}, MinItems: 1}, true},
		{"json", json.RawMessage(`{"fields":[{"key":"n","type":"int","label":"Number","options":{"step":2}}],"max_items":10}`), true},
		{"negative min", field.RepeaterOptions{Fields: []field.Definition{textDefinition()}, MinItems: -1}, false},
		{"negative max", field.RepeaterOptions{Fields: []field.Definition{textDefinition()}, MaxItems: -1}, false},
		{"reversed limits", field.RepeaterOptions{Fields: []field.Definition{textDefinition()}, MinItems: 2, MaxItems: 1}, false},
		{"empty", field.RepeaterOptions{}, false},
		{"nil", nil, false},
		{"duplicate", field.RepeaterOptions{Fields: []field.Definition{textDefinition(), textDefinition()}}, false},
		{"unknown", field.RepeaterOptions{Fields: []field.Definition{{Key: "x", Type: "missing", Label: "X"}}}, false},
		{"nested", field.RepeaterOptions{Fields: []field.Definition{repeater(textDefinition())}}, false},
		{"unknown option", json.RawMessage(`{"fields":[],"surprise":1}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := repeater()
			def.Options = tc.options
			_, err := field.CompilePersistent([]field.Definition{def}, field.StandardTypes())
			if (err == nil) != tc.valid {
				t.Fatalf("compile: %v", err)
			}
		})
	}
}
func TestRepeaterNormalizeValidateAndStorage(t *testing.T) {
	required := true
	title := textDefinition()
	title.Required = &required
	title.Rules = []string{"min=2"}
	def := repeater(title, field.Definition{Key: "count", Type: field.TypeInteger, Label: "Count"})
	def.Options = field.RepeaterOptions{Fields: def.Options.(field.RepeaterOptions).Fields, MinItems: 1, MaxItems: 2}
	schema, err := field.CompilePersistent([]field.Definition{def}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	input := []map[string]any{{"title": "Second", "count": float64(2)}, {"title": "First", "count": json.Number("1")}}
	values, err := schema.Validate(map[string]any{"slides": input})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{map[string]any{"title": "Second", "count": int64(2)}, map[string]any{"title": "First", "count": int64(1)}}
	if !reflect.DeepEqual(values["slides"], want) {
		t.Fatalf("values=%#v", values)
	}
	input[0]["title"] = "changed"
	if !reflect.DeepEqual(values["slides"], want) {
		t.Fatal("rows share input")
	}
	stored, err := schema.StoredValues(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Kind != field.StorageJSON || stored[0].Multiple || !reflect.DeepEqual(stored[0].Value, want) {
		t.Fatalf("stored=%#v", stored)
	}
	for _, tc := range []struct {
		name      string
		value     any
		key, rule string
	}{
		{"nonarray", "bad", "slides", "type"},
		{"null", nil, "slides", "type"},
		{"object", map[string]any{}, "slides", "type"},
		{"nonobject", []any{1}, "slides[0]", "type"},
		{"null row", []any{nil}, "slides[0]", "type"},
		{"required", []any{map[string]any{}}, "slides[0].title", "required"},
		{"scalar", []any{map[string]any{"title": "OK", "count": "bad"}}, "slides[0].count", "type"},
		{"rule", []any{map[string]any{"title": "x"}}, "slides[0].title", "min"},
		{"unknown key", []any{map[string]any{"title": "OK", "x": 1}}, "slides[0].x", "defined"},
		{"minimum", []any{}, "slides", "min"},
		{"maximum", []any{1, 2, 3}, "slides", "max"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := schema.Validate(map[string]any{"slides": tc.value})
			var failures field.ValidationErrors
			if !errors.As(err, &failures) || len(failures) == 0 || failures[0].Key != tc.key || failures[0].Rule != tc.rule {
				t.Fatalf("errors=%#v (%v)", failures, err)
			}
		})
	}
	if _, err := schema.Validate(nil); err == nil {
		t.Fatal("omitted value bypassed min")
	}
	if _, err := schema.ValidatePartial(nil); err != nil {
		t.Fatal(err)
	}
}
func TestRepeaterEmptyAndRequired(t *testing.T) {
	def := repeater(textDefinition())
	schema, err := field.Compile([]field.Definition{def}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	for _, values := range []map[string]any{nil, {"slides": []any{}}} {
		result, err := schema.Validate(values)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		if string(raw) != `{"slides":[]}` {
			t.Fatal(string(raw))
		}
	}
	required := true
	def.Required = &required
	schema, err = field.Compile([]field.Definition{def}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schema.Validate(map[string]any{"slides": []any{}}); err == nil {
		t.Fatal("required repeater accepted empty")
	}
}
func TestRepeaterCustomMetadataAndCloning(t *testing.T) {
	custom := field.DescribedType{Type: configuredString{}, Presentation: field.Metadata{Label: "Custom", Editor: "example.text"}}
	types := append(field.StandardTypes(), custom)
	fields := []field.Definition{{Key: "custom", Type: custom.Code(), Label: "Custom", Options: map[string]any{"max": float64(12)}}}
	def := repeater(fields...)
	schema, err := field.CompilePersistent([]field.Definition{def}, types)
	if err != nil {
		t.Fatal(err)
	}
	fields[0].Key = "mutated"
	detached := schema.Definitions()
	detached[0].Options.(field.RepeaterOptions).Fields[0].Label = "changed"
	if _, err := schema.Validate(map[string]any{"slides": []any{map[string]any{"custom": "works"}}}); err != nil {
		t.Fatal(err)
	}
	descriptor, err := field.Describe(schema.Definitions()[0], types)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"editor":"example.text"`, `"max":12`, `"label":"Custom"`, `"editor":"repeater"`} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("metadata %s missing %s", raw, expected)
		}
	}
	options, err := field.DecodeOptionsJSON(field.TypeRepeater, descriptor.Options)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip := repeater()
	roundTrip.Options = options
	if _, err := field.CompilePersistent([]field.Definition{roundTrip}, types); err != nil {
		t.Fatal(err)
	}
	repeaterType, _ := types.FieldType(field.TypeRepeater)
	if field.DescribeType(repeaterType).Label != "Конструктор" {
		t.Fatal("wrong admin label")
	}
}
func TestRepeaterReferences(t *testing.T) {
	fileDef := field.Definition{Key: "file", Type: field.TypeFile, Label: "File", Options: field.FileOptions{MIMETypes: []string{"image/*"}}}
	mediaDef := field.Definition{Key: "image", Type: field.TypeMedia, Label: "Image"}
	schema, err := field.CompilePersistent([]field.Definition{repeater(fileDef, mediaDef), fileDef, mediaDef}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	values, err := schema.Validate(map[string]any{"slides": []any{map[string]any{"file": 1, "image": 2}, map[string]any{"file": 3, "image": 4}}, "file": 5, "image": 6})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := schema.References(values)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, ref := range refs {
		keys = append(keys, ref.Key)
	}
	if !reflect.DeepEqual(keys, []string{"slides[0].file", "slides[0].image", "slides[1].file", "slides[1].image", "file", "image"}) {
		t.Fatalf("refs=%#v", refs)
	}
	files, err := schema.FileReferences(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[0].Options.MIMETypes[0] != "image/*" || files[2].ID != 5 {
		t.Fatalf("files=%#v", files)
	}
	stored, err := schema.StoredValues(values)
	if err != nil {
		t.Fatal(err)
	}
	media, err := stored[0].MediaReferences()
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 2 || media[0].ID != 2 || !reflect.DeepEqual(media[1].Path, []string{"1", "image"}) {
		t.Fatalf("media=%#v", media)
	}
}

type repeaterWrapper struct{}

func (repeaterWrapper) Code() field.TypeCode { return "example.wrapper" }
func (repeaterWrapper) Compile(ctx field.CompileContext, options any) (field.ValueType, error) {
	t, _ := ctx.Types.FieldType(field.TypeRepeater)
	return t.Compile(ctx, options)
}
func TestRepeaterRejectsIndirectNesting(t *testing.T) {
	def := repeater(field.Definition{Key: "wrapped", Label: "Wrapped", Type: "example.wrapper", Options: field.RepeaterOptions{Fields: []field.Definition{textDefinition()}}})
	_, err := field.Compile([]field.Definition{def}, append(field.StandardTypes(), repeaterWrapper{}))
	if err == nil || !strings.Contains(err.Error(), "nested repeater") {
		t.Fatalf("err=%v", err)
	}
}

// A contributed type can reuse a reference-containing value type without the
// repeater or resource services knowing its semantic type code.
type customFileType struct{}

func (customFileType) Code() field.TypeCode { return "example.file" }
func (customFileType) Compile(ctx field.CompileContext, options any) (field.ValueType, error) {
	t, _ := ctx.Types.FieldType(field.TypeFile)
	return t.Compile(ctx, options)
}
func TestRepeaterCollectsCustomReferences(t *testing.T) {
	types := append(field.StandardTypes(), customFileType{})
	schema, err := field.CompilePersistent([]field.Definition{repeater(field.Definition{Key: "asset", Type: "example.file", Label: "Asset", Options: field.FileOptions{MIMETypes: []string{"image/*"}}})}, types)
	if err != nil {
		t.Fatal(err)
	}
	values, err := schema.Validate(map[string]any{"slides": []any{map[string]any{"asset": 42}}})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := schema.FileReferences(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Key != "slides[0].asset" || refs[0].ID != 42 || refs[0].Options.MIMETypes[0] != "image/*" {
		t.Fatalf("references=%#v", refs)
	}
}
