package field_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/field/validation"
)

type validatorResolver struct {
	field.Types
	extra field.ValidatorType
}

func (r validatorResolver) ValidatorType(code field.ValidatorCode) (field.ValidatorType, bool) {
	if r.extra != nil && r.extra.Code() == code {
		return r.extra, true
	}
	return r.Types.ValidatorType(code)
}

type evenType struct{}

func (evenType) Code() field.ValidatorCode { return "example.even" }
func (evenType) ValidatorMetadata() field.ValidatorMetadata {
	return field.ValidatorMetadata{Label: "Even number", Options: []field.ConfigField{{Key: "offset", Label: "Offset", Type: field.TypeInteger, Required: true}}, FieldTypes: []field.TypeCode{field.TypeInteger}}
}
func (evenType) Compile(ctx field.ValidatorContext, options any) (field.Validator, error) {
	if ctx.Storage != field.StorageInteger || ctx.Multiple {
		return nil, fmt.Errorf("integer scalar required")
	}
	var raw map[string]any
	bytes, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(bytes, &raw); err != nil {
		return nil, err
	}
	if len(raw) != 1 {
		return nil, fmt.Errorf("offset is required")
	}
	parsed, err := field.DecodeOptions[struct {
		Offset int64 `json:"offset"`
	}](options)
	if err != nil {
		return nil, err
	}
	if parsed.Offset < 0 {
		return nil, fmt.Errorf("offset must be nonnegative")
	}
	return evenValidator{offset: parsed.Offset}, nil
}

type evenValidator struct{ offset int64 }

func (v evenValidator) Validate(value any) error {
	if (value.(int64)+v.offset)%2 != 0 {
		return errors.New("odd")
	}
	return nil
}

