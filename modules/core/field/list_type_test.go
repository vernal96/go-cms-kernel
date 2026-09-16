package field_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

func TestStandardMultipleFields(t *testing.T) {
	cases := []struct {
		code    field.TypeCode
		value   any
		invalid any
		extra   string
		kind    field.StorageKind
	}{
		{field.TypeString, "hello", 1, "", field.StorageString},
		{field.TypeTextarea, "long text", 1, "", field.StorageString},
		{field.TypeEmail, "a@example.test", "invalid", "", field.StorageString},
		{field.TypePhone, "+79991234567", "invalid", "", field.StorageString},
		{field.TypeInteger, int64(0), 1.5, "", field.StorageInteger},
		{field.TypeFloat, 0.0, "invalid", "", field.StorageFloat},
		{field.TypeFile, int64(7), -1, "", field.StorageReference},
		{field.TypeMedia, int64(8), -1, "", field.StorageReference},
		{field.TypeSelect, "one", "unknown", `,"choices":[{"value":"one","label":"One"},{"value":"two","label":"Two"}]`, field.StorageString},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			options, err := field.DecodeOptionsJSON(tc.code, json.RawMessage(`{"multiple":true,"min_items":1,"max_items":2`+tc.extra+`}`))
			if err != nil {
				t.Fatal(err)
			}
			def := field.Definition{Key: "values", Label: "Values", Type: tc.code, Options: options}
			schema, err := field.CompilePersistent([]field.Definition{def}, field.StandardTypes())
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := field.Describe(def, field.StandardTypes())
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := json.Unmarshal(descriptor.Options, &config); err != nil || config["multiple"] != true || config["max_items"] != float64(2) {
				t.Fatalf("metadata %s: %v", descriptor.Options, err)
			}
			normalized, err := schema.Validate(map[string]any{"values": []any{tc.value}})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := schema.StoredValues(normalized)
			if err != nil {
				t.Fatal(err)
			}
			if len(stored) != 1 || !stored[0].Multiple || stored[0].Position != 0 || stored[0].Kind != tc.kind || !reflect.DeepEqual(stored[0].Value, tc.value) {
				t.Fatalf("stored %#v", stored)
			}
			for _, input := range []map[string]any{nil, {"values": []any{}}, {"values": tc.value}, {"values": []any{tc.value, tc.value, tc.value}}} {
				if _, err := schema.Validate(input); err == nil {
					t.Fatalf("accepted %#v", input)
				}
			}
			if _, err := schema.ValidatePartial(nil); err != nil {
				t.Fatalf("partial: %v", err)
			}
			for _, invalid := range []any{tc.invalid, nil, ""} {
				_, err := schema.Validate(map[string]any{"values": []any{tc.value, invalid}})
				var failures field.ValidationErrors
				if !errors.As(err, &failures) || len(failures) == 0 || failures[0].Key != "values[1]" {
					t.Fatalf("indexed error: %v", err)
				}
			}
			if tc.code == field.TypeMedia {
				refs, err := stored[0].MediaReferences()
				if err != nil || len(refs) != 1 || refs[0].ID != 8 {
					t.Fatalf("stored media refs: %#v %v", refs, err)
				}
			}
			if tc.code == field.TypeFile || tc.code == field.TypeMedia {
				refs, err := schema.References(normalized)
				if err != nil || len(refs) != 1 || refs[0].Key != "values[0]" {
					t.Fatalf("refs: %#v %v", refs, err)
				}
			}
		})
	}
}

func TestListRulesBoundsAndOrdering(t *testing.T) {
	for _, options := range []field.StringOptions{{MinItems: 1}, {Multiple: true, MinItems: -1}, {Multiple: true, MaxItems: -1}, {Multiple: true, MinItems: 3, MaxItems: 2}} {
		if _, err := field.Compile([]field.Definition{{Key: "v", Label: "V", Type: field.TypeString, Options: options}}, field.StandardTypes()); err == nil {
			t.Fatalf("accepted %#v", options)
		}
	}
	for _, def := range []field.Definition{
		{Key: "v", Label: "V", Type: field.TypeString, Options: field.StringOptions{Multiple: true}, Rules: []string{"min=2", "max=5"}},
		{Key: "v", Label: "V", Type: field.TypeInteger, Options: field.IntegerOptions{Multiple: true}, Rules: []string{"min=2", "max=5"}},
	} {
		schema, err := field.CompilePersistent([]field.Definition{def}, field.StandardTypes())
		if err != nil {
			t.Fatal(err)
		}
		good, bad := any("ok"), any("x")
		if def.Type == field.TypeInteger {
			good, bad = int64(4), int64(1)
		}
		_, err = schema.Validate(map[string]any{"v": []any{good, bad}})
		var failures field.ValidationErrors
		if !errors.As(err, &failures) || failures[0].Key != "v[1]" || failures[0].Rule != "min" {
			t.Fatalf("rules: %v", err)
		}
		values, err := schema.Validate(map[string]any{"v": []any{good, good}})
		if err != nil {
			t.Fatal(err)
		}
		stored, err := schema.StoredValues(values)
		if err != nil || len(stored) != 2 || stored[1].Position != 1 {
			t.Fatalf("order: %#v %v", stored, err)
		}
		empty, err := schema.Validate(map[string]any{"v": []any{}})
		if err != nil || len(empty) != 0 {
			t.Fatalf("optional empty: %#v %v", empty, err)
		}
	}
	schema, err := field.Compile([]field.Definition{{Key: "v", Label: "V", Type: field.TypeSelect, Options: field.SelectOptions{Multiple: true, Choices: []field.Choice{{Value: "a", Label: "A"}}}}}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schema.Validate(map[string]any{"v": []string{"a", "a"}}); err == nil {
		t.Fatal("duplicate selection accepted")
	}
}

func TestListsInsideRepeater(t *testing.T) {
	schema, err := field.CompilePersistent([]field.Definition{{Key: "rows", Label: "Rows", Type: field.TypeRepeater, Options: field.RepeaterOptions{Fields: []field.Definition{
		{Key: "images", Label: "Images", Type: field.TypeMedia, Options: field.MediaOptions{Multiple: true}},
		{Key: "emails", Label: "Emails", Type: field.TypeEmail, Options: field.StringOptions{Multiple: true}},
	}}}}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	values, err := schema.Validate(map[string]any{"rows": []any{map[string]any{"images": []any{int64(4), int64(2)}, "emails": []any{"a@example.test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := schema.References(values)
	if err != nil || len(refs) != 2 || refs[1].Key != "rows[0].images[1]" || refs[1].ID != 2 {
		t.Fatalf("refs %#v %v", refs, err)
	}
	_, err = schema.Validate(map[string]any{"rows": []any{map[string]any{"emails": []any{"invalid"}}}})
	var failures field.ValidationErrors
	if !errors.As(err, &failures) || failures[0].Key != "rows[0].emails[0]" {
		t.Fatalf("nested: %v", err)
	}
}
