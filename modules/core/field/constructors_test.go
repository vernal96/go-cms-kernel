package field_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/field/validation"
)

func TestStandardFieldConstructorsCompile(t *testing.T) {
	mediaMIMETypes := []string{"image/*"}
	nestedStep := int64(3)
	repeaterFields := []field.Definition{
		field.Integer("count", "Count", field.IntegerOptions{Step: &nestedStep}),
	}
	definitions := []field.Definition{
		field.String("string", "String", field.StringOptions{Multiple: true}),
		field.Integer("integer", "Integer", field.IntegerOptions{Multiple: true}),
		field.Float("float", "Float", field.FloatOptions{Multiple: true}),
		field.Checkbox("checkbox", "Checkbox"),
		field.Radio("radio", "Radio", field.RadioOptions{Choices: []field.Choice{{Value: "yes", Label: "Yes"}}}),
		field.Select("select", "Select", field.SelectOptions{Choices: []field.Choice{{Value: "yes", Label: "Yes"}}, Multiple: true}),
		field.Textarea("textarea", "Textarea", field.StringOptions{Multiple: true}),
		field.Email("email", "Email", field.StringOptions{Multiple: true}),
		field.Phone("phone", "Phone", field.PhoneOptions{Multiple: true}),
		field.Media("media", "Media", field.MediaOptions{Disk: filesystem.Code("media"), VirtualPath: "images", SettingsCode: "default", MIMETypes: mediaMIMETypes}),
		field.JSON("json", "JSON"),
		field.Repeater("repeater", "Repeater", field.RepeaterOptions{Fields: repeaterFields}),
	}
	mediaMIMETypes[0] = "text/plain"
	nestedStep = 9
	repeaterFields[0].Key = "changed"
	want := []field.TypeCode{
		field.TypeString, field.TypeInteger, field.TypeFloat, field.TypeCheckbox,
		field.TypeRadio, field.TypeSelect, field.TypeTextarea, field.TypeEmail,
		field.TypePhone, field.TypeMedia, field.TypeJSON, field.TypeRepeater,
	}
	for i := range definitions {
		if definitions[i].Type != want[i] {
			t.Errorf("constructor %q type = %q, want %q", definitions[i].Key, definitions[i].Type, want[i])
		}
	}
	wantOptions := []reflect.Type{
		reflect.TypeOf(field.StringOptions{}), reflect.TypeOf(field.IntegerOptions{}),
		reflect.TypeOf(field.FloatOptions{}), nil, reflect.TypeOf(field.RadioOptions{}),
		reflect.TypeOf(field.SelectOptions{}), reflect.TypeOf(field.StringOptions{}),
		reflect.TypeOf(field.StringOptions{}), reflect.TypeOf(field.PhoneOptions{}),
		reflect.TypeOf(field.MediaOptions{}), nil, reflect.TypeOf(field.RepeaterOptions{}),
	}
	for i := range definitions {
		if got := reflect.TypeOf(definitions[i].Options); got != wantOptions[i] {
			t.Errorf("constructor %q options type = %v, want %v", definitions[i].Key, got, wantOptions[i])
		}
	}
	if got := definitions[9].Options.(field.MediaOptions).MIMETypes[0]; got != "image/*" {
		t.Errorf("media constructor retained caller MIME slice value %q", got)
	}
	repeaterOption := definitions[11].Options.(field.RepeaterOptions).Fields[0]
	if repeaterOption.Key != "count" || *repeaterOption.Options.(field.IntegerOptions).Step != 3 {
		t.Errorf("repeater constructor retained caller fields: %#v", repeaterOption)
	}
	if _, err := field.Compile(definitions, field.StandardTypes()); err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if _, err := field.CompilePersistent(definitions, field.StandardTypes()); err != nil {
		t.Fatalf("CompilePersistent() error = %v", err)
	}
}

func TestLegacyDefinitionLiteralStillCompiles(t *testing.T) {
	legacy := field.Definition{Key: "title", Label: "Title", Type: field.TypeString, Options: field.StringOptions{}}
	if _, err := field.CompilePersistent([]field.Definition{legacy}, field.StandardTypes()); err != nil {
		t.Fatalf("CompilePersistent() legacy literal error = %v", err)
	}
}

