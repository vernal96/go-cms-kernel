package field_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

type typeResolver map[field.TypeCode]field.Type

func (r typeResolver) FieldType(
	code field.TypeCode,
) (field.Type, bool) {
	fieldType, exists := r[code]
	return fieldType, exists
}

func standardResolver() typeResolver {
	result := make(typeResolver)
	for _, fieldType := range field.StandardTypes() {
		result[fieldType.Code()] = fieldType
	}
	return result
}

func boolPointer(value bool) *bool {
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}

func TestSchemaNormalizesStandardTypes(t *testing.T) {
	schema, err := field.Compile(
		[]field.Definition{
			{
				Key:      "title",
				Type:     field.TypeString,
				Label:    "Title",
				Required: boolPointer(true),
				Rules:    []string{"min=2"},
			},
			{
				Key:   "count",
				Type:  field.TypeInteger,
				Label: "Count",
				Options: field.IntegerOptions{
					Step: int64Pointer(2),
				},
			},
			{
				Key:   "ratio",
				Type:  field.TypeFloat,
				Label: "Ratio",
			},
			{
				Key:      "enabled",
				Type:     field.TypeCheckbox,
				Label:    "Enabled",
				Required: boolPointer(true),
			},
			{
				Key:   "color",
				Type:  field.TypeRadio,
				Label: "Color",
				Options: field.RadioOptions{
					Choices: []field.Choice{
						{Value: "red", Label: "Red"},
						{Value: "blue", Label: "Blue"},
					},
				},
			},
			{
				Key:   "roles",
				Type:  field.TypeSelect,
				Label: "Roles",
				Options: field.SelectOptions{
					Multiple: true,
					Choices: []field.Choice{
						{Value: "author", Label: "Author"},
						{Value: "editor", Label: "Editor"},
					},
				},
			},
			{
				Key:   "description",
				Type:  field.TypeTextarea,
				Label: "Description",
			},
			{
				Key:   "email",
				Type:  field.TypeEmail,
				Label: "Email",
			},
			{
				Key:   "phone",
				Type:  field.TypePhone,
				Label: "Phone",
			},
			{
				Key:   "optional",
				Type:  field.TypeString,
				Label: "Optional",
			},
			{
				Key:      "explicit_optional",
				Type:     field.TypeString,
				Label:    "Explicit optional",
				Required: boolPointer(false),
			},
		},
		standardResolver(),
	)
	if err != nil {
		t.Fatal(err)
	}

	values, err := schema.Validate(map[string]any{
		"title":             "CMS",
		"count":             json.Number("3"),
		"ratio":             int64(2),
		"enabled":           false,
		"color":             "red",
		"roles":             []any{"author", "editor"},
		"description":       "Text",
		"email":             "site@example.com",
		"phone":             "+79991234567",
		"optional":          "",
		"explicit_optional": "",
	})
	if err != nil {
		t.Fatal(err)
	}

	if values["count"] != int64(3) {
		t.Fatalf("integer value = %#v", values["count"])
	}
	if values["ratio"] != float64(2) {
		t.Fatalf("float value = %#v", values["ratio"])
	}
	if values["enabled"] != false {
		t.Fatalf("checkbox value = %#v", values["enabled"])
	}
	if !reflect.DeepEqual(
		values["roles"],
		[]string{"author", "editor"},
	) {
		t.Fatalf("multiple select value = %#v", values["roles"])
	}
	if _, exists := values["optional"]; exists {
		t.Fatal("empty optional value was preserved")
	}
	if _, exists := values["explicit_optional"]; exists {
		t.Fatal("empty explicitly optional value was preserved")
	}
}

