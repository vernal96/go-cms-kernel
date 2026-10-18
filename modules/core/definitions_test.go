package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
)

const (
	definitionTestField     field.TypeCode      = "custom_label"
	definitionTestValidator field.ValidatorCode = "custom_starts_with"
)

type definitionTestFieldType struct{}

func (definitionTestFieldType) Code() field.TypeCode { return definitionTestField }
func (definitionTestFieldType) Compile(_ field.CompileContext, options any) (field.ValueType, error) {
	prefix, _ := options.(string)
	return definitionTestValueType{prefix: prefix}, nil
}

type definitionTestValueType struct{ prefix string }

func (v definitionTestValueType) Normalize(value any) (any, error) {
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("expected string, got %T", value)
	}
	return text, nil
}
func (definitionTestValueType) Empty(value any) bool {
	text, ok := value.(string)
	return !ok || text == ""
}
func (v definitionTestValueType) Validate(value any) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("expected string, got %T", value)
	}
	if v.prefix != "" && !strings.HasPrefix(text, v.prefix) {
		return errors.New("prefix mismatch")
	}
	return nil
}
func (definitionTestValueType) Example() any                   { return "label: example" }
func (definitionTestValueType) StorageKind() field.StorageKind { return field.StorageString }
func (definitionTestValueType) Multiple() bool                 { return false }

type definitionTestValidatorType struct{}

func (definitionTestValidatorType) Code() field.ValidatorCode { return definitionTestValidator }
func (definitionTestValidatorType) Compile(_ field.ValidatorContext, _ any) (field.Validator, error) {
	return definitionTestValidatorImpl{}, nil
}

type definitionTestValidatorImpl struct{}

func (definitionTestValidatorImpl) Scope() field.ValidatorScope { return field.ValidatorScopeValue }
func (definitionTestValidatorImpl) Validate(value any) error {
	text, ok := value.(string)
	if !ok || !strings.Contains(text, ":") {
		return errors.New("colon is required")
	}
	return nil
}

type definitionTestResourceType struct{ fields []field.Definition }

func (definitionTestResourceType) Code() resourcetype.Code         { return "custom_record" }
func (definitionTestResourceType) PathMode() resourcetype.PathMode { return resourcetype.PathNone }
func (r definitionTestResourceType) Metadata() resourcetype.Metadata {
	return resourcetype.Metadata{
		Label:            "Custom record",
		Capabilities:     resourcetype.Capabilities{SupportsFields: true, MutableType: true},
		SettingsFields:   r.fields,
		SettingsDefaults: map[string]any{},
	}
}
func (definitionTestResourceType) Normalize(payload resourcetype.Payload) (resourcetype.Payload, error) {
	return payload, nil
}

type definitionTestModule struct {
	code     kernel.ModuleCode
	register func(*Definitions) error
}

func (m definitionTestModule) Code() kernel.ModuleCode { return m.code }
func (definitionTestModule) Dependencies() []kernel.ModuleCode {
	return []kernel.ModuleCode{ModuleCode}
}
func (definitionTestModule) Validate(context.Context, kernel.ModuleValidationContext) error {
	return nil
}
func (m definitionTestModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return definitionTestRuntime{code: m.code}, nil
}
func (m definitionTestModule) DefinitionContributions() []kernel.DefinitionContribution {
	return []kernel.DefinitionContribution{kernel.DefineFor[*Definitions](ModuleCode, func(_ context.Context, definitions *Definitions) error {
		return m.register(definitions)
	})}
}

type definitionTestRuntime struct{ code kernel.ModuleCode }

func (r definitionTestRuntime) ModuleCode() kernel.ModuleCode { return r.code }

