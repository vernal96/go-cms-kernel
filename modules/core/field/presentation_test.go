package field_test

import (
	"encoding/json"
	"testing"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

func TestFieldDefinitionsSerializeAllOptions(t *testing.T) {
	required := true
	integerStep := int64(2)
	floatStep := 0.25
	definitions := []field.Definition{
		{Key: "text", Type: field.TypeString, Label: "Text", Required: &required, Rules: []string{"min=2"}},
		{Key: "integer", Type: field.TypeInteger, Label: "Integer", Options: field.IntegerOptions{Step: &integerStep}},
		{Key: "float", Type: field.TypeFloat, Label: "Float", Options: field.FloatOptions{Step: &floatStep}},
		{Key: "checkbox", Type: field.TypeCheckbox, Label: "Checkbox"},
		{Key: "radio", Type: field.TypeRadio, Label: "Radio", Options: field.RadioOptions{Choices: []field.Choice{{Value: "one", Label: "One"}}}},
		{Key: "select", Type: field.TypeSelect, Label: "Select", Options: field.SelectOptions{Multiple: true, Choices: []field.Choice{{Value: "one", Label: "One"}}}},
		{Key: "textarea", Type: field.TypeTextarea, Label: "Textarea"},
		{Key: "email", Type: field.TypeEmail, Label: "Email"},
		{Key: "phone", Type: field.TypePhone, Label: "Phone", Options: field.PhoneOptions{}},
		{Key: "asset", Type: field.TypeFile, Label: "Asset", Options: field.FileOptions{Storages: []filesystem.Code{"public"}, MIMETypes: []string{"image/*"}}},
		{Key: "html", Type: field.TypeString, Label: "HTML", Editor: "html", VisibleWhen: &field.VisibleWhen{Field: "enabled", Value: true}},
	}

	result, err := field.DescribeDefinitions(definitions, standardResolver())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(definitions) || decoded[0]["required"] != true {
		t.Fatalf("fields = %#v", decoded)
	}
	integerOptions := decoded[1]["options"].(map[string]any)
	floatOptions := decoded[2]["options"].(map[string]any)
	selectOptions := decoded[5]["options"].(map[string]any)
	phoneOptions := decoded[8]["options"].(map[string]any)
	fileOptions := decoded[9]["options"].(map[string]any)
	if integerOptions["step"] != float64(2) || floatOptions["step"] != 0.25 ||
		selectOptions["multiple"] != true || len(phoneOptions) != 0 ||
		fileOptions["storages"].([]any)[0] != "public" || fileOptions["mime_types"].([]any)[0] != "image/*" {
		t.Fatalf("serialized options = %#v %#v %#v %#v %#v", integerOptions, floatOptions, selectOptions, phoneOptions, fileOptions)
	}
	if decoded[10]["editor"] != "html" || decoded[10]["visible_when"].(map[string]any)["field"] != "enabled" {
		t.Fatalf("editor metadata = %#v", decoded[10])
	}
}

func TestFieldDefinitionRejectsUnknownType(t *testing.T) {
	_, err := field.Describe(field.Definition{Key: "future", Type: "future", Label: "Future"}, standardResolver())
	if err == nil {
		t.Fatal("unknown field type was accepted")
	}
}
