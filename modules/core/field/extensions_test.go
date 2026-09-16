package field_test

import (
	"encoding/json"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"reflect"
	"testing"
)

type configuredString struct{}

func (configuredString) Code() field.TypeCode { return "example.text" }
func (configuredString) Compile(ctx field.CompileContext, options any) (field.ValueType, error) {
	if _, err := field.DecodeOptions[struct {
		Max int `json:"max"`
	}](options); err != nil {
		return nil, err
	}
	base, _ := field.StandardTypes().FieldType(field.TypeString)
	return base.Compile(ctx, nil)
}
func TestCustomFieldCompilesAndDescribesOptions(t *testing.T) {
	custom := field.DescribedType{Type: configuredString{}, Presentation: field.Metadata{Label: "Custom text", Editor: "textarea", Options: []field.ConfigField{{Key: "max", Label: "Limit", Type: field.TypeInteger}}}}
	resolver := field.Types{custom}
	definition := field.Definition{Key: "custom", Type: custom.Code(), Label: "Custom", Options: map[string]any{"max": float64(12)}}
	schema, err := field.CompilePersistent([]field.Definition{definition}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schema.Validate(map[string]any{"custom": "saved"}); err != nil {
		t.Fatal(err)
	}
	metadata, err := field.Describe(definition, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Editor != "textarea" || string(metadata.Options) != `{"max":12}` {
		t.Fatalf("metadata=%+v", metadata)
	}
	described := field.DescribeType(custom)
	described.Options[0].Label = "changed"
	if field.DescribeType(custom).Options[0].Label != "Limit" {
		t.Fatal("metadata shares mutable fields")
	}
}
func TestOptionsJSONRoundTrips(t *testing.T) {
	step := int64(2)
	for _, item := range []struct {
		code  field.TypeCode
		value any
	}{
		{field.TypeInteger, field.IntegerOptions{Step: &step}},
		{field.TypeSelect, field.SelectOptions{Choices: []field.Choice{{Value: "a", Label: "A"}}, Multiple: true}},
		{"custom", map[string]any{"enabled": false, "limit": float64(3), "values": []any{"a", float64(2)}}},
	} {
		raw, err := field.EncodeOptionsJSON(item.value)
		if err != nil {
			t.Fatal(err)
		}
		value, err := field.DecodeOptionsJSON(item.code, raw)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(value, item.value) {
			t.Fatalf("%s: got %#v want %#v", item.code, value, item.value)
		}
	}
	for _, raw := range []string{`{"step":1.5}`, `{"step":2,"unknown":true}`, `{} {}`} {
		if _, err := field.DecodeOptionsJSON(field.TypeInteger, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