func TestCoreDefinitionContributionsCompileTemplatesAndLateResourceFields(t *testing.T) {
	fieldContributor := definitionTestModule{code: "custom_fields", register: func(definitions *Definitions) error {
		if err := definitions.RegisterFieldType(definitionTestFieldType{}); err != nil {
			return err
		}
		return definitions.RegisterValidatorType(definitionTestValidatorType{})
	}}
	resourceContributor := definitionTestModule{code: "custom_resource", register: func(definitions *Definitions) error {
		return definitions.RegisterResourceType(definitionTestResourceType{fields: []field.Definition{{
			Key: "custom_setting", Type: definitionTestField, Label: "Custom setting", Options: "resource:",
			Validators: []field.ValidatorDefinition{{Type: definitionTestValidator}},
		}}})
	}}
	profile := kernel.Profile{
		Code:    "definitions",
		Modules: []kernel.Module{New(Config{}), resourceContributor, fieldContributor},
		Templates: []template.Definition{{Code: "custom", Label: "Custom", Fields: []field.Definition{{
			Key: "custom_field", Type: definitionTestField, Label: "Custom field", Options: "template:",
			Validators: []field.ValidatorDefinition{{Type: definitionTestValidator}},
		}}}},
	}
	definitions, err := kernel.CompileProfileDefinitions(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	registry := definitions.Registry()
	if _, ok := registry.ResourceType(resourcetype.Page); !ok {
		t.Fatal("Core standard resource types were not preserved")
	}
	if _, ok := registry.Permission("core.site.read"); !ok {
		t.Fatal("Core site permission was not preserved")
	}
	compiledTemplate, err := template.Compile(profile.Templates, registry)
	if err != nil {
		t.Fatal(err)
	}
	runtime, ok := compiledTemplate.Template("custom")
	if !ok {
		t.Fatal("contributed template was not compiled")
	}
	if _, err := runtime.FieldSchema().Validate(map[string]any{"custom_field": "template:valid"}); err != nil {
		t.Fatalf("contributed field validation failed: %v", err)
	}
	if _, err := runtime.FieldSchema().Validate(map[string]any{"custom_field": "missing colon"}); err == nil {
		t.Fatal("contributed validator accepted invalid template value")
	}
	resourceType, ok := registry.ResourceType("custom_record")
	if !ok {
		t.Fatal("resource type declared before its field type was not compiled")
	}
	metadata := resourceType.Metadata()
	settings, err := field.Compile(metadata.SettingsFields, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Validate(map[string]any{"custom_setting": "resource:valid"}); err != nil {
		t.Fatalf("resource settings did not preserve custom field options: %v", err)
	}
	if _, err := settings.Validate(map[string]any{"custom_setting": "wrong"}); err == nil {
		t.Fatal("resource settings ignored contributed validator")
	}
}

func TestCoreDefinitionContributionRequiresProviderAndRejectsDuplicateFieldTypes(t *testing.T) {
	first := definitionTestModule{code: "first_fields", register: func(definitions *Definitions) error {
		return definitions.RegisterFieldType(definitionTestFieldType{})
	}}
	second := definitionTestModule{code: "second_fields", register: func(definitions *Definitions) error {
		return definitions.RegisterFieldType(definitionTestFieldType{})
	}}
	_, err := kernel.CompileProfileDefinitions(context.Background(), kernel.Profile{
		Code: "duplicate-fields", Modules: []kernel.Module{New(Config{}), first, second},
	})
	if err == nil || !strings.Contains(err.Error(), "field type \"custom_label\" already exists") {
		t.Fatalf("duplicate cross-owner field registration error = %v", err)
	}

	withoutContributor, err := kernel.CompileProfileDefinitions(context.Background(), kernel.Profile{
		Code: "missing-contributor", Modules: []kernel.Module{New(Config{})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = template.Compile([]template.Definition{{Code: "missing", Label: "Missing", Fields: []field.Definition{{
		Key: "custom_field", Type: definitionTestField, Label: "Custom field",
	}}}}, withoutContributor.Registry())
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("template without field contributor error = %v", err)
	}
}
