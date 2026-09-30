package field

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
)

// ValidatorDefinition is JSON transportable configuration, not executable state.
type ValidatorCode string

type ValidatorDefinition struct {
	Type    ValidatorCode `json:"type"`
	Options any           `json:"options,omitempty"`
}

type ValidatorContext struct {
	FieldType TypeCode
	ValueType ValueType
	Multiple  bool
	Storage   StorageKind
}

type Validator interface{ Validate(any) error }

type ValidatorType interface {
	Code() ValidatorCode
	Compile(ValidatorContext, any) (Validator, error)
}

type ValidatorResolver interface {
	ValidatorType(ValidatorCode) (ValidatorType, bool)
}

type ValidatorMetadata struct {
	Code          ValidatorCode            `json:"code"`
	Label         string                   `json:"label"`
	Options       []ConfigField            `json:"options"`
	OptionsEditor EditorCode               `json:"options_editor,omitempty"`
	FieldTypes    []TypeCode               `json:"field_types,omitempty"`
	Multiple      *bool                    `json:"multiple,omitempty"`
	Applicability []ValidatorApplicability `json:"applicability,omitempty"`
}

type ValidatorApplicability struct {
	FieldTypes []TypeCode `json:"field_types,omitempty"`
	Multiple   *bool      `json:"multiple,omitempty"`
}

type ValidatorMetadataProvider interface{ ValidatorMetadata() ValidatorMetadata }

type DescribedValidatorType struct {
	ValidatorType
	Presentation ValidatorMetadata
}

func (t DescribedValidatorType) ValidatorMetadata() ValidatorMetadata {
	return CloneValidatorMetadata(t.Presentation)
}

func CloneValidatorMetadata(m ValidatorMetadata) ValidatorMetadata {
	m.Options = CloneConfigFields(m.Options)
	m.FieldTypes = append([]TypeCode(nil), m.FieldTypes...)
	m.Applicability = append([]ValidatorApplicability(nil), m.Applicability...)
	for i := range m.Applicability {
		m.Applicability[i].FieldTypes = append([]TypeCode(nil), m.Applicability[i].FieldTypes...)
		if m.Applicability[i].Multiple != nil {
			value := *m.Applicability[i].Multiple
			m.Applicability[i].Multiple = &value
		}
	}
	if m.Multiple != nil {
		value := *m.Multiple
		m.Multiple = &value
	}
	return m
}

func DescribeValidatorType(t ValidatorType) ValidatorMetadata {
	m := ValidatorMetadata{Code: t.Code(), Label: string(t.Code()), Options: []ConfigField{}}
	if p, ok := t.(ValidatorMetadataProvider); ok {
		m = CloneValidatorMetadata(p.ValidatorMetadata())
	}
	m.Code = t.Code()
	return m
}

func SnapshotValidatorType(t ValidatorType) ValidatorType {
	return DescribedValidatorType{ValidatorType: t, Presentation: DescribeValidatorType(t)}
}

// CloneValidatorDefinitions detaches both typed Go options and JSON options.
func CloneValidatorDefinitions(source []ValidatorDefinition) []ValidatorDefinition {
	if source == nil {
		return nil
	}
	result := make([]ValidatorDefinition, len(source))
	for i, item := range source {
		result[i] = item
		if item.Options != nil {
			raw, err := json.Marshal(item.Options)
			if err == nil {
				value := reflectClone(item.Options, raw)
				result[i].Options = value
			} else {
				result[i].Options = cloneEditorValue(item.Options)
			}
		}
	}
	return result
}

func reflectClone(original any, raw []byte) any {
	// JSON-shaped options are the public contract. Preserve a Go option's concrete
	// type when possible so typed declarations remain convenient to inspect.
	value := reflect.New(reflect.TypeOf(original))
	if reflect.TypeOf(original).Kind() == reflect.Pointer {
		value = reflect.New(reflect.TypeOf(original).Elem())
	}
	if err := json.Unmarshal(raw, value.Interface()); err == nil {
		if reflect.TypeOf(original).Kind() == reflect.Pointer {
			return value.Interface()
		}
		return value.Elem().Interface()
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err == nil {
		return generic
	}
	return cloneEditorValue(original)
}

type ValidatorTypes []ValidatorType

func (types ValidatorTypes) ValidatorType(code ValidatorCode) (ValidatorType, bool) {
	for _, item := range types {
		if item.Code() == code {
			return item, true
		}
	}
	return nil, false
}
func (types ValidatorTypes) ValidatorTypes() []ValidatorCode {
	result := make([]ValidatorCode, 0, len(types))
	for _, item := range types {
		result = append(result, item.Code())
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func validatorFailure(code ValidatorCode, params map[string]any) error {
	return ValidationErrors{{Code: code, Params: cloneEditorValue(params).(map[string]any)}}
}

var validatorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

func ValidateValidatorCode(code ValidatorCode) error {
	if !validatorCodePattern.MatchString(string(code)) {
		return fmt.Errorf("invalid validator code %q", code)
	}
	return nil
}
