package resourcetype

import (
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

type contractTestType struct {
	metadata  Metadata
	normalize func(Payload) (Payload, error)
	calls     *int
}

func (contractTestType) Code() Code         { return "contract_test" }
func (contractTestType) PathMode() PathMode { return PathRoute }
func (t contractTestType) Metadata() Metadata {
	return t.metadata
}
func (t contractTestType) Normalize(payload Payload) (Payload, error) {
	if t.calls != nil {
		(*t.calls)++
	}
	if t.normalize != nil {
		return t.normalize(payload)
	}
	return payload, nil
}

type contractFieldTypes map[field.TypeCode]field.Type

func (r contractFieldTypes) FieldType(code field.TypeCode) (field.Type, bool) {
	fieldType, exists := r[code]
	return fieldType, exists
}

func contractResolver() contractFieldTypes {
	result := make(contractFieldTypes)
	for _, fieldType := range field.StandardTypes() {
		result[fieldType.Code()] = fieldType
	}
	return result
}

func TestCompiledTypeEnforcesSettingsSchemaAfterNormalize(t *testing.T) {
	required := true
	metadata := Metadata{
		Label: "Contract test",
		SettingsFields: []field.Definition{
			{Key: "catalog_id", Type: field.TypeString, Label: "Catalog", Required: &required},
			{Key: "limit", Type: field.TypeInteger, Label: "Limit"},
			{Key: "mode", Type: field.TypeString, Label: "Mode"},
		},
		SettingsDefaults: map[string]any{"mode": "standard"},
	}

	testCases := []struct {
		name      string
		normalize func(Payload) (Payload, error)
		wantMode  string
		wantError string
	}{
		{
			name: "valid transformed setting",
			normalize: func(payload Payload) (Payload, error) {
				payload.TypeSettings["mode"] = strings.ToUpper(payload.TypeSettings["mode"].(string))
				return payload, nil
			},
			wantMode: "STANDARD",
		},
		{
			name: "unknown setting added",
			normalize: func(payload Payload) (Payload, error) {
				payload.TypeSettings["unknown"] = true
				return payload, nil
			},
			wantError: `field "unknown"`,
		},
		{
			name: "integer changed to string",
			normalize: func(payload Payload) (Payload, error) {
				payload.TypeSettings["limit"] = "invalid"
				return payload, nil
			},
			wantError: `field "limit"`,
		},
		{
			name: "required setting deleted",
			normalize: func(payload Payload) (Payload, error) {
				delete(payload.TypeSettings, "catalog_id")
				return payload, nil
			},
			wantError: `field "catalog_id"`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			calls := 0
			compiled, err := Compile(contractTestType{
				metadata: metadata, normalize: testCase.normalize, calls: &calls,
			}, contractResolver())
			if err != nil {
				t.Fatal(err)
			}

			input := Payload{TypeSettings: map[string]any{
				"catalog_id": "primary",
				"limit":      int64(10),
			}}
			normalized, err := compiled.Normalize(input)
			if calls != 1 {
				t.Fatalf("custom Normalize calls = %d", calls)
			}
			if testCase.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
					t.Fatalf("Normalize error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if normalized.TypeSettings["mode"] != testCase.wantMode {
				t.Fatalf("normalized settings = %#v", normalized.TypeSettings)
			}
			if _, exists := input.TypeSettings["mode"]; exists {
				t.Fatalf("input payload was mutated: %#v", input.TypeSettings)
			}
		})
	}
}

func TestCompiledTypeEnforcesDeclaredContentTypes(t *testing.T) {
	metadata := Metadata{
		Label:        "Content contract",
		Capabilities: Capabilities{SupportsContent: true},
		ContentTypes: []ContentTypeOption{
			{Code: "html", Label: "HTML", Editor: ContentEditorHTML},
			{Code: "markdown", Label: "Markdown", Editor: ContentEditorTextarea},
		},
		SettingsDefaults: map[string]any{},
	}

	testCases := []struct {
		name        string
		incoming    string
		transformed string
		want        string
		wantError   bool
		wantCalls   int
	}{
		{name: "allowed html", incoming: "html", want: "html", wantCalls: 1},
		{name: "second declared type", incoming: "markdown", want: "markdown", wantCalls: 1},
		{name: "unsupported incoming", incoming: "xml", wantError: true},
		{name: "unsupported transformation", incoming: "html", transformed: "xml", wantError: true, wantCalls: 1},
		{name: "valid transformation", incoming: "html", transformed: "markdown", want: "markdown", wantCalls: 1},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			calls := 0
			compiled, err := Compile(contractTestType{
				metadata: metadata,
				calls:    &calls,
				normalize: func(payload Payload) (Payload, error) {
					if testCase.transformed != "" {
						value := testCase.transformed
						payload.ContentType = &value
					}
					return payload, nil
				},
			}, contractResolver())
			if err != nil {
				t.Fatal(err)
			}

			contentType := testCase.incoming
			normalized, err := compiled.Normalize(Payload{ContentType: &contentType})
			if calls != testCase.wantCalls {
				t.Fatalf("custom Normalize calls = %d, want %d", calls, testCase.wantCalls)
			}
			if testCase.wantError {
				if err == nil || !strings.Contains(err.Error(), "content_type") {
					t.Fatalf("Normalize error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if normalized.ContentType == nil || *normalized.ContentType != testCase.want {
				t.Fatalf("normalized content type = %#v", normalized.ContentType)
			}
		})
	}
}

func TestCompiledTypeLeavesContentRulesToTypesWithoutContentCapability(t *testing.T) {
	calls := 0
	compiled, err := Compile(contractTestType{
		metadata: Metadata{Label: "No content", SettingsDefaults: map[string]any{}},
		calls:    &calls,
	}, contractResolver())
	if err != nil {
		t.Fatal(err)
	}

	contentType := "custom"
	normalized, err := compiled.Normalize(Payload{ContentType: &contentType})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || normalized.ContentType == nil || *normalized.ContentType != contentType {
		t.Fatalf("normalized payload = %#v, calls = %d", normalized, calls)
	}
}