func TestFieldConstructorWithMethodsAreDetached(t *testing.T) {
	choices := []field.Choice{{Value: "draft", Label: "Draft"}}
	step := int64(2)
	base := field.Integer("count", "Count", field.IntegerOptions{Step: &step})
	validators := []field.ValidatorDefinition{validation.Min(1)}
	visibleValue := map[string]any{"enabled": []any{true}}
	condition := &field.VisibleWhen{Field: "enabled", Value: visibleValue}
	derived := base.WithRequired(true).
		WithValidators(validators...).
		WithEditor("number").
		WithVisibleWhen(condition).
		WithPublic(true)

	step = 4
	validators[0].Type = "invalid"
	visibleValue["enabled"].([]any)[0] = false
	condition.Field = "changed"
	choices[0].Label = "Changed"

	if base.Required || base.Editor != "" || base.Public || len(base.Validators) != 0 || base.VisibleWhen != nil {
		t.Fatal("With methods mutated the source definition")
	}
	if got := *derived.Options.(field.IntegerOptions).Step; got != 2 {
		t.Fatalf("derived step = %d, want 2", got)
	}
	if derived.Validators[0].Type != "min" {
		t.Fatalf("derived validator type = %q, want min", derived.Validators[0].Type)
	}
	if derived.VisibleWhen.Field != "enabled" || derived.VisibleWhen.Value.(map[string]any)["enabled"].([]any)[0] != true {
		t.Fatalf("derived visibility condition changed through source: %#v", derived.VisibleWhen)
	}

	baseStep := *base.Options.(field.IntegerOptions).Step
	*derived.Options.(field.IntegerOptions).Step = 6
	if got := *base.Options.(field.IntegerOptions).Step; got != baseStep {
		t.Fatalf("mutating derived options changed base step to %d", got)
	}

	validatorSource := field.String("title", "Title").WithValidators(validation.MinLength(3))
	validatorDerived := validatorSource.WithPublic(true)
	validatorDerived.Validators[0].Type = "changed"
	if got := validatorSource.Validators[0].Type; got != "min_length" {
		t.Fatalf("mutating derived validators changed source type to %q", got)
	}

	visibilityInput := map[string]any{"enabled": []any{true}}
	visibilitySource := field.String("visible", "Visible").WithVisibleWhen(&field.VisibleWhen{Field: "toggle", Value: &visibilityInput})
	visibilityDerived := visibilitySource.WithPublic(true)
	(*visibilityDerived.VisibleWhen.Value.(*map[string]any))["enabled"].([]any)[0] = false
	if got := visibilityInput["enabled"].([]any)[0]; got != true {
		t.Fatalf("mutating derived visibility changed input to %v", got)
	}
	(*visibilitySource.VisibleWhen.Value.(*map[string]any))["enabled"].([]any)[0] = "source changed"
	if got := (*visibilityDerived.VisibleWhen.Value.(*map[string]any))["enabled"].([]any)[0]; got != false {
		t.Fatalf("mutating source visibility changed derived value to %v", got)
	}

	choiceDef := field.Select("state", "State", field.SelectOptions{Choices: choices})
	choices[0].Value = "changed"
	if got := choiceDef.Options.(field.SelectOptions).Choices[0].Value; got != "draft" {
		t.Fatalf("constructor retained caller choices, got %q", got)
	}
}

func TestSimpleFieldConstructorRejectsMultipleOptions(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != "field constructor accepts at most one options value" {
			t.Fatalf("panic = %v, want an at-most-one-options message", recovered)
		}
	}()
	_ = field.String("title", "Title", field.StringOptions{}, field.StringOptions{})
}

func TestTypedDefinitionValidatorsRunAtCompileTime(t *testing.T) {
	definition := field.String("title", "Title").WithValidators(validation.MinLength(3))
	schema, err := field.Compile([]field.Definition{definition}, field.StandardTypes())
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if _, err := schema.Validate(map[string]any{"title": "ab"}); err == nil {
		t.Fatal("short value passed the typed definition validator")
	}
	if _, err := schema.Validate(map[string]any{"title": "good"}); err != nil {
		t.Fatalf("valid value failed validation: %v", err)
	}
}

func TestTypedConstructorWorksForCustomFieldType(t *testing.T) {
	definition := func(key, label string, options customProductOptions) field.Definition {
		return field.Definition{Key: key, Label: label, Type: "catalog.product", Options: options}
	}
	compiled, err := field.Compile([]field.Definition{definition("product", "Product", customProductOptions{CategoryID: 8})}, customTypeResolver{})
	if err != nil {
		t.Fatalf("Compile() custom type error = %v", err)
	}
	if got := compiled.Definitions()[0].Options.(customProductOptions).CategoryID; got != 8 {
		t.Fatalf("custom options category = %d, want 8", got)
	}
}

type customTypeResolver struct{}

func (customTypeResolver) FieldType(code field.TypeCode) (field.Type, bool) {
	if code == "catalog.product" {
		return customProductType{}, true
	}
	return nil, false
}

type customProductType struct{}

type customProductOptions struct{ CategoryID int64 }

func (customProductType) Code() field.TypeCode { return "catalog.product" }
func (customProductType) Compile(_ field.CompileContext, options any) (field.ValueType, error) {
	if _, ok := options.(customProductOptions); !ok {
		return nil, os.ErrInvalid
	}
	return customProductValue{}, nil
}

type customProductValue struct{}

func (customProductValue) Normalize(value any) (any, error) { return value, nil }
func (customProductValue) Empty(any) bool                   { return false }
func (customProductValue) Validate(any) error               { return nil }
func (customProductValue) Example() any                     { return nil }

func TestMediaConstructorRejectsWrongOptionsAtCompileTime(t *testing.T) {
	workDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "invalid.go")
	source := `package main
import "github.com/vernal96/go-cms-kernel/modules/core/field"
var _ = field.Media("image", "Image", field.SelectOptions{})
func main() {}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", path)
	command.Dir = workDir
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("invalid Media options unexpectedly compiled")
	}
	if !strings.Contains(string(output), "cannot use field.SelectOptions") {
		t.Fatalf("compile failed for an unexpected reason: %s", output)
	}
}
