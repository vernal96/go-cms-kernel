package forms

import (
	"context"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/permission"
)

func TestFormsDefinitionContributionsCompileWithBuiltins(t *testing.T) {
	profile := formsDefinitionTestProfile(formsDefinitionContributor{
		add: func(_ context.Context, definitions *Definitions) error {
			if err := definitions.RegisterFieldType(testDefinitionFieldType{}); err != nil {
				return err
			}
			return definitions.RegisterPermissionEntity(permission.Entity{Code: "custom_action"})
		},
	})
	definitions, err := kernel.CompileProfileDefinitions(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	registry := definitions.Registry()
	for _, code := range []field.TypeCode{FieldTypeCaptcha, FieldTypeConsent, FieldTypeUpload, "forms.test"} {
		if _, ok := registry.FieldType(code); !ok {
			t.Errorf("field type %q missing from compiled profile", code)
		}
	}
	if _, ok := registry.FieldType(field.TypeString); !ok {
		t.Error("standard string field type missing from compiled profile")
	}
	for _, validator := range field.StandardValidatorTypes() {
		if _, ok := registry.ValidatorType(validator.Code()); !ok {
			t.Errorf("standard validator %q missing from compiled profile", validator.Code())
		}
	}
	for _, code := range []permission.Code{"forms.form.read", "forms.result.read", "forms.action.read", "forms.status.read", "forms.custom_action.read"} {
		if _, ok := registry.Permission(code); !ok {
			t.Errorf("permission %q missing from compiled profile", code)
		}
	}
}

func TestFormsDefinitionContributionErrorRejectsProfile(t *testing.T) {
	t.Run("field type", func(t *testing.T) {
		profile := formsDefinitionTestProfile(formsDefinitionContributor{
			add: func(_ context.Context, definitions *Definitions) error {
				if err := definitions.RegisterFieldType(testDefinitionFieldType{}); err != nil {
					return err
				}
				return definitions.RegisterFieldType(testDefinitionFieldType{})
			},
		})
		if _, err := kernel.CompileProfileDefinitions(context.Background(), profile); err == nil {
			t.Fatal("duplicate Forms field type did not reject profile definitions")
		}
	})
	t.Run("permission entity", func(t *testing.T) {
		profile := formsDefinitionTestProfile(formsDefinitionContributor{
			add: func(_ context.Context, definitions *Definitions) error {
				if err := definitions.RegisterPermissionEntity(permission.Entity{Code: "custom"}); err != nil {
					return err
				}
				return definitions.RegisterPermissionEntity(permission.Entity{Code: "custom"})
			},
		})
		if _, err := kernel.CompileProfileDefinitions(context.Background(), profile); err == nil {
			t.Fatal("duplicate Forms permission entity did not reject profile definitions")
		}
	})
}

func TestFormsDefinitionsRejectRegistrationAfterFinalization(t *testing.T) {
	module := New(Config{}).(module)
	definitions, err := module.NewDefinitions(context.Background(), kernel.DefinitionContext{})
	if err != nil {
		t.Fatal(err)
	}
	formsDefinitions := definitions.(*Definitions)
	if err := formsDefinitions.FinalizeDefinitionBuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := formsDefinitions.RegisterFieldType(testDefinitionFieldType{}); err == nil {
		t.Fatal("field type registration after finalization was accepted")
	}
	if err := formsDefinitions.RegisterPermissionEntity(permission.Entity{Code: "late"}); err == nil {
		t.Fatal("permission registration after finalization was accepted")
	}
}

func formsDefinitionTestProfile(contributor formsDefinitionContributor) kernel.Profile {
	return kernel.Profile{
		Code: "forms-definitions-test",
		Modules: []kernel.Module{
			definitionTestModule{code: "core"},
			definitionTestModule{code: "mail"},
			New(Config{}),
			contributor,
		},
	}
}

type definitionTestModule struct{ code kernel.ModuleCode }

func (m definitionTestModule) Code() kernel.ModuleCode { return m.code }
func (m definitionTestModule) Registry() (kernel.ModuleRegistry, error) {
	if m.code != "core" {
		return kernel.ModuleRegistry{}, nil
	}
	return kernel.ModuleRegistry{FieldTypes: field.StandardTypes(), ValidatorTypes: field.StandardValidatorTypes()}, nil
}
func (definitionTestModule) Validate(context.Context, kernel.ModuleValidationContext) error {
	return nil
}
func (m definitionTestModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return definitionTestRuntime{code: m.code}, nil
}

type definitionTestRuntime struct{ code kernel.ModuleCode }

func (r definitionTestRuntime) ModuleCode() kernel.ModuleCode { return r.code }

type formsDefinitionContributor struct {
	add func(context.Context, *Definitions) error
}

func (formsDefinitionContributor) Code() kernel.ModuleCode { return "forms_definition_test" }
func (formsDefinitionContributor) Dependencies() []kernel.ModuleCode {
	return []kernel.ModuleCode{ModuleCode}
}
func (formsDefinitionContributor) Validate(context.Context, kernel.ModuleValidationContext) error {
	return nil
}
func (formsDefinitionContributor) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return definitionTestRuntime{code: "forms_definition_test"}, nil
}
func (m formsDefinitionContributor) DefinitionContributions() []kernel.DefinitionContribution {
	return []kernel.DefinitionContribution{kernel.DefineFor[*Definitions](ModuleCode, m.add)}
}

type testDefinitionFieldType struct{}

func (testDefinitionFieldType) Code() field.TypeCode { return "forms.test" }
func (testDefinitionFieldType) Compile(field.CompileContext, any) (field.ValueType, error) {
	return captchaValue{}, nil
}