func TestSchemaStoredValuesPreserveKindsAndMultiValueOrder(t *testing.T) {
	schema, err := field.Compile([]field.Definition{
		{Key: "count", Type: field.TypeInteger, Label: "Count"},
		{Key: "roles", Type: field.TypeSelect, Label: "Roles", Options: field.SelectOptions{
			Multiple: true,
			Choices:  []field.Choice{{Value: "editor", Label: "Editor"}, {Value: "author", Label: "Author"}},
		}},
		{Key: "attachment", Type: field.TypeFile, Label: "Attachment"},
	}, standardResolver())
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := schema.Validate(map[string]any{
		"count": 3, "roles": []any{"editor", "author"}, "attachment": 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := schema.StoredValues(normalized)
	if err != nil {
		t.Fatal(err)
	}
	want := []field.StoredValue{
		{Key: "count", Kind: field.StorageInteger, Value: int64(3)},
		{Key: "roles", Position: 0, Kind: field.StorageString, Multiple: true, Value: "editor"},
		{Key: "roles", Position: 1, Kind: field.StorageString, Multiple: true, Value: "author"},
		{Key: "attachment", Kind: field.StorageReference, Value: int64(42)},
	}
	if !reflect.DeepEqual(stored, want) {
		t.Fatalf("stored values = %#v, want %#v", stored, want)
	}
}

func TestSchemaRequiredAndStrictValidation(t *testing.T) {
	schema, err := field.Compile(
		[]field.Definition{
			{
				Key:      "employees",
				Type:     field.TypeInteger,
				Label:    "Employees",
				Required: boolPointer(true),
			},
			{
				Key:      "enabled",
				Type:     field.TypeCheckbox,
				Label:    "Enabled",
				Required: boolPointer(true),
			},
			{
				Key:   "email",
				Type:  field.TypeEmail,
				Label: "Email",
			},
			{
				Key:   "phone",
				Type:  field.TypePhone,
				Label: "Phone",
			},
			{
				Key:   "kind",
				Type:  field.TypeSelect,
				Label: "Kind",
				Options: field.SelectOptions{
					Choices: []field.Choice{
						{Value: "public", Label: "Public"},
					},
				},
			},
		},
		standardResolver(),
	)
	if err != nil {
		t.Fatal(err)
	}

	values, err := schema.Validate(map[string]any{
		"employees": 0,
		"enabled":   false,
		"email":     "",
	})
	if err != nil {
		t.Fatalf("zero and false required values failed: %v", err)
	}
	if values["employees"] != int64(0) || values["enabled"] != false {
		t.Fatalf("normalized required values = %#v", values)
	}

	testCases := []struct {
		name   string
		values map[string]any
		key    string
		rule   string
	}{
		{
			name:   "missing required",
			values: map[string]any{"enabled": false},
			key:    "employees",
			rule:   "required",
		},
		{
			name: "strict integer",
			values: map[string]any{
				"employees": "12",
				"enabled":   false,
			},
			key:  "employees",
			rule: "type",
		},
		{
			name: "integer overflow",
			values: map[string]any{
				"employees": float64(9223372036854775808),
				"enabled":   false,
			},
			key:  "employees",
			rule: "type",
		},
		{
			name: "email",
			values: map[string]any{
				"employees": 1,
				"enabled":   false,
				"email":     "invalid",
			},
			key:  "email",
			rule: "email",
		},
		{
			name: "phone",
			values: map[string]any{
				"employees": 1,
				"enabled":   false,
				"phone":     "8 (999) 123-45-67",
			},
			key:  "phone",
			rule: "e164",
		},
		{
			name: "choice",
			values: map[string]any{
				"employees": 1,
				"enabled":   false,
				"kind":      "private",
			},
			key:  "kind",
			rule: "oneof",
		},
		{
			name: "unknown",
			values: map[string]any{
				"employees": 1,
				"enabled":   false,
				"unknown":   true,
			},
			key:  "unknown",
			rule: "defined",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, validationErr := schema.Validate(testCase.values)
			var validationErrors field.ValidationErrors
			if !errors.As(validationErr, &validationErrors) {
				t.Fatalf("validation error = %T %v", validationErr, validationErr)
			}

			for _, item := range validationErrors {
				if item.Key == testCase.key && item.Rule == testCase.rule {
					return
				}
			}
			t.Fatalf(
				"missing error %s/%s in %#v",
				testCase.key,
				testCase.rule,
				validationErrors,
			)
		})
	}
}

func TestSchemaValidatePartialDistinguishesOmittedAndEmptyRequiredValues(t *testing.T) {
	required := true
	schema, err := field.Compile([]field.Definition{
		{Key: "catalog_id", Type: field.TypeString, Label: "Catalog", Required: &required},
		{Key: "limit", Type: field.TypeInteger, Label: "Limit"},
		{Key: "note", Type: field.TypeString, Label: "Note"},
	}, standardResolver())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("omitted required", func(t *testing.T) {
		partial, err := schema.ValidatePartial(map[string]any{"limit": json.Number("7")})
		if err != nil || partial["limit"] != int64(7) {
			t.Fatalf("partial values = %#v, %v", partial, err)
		}
	})

	for name, value := range map[string]any{"empty": "", "nil": nil} {
		t.Run(name+" required", func(t *testing.T) {
			_, err := schema.ValidatePartial(map[string]any{"catalog_id": value})
			assertValidationError(t, err, "catalog_id", "required")
		})
	}

	t.Run("optional empty", func(t *testing.T) {
		partial, err := schema.ValidatePartial(map[string]any{"note": ""})
		if err != nil || len(partial) != 0 {
			t.Fatalf("partial values = %#v, %v", partial, err)
		}
	})

	t.Run("invalid supplied type", func(t *testing.T) {
		_, err := schema.ValidatePartial(map[string]any{"limit": "invalid"})
		assertValidationError(t, err, "limit", "type")
	})

	if _, err := schema.Validate(map[string]any{"limit": int64(7)}); err == nil {
		t.Fatal("full validation accepted an omitted required value")
	}
	if _, err := schema.ValidatePartial(map[string]any{"unknown": true}); err == nil {
		t.Fatal("partial validation accepted an unknown value")
	}
}

func assertValidationError(t *testing.T, err error, key, rule string) {
	t.Helper()

	var validationErrors field.ValidationErrors
	if !errors.As(err, &validationErrors) {
		t.Fatalf("validation error = %T %v", err, err)
	}
	for _, item := range validationErrors {
		if item.Key == key && item.Rule == rule {
			return
		}
	}
	t.Fatalf("missing error %s/%s in %#v", key, rule, validationErrors)
}

func TestSchemaPhonePatternAndStepMetadata(t *testing.T) {
	floatStep := 0.25
	schema, err := field.Compile(
		[]field.Definition{
			{
				Key:   "amount",
				Type:  field.TypeFloat,
				Label: "Amount",
				Options: field.FloatOptions{
					Step: &floatStep,
				},
			},
			{
				Key:   "phone",
				Type:  field.TypePhone,
				Label: "Phone",
				Options: field.PhoneOptions{
					Pattern: `^07\d{9}$`,
				},
			},
		},
		standardResolver(),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := schema.Validate(map[string]any{
		"amount": 0.3,
		"phone":  "07123456789",
	}); err != nil {
		t.Fatalf("step was used for value validation: %v", err)
	}

	_, err = schema.Validate(map[string]any{"phone": "+79991234567"})
	var validationErrors field.ValidationErrors
	if !errors.As(err, &validationErrors) ||
		len(validationErrors) != 1 ||
		validationErrors[0].Rule != "pattern" {
		t.Fatalf("custom phone pattern error = %#v, %v", validationErrors, err)
	}
}

func TestCompileRejectsInvalidDefinitions(t *testing.T) {
	testCases := []struct {
		name       string
		definition field.Definition
		contains   string
	}{
		{
			name: "unknown type",
			definition: field.Definition{
				Key: "value", Type: "unknown", Label: "Value",
			},
			contains: "unknown type",
		},
		{
			name: "required rule",
			definition: field.Definition{
				Key: "value", Type: field.TypeString, Label: "Value",
				Rules: []string{"required"},
			},
			contains: "managed by Required",
		},
		{
			name: "unknown rule",
			definition: field.Definition{
				Key: "value", Type: field.TypeString, Label: "Value",
				Rules: []string{"not_registered"},
			},
			contains: "invalid validation rules",
		},
		{
			name: "invalid step",
			definition: field.Definition{
				Key: "value", Type: field.TypeInteger, Label: "Value",
				Options: field.IntegerOptions{
					Step: int64Pointer(0),
				},
			},
			contains: "step must be greater",
		},
		{
			name: "invalid pattern",
			definition: field.Definition{
				Key: "value", Type: field.TypePhone, Label: "Value",
				Options: field.PhoneOptions{Pattern: `(`},
			},
			contains: "compile phone pattern",
		},
		{
			name: "duplicate choice",
			definition: field.Definition{
				Key: "value", Type: field.TypeRadio, Label: "Value",
				Options: field.RadioOptions{
					Choices: []field.Choice{
						{Value: "same", Label: "First"},
						{Value: "same", Label: "Second"},
					},
				},
			},
			contains: "duplicate choice",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := field.Compile(
				[]field.Definition{testCase.definition},
				standardResolver(),
			)
			if err == nil || !strings.Contains(err.Error(), testCase.contains) {
				t.Fatalf("compile error = %v", err)
			}
		})
	}

	_, err := field.Compile(
		[]field.Definition{
			{Key: "same", Type: field.TypeString, Label: "First"},
			{Key: "same", Type: field.TypeString, Label: "Second"},
		},
		standardResolver(),
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate field key") {
		t.Fatalf("duplicate key error = %v", err)
	}
}

func TestSchemaDefinitionsAreCloned(t *testing.T) {
	required := true
	choices := []field.Choice{{Value: "one", Label: "One"}}
	definitions := []field.Definition{
		{
			Key:      "value",
			Type:     field.TypeSelect,
			Label:    "Value",
			Required: &required,
			Rules:    []string{"min=1"},
			Options: field.SelectOptions{
				Choices: choices,
			},
		},
	}

	schema, err := field.Compile(definitions, standardResolver())
	if err != nil {
		t.Fatal(err)
	}

	definitions[0].Rules[0] = "max=0"
	definitions[0].Required = boolPointer(false)
	choices[0].Value = "changed"

	first := schema.Definitions()
	first[0].Rules[0] = "changed"
	firstOptions := first[0].Options.(field.SelectOptions)
	firstOptions.Choices[0].Value = "changed"

	second := schema.Definitions()
	secondOptions := second[0].Options.(field.SelectOptions)
	if second[0].Rules[0] != "min=1" ||
		second[0].Required == nil ||
		!*second[0].Required ||
		secondOptions.Choices[0].Value != "one" {
		t.Fatalf("schema definitions were mutated: %#v", second)
	}
}