func TestBuiltinValidators(t *testing.T) {
	cases := []struct {
		name       string
		typ        field.TypeCode
		options    any
		validators []field.ValidatorDefinition
		good, bad  any
		code       field.ValidatorCode
	}{
		{"min", field.TypeInteger, nil, []field.ValidatorDefinition{validation.Min(2)}, float64(2), float64(1), "min"},
		{"max", field.TypeFloat, nil, []field.ValidatorDefinition{validation.Max(5)}, float64(5), float64(6), "max"},
		{"between", field.TypeInteger, nil, []field.ValidatorDefinition{validation.Between(2, 4)}, float64(3), float64(5), "between"},
		{"multiple_of", field.TypeInteger, nil, []field.ValidatorDefinition{validation.MultipleOf(3)}, float64(6), float64(5), "multiple_of"},
		{"digits", field.TypeInteger, nil, []field.ValidatorDefinition{validation.Digits(3)}, float64(123), float64(12), "digits"},
		{"min_digits", field.TypeInteger, nil, []field.ValidatorDefinition{validation.MinDigits(2)}, float64(12), float64(1), "min_digits"},
		{"max_digits", field.TypeInteger, nil, []field.ValidatorDefinition{validation.MaxDigits(2)}, float64(12), float64(123), "max_digits"},
		{"digits_between", field.TypeInteger, nil, []field.ValidatorDefinition{validation.DigitsBetween(2, 3)}, float64(12), float64(1), "digits_between"},
		{"min_length", field.TypeString, nil, []field.ValidatorDefinition{validation.MinLength(2)}, "аб", "а", "min_length"},
		{"max_length", field.TypeString, nil, []field.ValidatorDefinition{validation.MaxLength(2)}, "аб", "абв", "max_length"},
		{"length", field.TypeString, nil, []field.ValidatorDefinition{validation.Length(2)}, "ab", "a", "length"},
		{"length_between", field.TypeString, nil, []field.ValidatorDefinition{validation.LengthBetween(2, 3)}, "ab", "a", "length_between"},
		{"alpha", field.TypeString, nil, []field.ValidatorDefinition{validation.Alpha()}, "абc", "ab1", "alpha"},
		{"alpha_dash", field.TypeString, nil, []field.ValidatorDefinition{validation.AlphaDash()}, "ab-1", "ab!", "alpha_dash"},
		{"alpha_numeric", field.TypeString, nil, []field.ValidatorDefinition{validation.AlphaNumeric()}, "a1", "a-", "alpha_numeric"},
		{"ascii", field.TypeString, nil, []field.ValidatorDefinition{validation.ASCII()}, "abc", "абв", "ascii"},
		{"lowercase", field.TypeString, nil, []field.ValidatorDefinition{validation.Lowercase()}, "abc", "Abc", "lowercase"},
		{"uppercase", field.TypeString, nil, []field.ValidatorDefinition{validation.Uppercase()}, "ABC", "Abc", "uppercase"},
		{"starts_with", field.TypeString, nil, []field.ValidatorDefinition{validation.StartsWith("ab")}, "abc", "xbc", "starts_with"},
		{"ends_with", field.TypeString, nil, []field.ValidatorDefinition{validation.EndsWith("bc")}, "abc", "abx", "ends_with"},
		{"doesnt_start_with", field.TypeString, nil, []field.ValidatorDefinition{validation.DoesntStartWith("ab")}, "xbc", "abc", "doesnt_start_with"},
		{"doesnt_end_with", field.TypeString, nil, []field.ValidatorDefinition{validation.DoesntEndWith("bc")}, "abx", "abc", "doesnt_end_with"},
		{"contains", field.TypeString, nil, []field.ValidatorDefinition{validation.Contains("bc")}, "abc", "axc", "contains"},
		{"doesnt_contain", field.TypeString, nil, []field.ValidatorDefinition{validation.DoesntContain("bc")}, "axc", "abc", "doesnt_contain"},
		{"regex", field.TypeString, nil, []field.ValidatorDefinition{validation.Regex(`^a`)}, "abc", "xbc", "regex"},
		{"not_regex", field.TypeString, nil, []field.ValidatorDefinition{validation.NotRegex(`^a`)}, "xbc", "abc", "not_regex"},
		{"in", field.TypeInteger, nil, []field.ValidatorDefinition{validation.In(1, 2)}, float64(2), float64(3), "in"},
		{"not_in", field.TypeString, nil, []field.ValidatorDefinition{validation.NotIn("blocked")}, "allowed", "blocked", "not_in"},
		{"min_items", field.TypeString, field.StringOptions{Multiple: true}, []field.ValidatorDefinition{validation.MinItems(2)}, []any{"a", "b"}, []any{"a"}, "min_items"},
		{"max_items", field.TypeString, field.StringOptions{Multiple: true}, []field.ValidatorDefinition{validation.MaxItems(2)}, []any{"a", "b"}, []any{"a", "b", "c"}, "max_items"},
		{"items_between", field.TypeString, field.StringOptions{Multiple: true}, []field.ValidatorDefinition{validation.ItemsBetween(1, 2)}, []any{"a"}, []any{"a", "b", "c"}, "items_between"},
		{"items_count", field.TypeString, field.StringOptions{Multiple: true}, []field.ValidatorDefinition{validation.ItemsCount(2)}, []any{"a", "b"}, []any{"a"}, "items_count"},
		{"unique_items", field.TypeString, field.StringOptions{Multiple: true}, []field.ValidatorDefinition{validation.UniqueItems()}, []any{"a", "b"}, []any{"a", "a"}, "unique_items"},
		{"string_list_contains", field.TypeString, field.StringOptions{Multiple: true}, []field.ValidatorDefinition{validation.Contains("b")}, []any{"a", "b"}, []any{"a", "c"}, "contains"},
		{"list_contains", field.TypeInteger, field.IntegerOptions{Multiple: true}, []field.ValidatorDefinition{validation.Contains(2)}, []any{1, 2}, []any{1, 3}, "contains"},
		{"url", field.TypeString, nil, []field.ValidatorDefinition{validation.URL()}, "https://example.test/a", "bad", "url"},
		{"ip", field.TypeString, nil, []field.ValidatorDefinition{validation.IP()}, "127.0.0.1", "invalid", "ip"},
		{"ipv4", field.TypeString, nil, []field.ValidatorDefinition{validation.IPv4()}, "127.0.0.1", "::1", "ipv4"},
		{"ipv6", field.TypeString, nil, []field.ValidatorDefinition{validation.IPv6()}, "::1", "127.0.0.1", "ipv6"},
		{"mac", field.TypeString, nil, []field.ValidatorDefinition{validation.MAC()}, "01:23:45:67:89:ab", "bad", "mac"},
		{"uuid", field.TypeString, nil, []field.ValidatorDefinition{validation.UUID()}, "123e4567-e89b-12d3-a456-426614174000", "bad", "uuid"},
		{"ulid", field.TypeString, nil, []field.ValidatorDefinition{validation.ULID()}, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "bad", "ulid"},
		{"hex_color", field.TypeString, nil, []field.ValidatorDefinition{validation.HexColor()}, "#abcdef", "abc", "hex_color"},
		{"accepted", field.TypeCheckbox, nil, []field.ValidatorDefinition{validation.Accepted()}, true, false, "accepted"},
		{"declined", field.TypeCheckbox, nil, []field.ValidatorDefinition{validation.Declined()}, false, true, "declined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := field.Compile([]field.Definition{{Key: "value", Type: tc.typ, Label: "Value", Options: tc.options, Validators: tc.validators}}, field.StandardTypes())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = schema.Validate(map[string]any{"value": tc.good}); err != nil {
				t.Fatalf("good: %v", err)
			}
			_, err = schema.Validate(map[string]any{"value": tc.bad})
			var failures field.ValidationErrors
			if !errors.As(err, &failures) || len(failures) == 0 || failures[0].Code != tc.code {
				t.Fatalf("bad=%#v %v", failures, err)
			}
		})
	}
}
func TestValidatorCompilationAndNestedRoundTrip(t *testing.T) {
	resolver := validatorResolver{Types: field.StandardTypes(), extra: evenType{}}
	nested := field.Definition{Key: "count", Type: field.TypeInteger, Label: "Count", Validators: []field.ValidatorDefinition{{Type: "example.even", Options: map[string]any{"offset": 1}}}}
	parent := field.Definition{Key: "rows", Type: field.TypeRepeater, Label: "Rows", Options: field.RepeaterOptions{Fields: []field.Definition{nested}}}
	descriptor, err := field.Describe(parent, resolver)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var restored field.Descriptor
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	options, err := field.DecodeOptionsJSON(field.TypeRepeater, restored.Options)
	if err != nil {
		t.Fatal(err)
	}
	parent.Options = options
	schema, err := field.Compile([]field.Definition{parent}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := schema.Validate(map[string]any{"rows": []any{map[string]any{"count": float64(3)}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized["rows"].([]any)[0].(map[string]any)["count"]; got != int64(3) {
		t.Fatalf("validator did not see normalized value: %T %#v", got, got)
	}
	_, err = schema.Validate(map[string]any{"rows": []any{map[string]any{"count": float64(2)}}})
	var failures field.ValidationErrors
	if !errors.As(err, &failures) || len(failures) != 1 || failures[0].Key != "rows[0].count" || failures[0].Code != "example.even" {
		t.Fatalf("nested: %v", err)
	}
	defs := schema.Definitions()
	before := schema.Definitions()
	defs[0].Options.(field.RepeaterOptions).Fields[0].Validators[0].Options.(map[string]any)["offset"] = 9
	if !reflect.DeepEqual(before, schema.Definitions()) {
		t.Fatal("validator options share snapshot")
	}
}
func TestValidatorCodeAndFailureParamsAreStable(t *testing.T) {
	for _, code := range []field.ValidatorCode{"", "bad code", "Uppercase", "-leading"} {
		if err := field.ValidateValidatorCode(code); err == nil {
			t.Fatalf("accepted validator code %q", code)
		}
	}
	if err := field.ValidateValidatorCode("example.custom_validator"); err != nil {
		t.Fatal(err)
	}
	schema, err := field.Compile([]field.Definition{{Key: "v", Type: field.TypeInteger, Label: "V", Validators: []field.ValidatorDefinition{validation.Max(2)}}}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	_, err = schema.Validate(map[string]any{"v": 3})
	var failures field.ValidationErrors
	if !errors.As(err, &failures) {
		t.Fatal(err)
	}
	failures[0].Params["value"] = 99
	_, err = schema.Validate(map[string]any{"v": 3})
	if !errors.As(err, &failures) || failures[0].Params["value"] != float64(2) {
		t.Fatalf("mutable error params: %v", failures)
	}
}

func TestValidatorCompileRejectsBadDefinitionsAndCollectsFailures(t *testing.T) {
	resolver := field.StandardTypes()
	bad := []field.ValidatorDefinition{{Type: "missing"}, {Type: "min", Options: map[string]any{}}, {Type: "regex", Options: map[string]any{"value": "("}}, {Type: "min_items", Options: map[string]any{"value": 1}}, {Type: "min_length", Options: map[string]any{"value": 2}}}
	for _, v := range bad {
		typ := field.TypeInteger
		if v.Type == "regex" {
			typ = field.TypeString
		}
		if _, err := field.Compile([]field.Definition{{Key: "v", Type: typ, Label: "V", Validators: []field.ValidatorDefinition{v}}}, resolver); err == nil {
			t.Fatalf("accepted %#v", v)
		}
	}
	if _, err := field.Compile([]field.Definition{{Key: "v", Type: field.TypeInteger, Label: "V", Validators: []field.ValidatorDefinition{validation.Min(1), validation.Min(2)}}}, resolver); err == nil {
		t.Fatal("duplicate accepted")
	}
	schema, err := field.Compile([]field.Definition{{Key: "v", Type: field.TypeInteger, Label: "V", Validators: []field.ValidatorDefinition{validation.Min(2), validation.Max(0)}}}, resolver)
	if err == nil || schema != nil {
		t.Fatal("contradictory bounds accepted")
	}
	schema, err = field.Compile([]field.Definition{{Key: "v", Type: field.TypeInteger, Label: "V", Validators: []field.ValidatorDefinition{validation.Min(5), validation.Max(3)}}}, resolver)
	if err == nil {
		t.Fatal("contradictory bounds accepted")
	}
	schema, err = field.Compile([]field.Definition{{Key: "v", Type: field.TypeInteger, Label: "V", Validators: []field.ValidatorDefinition{validation.Min(5), validation.MultipleOf(2)}}}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = schema.Validate(map[string]any{"v": float64(3)})
	var failures field.ValidationErrors
	if !errors.As(err, &failures) || len(failures) != 2 || failures[0].Code != "min" || failures[1].Code != "multiple_of" {
		t.Fatalf("multiple failures: %v", err)
	}
	partial, err := schema.ValidatePartial(nil)
	if err != nil || len(partial) != 0 {
		t.Fatalf("partial: %#v %v", partial, err)
	}
}
