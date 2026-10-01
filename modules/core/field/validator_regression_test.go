package field_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/field/validation"
)

type scopedValidatorType struct {
	scope   field.ValidatorScope
	check   func(any) error
	context *field.ValidatorContext
}

func (scopedValidatorType) Code() field.ValidatorCode { return "example.scoped" }
func (v scopedValidatorType) Compile(ctx field.ValidatorContext, _ any) (field.Validator, error) {
	if v.context != nil {
		*v.context = ctx
	}
	return scopedValidator{scope: v.scope, check: v.check}, nil
}

type scopedValidator struct {
	scope field.ValidatorScope
	check func(any) error
}

func (v scopedValidator) Scope() field.ValidatorScope { return v.scope }
func (v scopedValidator) Validate(value any) error    { return v.check(value) }

func TestContributedValidatorScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scope     field.ValidatorScope
		check     func(any) error
		good, bad []any
		path      string
	}{
		{"whole list", field.ValidatorScopeValue, func(value any) error {
			items, ok := value.([]string)
			if !ok {
				t.Fatalf("whole-list validator received %T", value)
			}
			if len(items) < 2 {
				return errors.New("at least two items")
			}
			return nil
		}, []any{"a", "b"}, []any{}, "values"},
		{"each item", field.ValidatorScopeItems, func(value any) error {
			item, ok := value.(string)
			if !ok {
				t.Fatalf("item validator received %T", value)
			}
			if len(item) < 2 {
				return errors.New("too short")
			}
			return nil
		}, []any{"aa", "bb"}, []any{"aa", "b"}, "values[1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ctx field.ValidatorContext
			resolver := validatorResolver{Types: field.StandardTypes(), extra: scopedValidatorType{scope: tc.scope, check: tc.check, context: &ctx}}
			definition := field.Definition{Key: "values", Type: field.TypeString, Label: "Values", Options: field.StringOptions{Multiple: true}, Validators: []field.ValidatorDefinition{{Type: "example.scoped"}, validation.MaxItems(3)}}
			schema, err := field.Compile([]field.Definition{definition}, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if !ctx.Multiple || ctx.Storage != field.StorageString || ctx.ValueType == nil {
				t.Fatalf("context: %#v", ctx)
			}
			if _, err := schema.Validate(map[string]any{"values": tc.good}); err != nil {
				t.Fatal(err)
			}
			_, err = schema.Validate(map[string]any{"values": tc.bad})
			var failures field.ValidationErrors
			if !errors.As(err, &failures) || len(failures) != 1 || failures[0].Key != tc.path || failures[0].Code != "example.scoped" {
				t.Fatalf("failures: %#v (%v)", failures, err)
			}
		})
	}
}

func TestValidatorScopeRejectsInvalidContracts(t *testing.T) {
	for _, tc := range []struct {
		scope    field.ValidatorScope
		multiple bool
	}{{"", true}, {"unknown", true}, {field.ValidatorScopeItems, false}} {
		t.Run(fmt.Sprintf("%s/multiple=%v", tc.scope, tc.multiple), func(t *testing.T) {
			resolver := validatorResolver{Types: field.StandardTypes(), extra: scopedValidatorType{scope: tc.scope}}
			_, err := field.Compile([]field.Definition{{Key: "v", Type: field.TypeString, Label: "V", Options: field.StringOptions{Multiple: tc.multiple}, Validators: []field.ValidatorDefinition{{Type: "example.scoped"}}}}, resolver)
			if err == nil {
				t.Fatal("invalid scope accepted")
			}
		})
	}
}

func TestRequiredValidatorOptionsRejectNull(t *testing.T) {
	for _, tc := range []struct {
		code    field.ValidatorCode
		typ     field.TypeCode
		options string
	}{
		{"min", field.TypeInteger, `{"value":null}`},
		{"max_length", field.TypeString, `{"value":null}`},
		{"regex", field.TypeString, `{"value":null}`},
		{"between", field.TypeInteger, `{"min":null,"max":10}`},
		{"between", field.TypeInteger, `{"min":0,"max":null}`},
		{"in", field.TypeInteger, `{"values":null}`},
	} {
		t.Run(string(tc.code)+tc.options, func(t *testing.T) {
			_, err := field.Compile([]field.Definition{{Key: "v", Type: tc.typ, Label: "V", Validators: []field.ValidatorDefinition{{Type: tc.code, Options: json.RawMessage(tc.options)}}}}, field.StandardTypes())
			if err == nil {
				t.Fatal("null required option accepted")
			}
		})
	}
	// Explicit zero remains a valid, meaningful option.
	schema, err := field.Compile([]field.Definition{{Key: "v", Type: field.TypeInteger, Label: "V", Validators: []field.ValidatorDefinition{validation.Max(0)}}}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schema.Validate(map[string]any{"v": 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := schema.Validate(map[string]any{"v": 1}); err == nil {
		t.Fatal("zero bound ignored")
	}
}

func TestIntegerMembershipPreservesPrecision(t *testing.T) {
	for _, value := range []int64{9007199254740993, math.MaxInt64, math.MinInt64} {
		other := value - 1
		if value == math.MinInt64 {
			other = value + 1
		}
		for _, tc := range []struct {
			name             string
			make             func(...any) field.ValidatorDefinition
			multiple, negate bool
		}{
			{"in", validation.In, false, false}, {"not_in", validation.NotIn, false, true},
			{"list in", validation.In, true, false}, {"list not_in", validation.NotIn, true, true},
			{"contains", validation.Contains, true, false}, {"doesnt_contain", validation.DoesntContain, true, true},
		} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, value), func(t *testing.T) {
				original := tc.make(value)
				raw, err := json.Marshal(original)
				if err != nil {
					t.Fatal(err)
				}
				var restored field.ValidatorDefinition
				if err := json.Unmarshal(raw, &restored); err != nil {
					t.Fatal(err)
				}
				for _, validator := range []field.ValidatorDefinition{original, restored} {
					definition := field.Definition{Key: "v", Type: field.TypeInteger, Label: "V", Options: field.IntegerOptions{Multiple: tc.multiple}, Validators: []field.ValidatorDefinition{validator}}
					schema, err := field.Compile([]field.Definition{definition}, field.StandardTypes())
					if err != nil {
						t.Fatal(err)
					}
					// Schema snapshots and descriptors must not round the saved configuration.
					cloned, _ := json.Marshal(schema.Definitions()[0].Validators[0])
					if string(cloned) != string(raw) {
						t.Fatalf("snapshot rounded %s to %s", raw, cloned)
					}
					descriptor, err := field.Describe(schema.Definitions()[0], field.StandardTypes())
					if err != nil {
						t.Fatal(err)
					}
					transported, _ := json.Marshal(descriptor.Validators[0])
					if string(transported) != string(raw) {
						t.Fatalf("descriptor rounded %s to %s", raw, transported)
					}
					for _, input := range []int64{value, other} {
						var supplied any = input
						if tc.multiple {
							supplied = []any{input}
						}
						_, err := schema.Validate(map[string]any{"v": supplied})
						wantValid := (input == value) != tc.negate
						if (err == nil) != wantValid {
							t.Fatalf("input=%d valid=%v: %v", input, wantValid, err)
						}
						if err != nil {
							var failures field.ValidationErrors
							if !errors.As(err, &failures) || !reflect.DeepEqual(failures[0].Params["values"], []any{value}) {
								t.Fatalf("rounded failure params: %#v", failures)
							}
						}
					}
				}
			})
		}
	}
}
