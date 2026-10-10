package field_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

func TestSchemaValidateIncomplete(t *testing.T) {
	schema, err := field.Compile([]field.Definition{
		{Key: "logo", Type: field.TypeFile, Label: "Logo", Required: true, Options: field.FileOptions{Disk: "public", VirtualPath: "assets", SettingsCode: "image"}},
		{Key: "title", Type: field.TypeString, Label: "Title", Required: true, Validators: []field.ValidatorDefinition{{Type: "min_length", Options: map[string]any{"value": 2}}}},
		{Key: "count", Type: field.TypeInteger, Label: "Count", Required: true, Validators: []field.ValidatorDefinition{{Type: "min", Options: map[string]any{"value": 0}}}},
		{Key: "enabled", Type: field.TypeCheckbox, Label: "Enabled", Required: true},
		{Key: "tags", Type: field.TypeString, Label: "Tags", Required: true, Options: field.StringOptions{Multiple: true}, Validators: []field.ValidatorDefinition{{Type: "min_items", Options: map[string]any{"value": 2}}}},
		repeater(field.Definition{Key: "title", Type: field.TypeString, Label: "Title", Required: true}),
	}, standardResolver())
	if err != nil {
		t.Fatal(err)
	}
	for _, values := range []map[string]any{nil, {}, {"logo": nil, "title": "", "tags": []string{}, "slides": []any{}}} {
		got, err := schema.ValidateIncomplete(values)
		if err != nil || len(got) != 0 {
			t.Fatalf("incomplete %v: %v, %v", values, got, err)
		}
	}
	got, err := schema.ValidateIncomplete(map[string]any{"count": 0, "enabled": false, "logo": float64(7), "title": "OK", "tags": []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["count"] != int64(0) || got["enabled"] != false || got["logo"] != int64(7) {
		t.Fatalf("values lost or not normalized: %#v", got)
	}
	for _, tc := range []struct {
		values    map[string]any
		key, code string
	}{
		{map[string]any{"logo": "invalid"}, "logo", "type"},
		{map[string]any{"title": "x"}, "title", "min_length"},
		{map[string]any{"count": -1}, "count", "min"},
		{map[string]any{"tags": []string{"a"}}, "tags", "min_items"},
		{map[string]any{"slides": []any{map[string]any{}}}, "slides[0].title", "required"},
		{map[string]any{"unknown": nil}, "unknown", "defined"},
	} {
		_, err := schema.ValidateIncomplete(tc.values)
		var failures field.ValidationErrors
		if !errors.As(err, &failures) || len(failures) != 1 || failures[0].Key != tc.key || string(failures[0].Code) != tc.code {
			t.Fatalf("%v: unexpected error %v", tc.values, err)
		}
	}
	if _, err := schema.Validate(map[string]any{}); err == nil {
		t.Fatal("full validation accepted missing required fields")
	}
	if _, err := schema.ValidatePartial(map[string]any{"logo": nil}); err == nil {
		t.Fatal("partial validation stopped checking supplied required fields")
	}
	partial, err := schema.ValidatePartial(nil)
	if err != nil || !reflect.DeepEqual(partial, map[string]any{}) {
		t.Fatalf("partial validation changed: %v, %v", partial, err)
	}
}
