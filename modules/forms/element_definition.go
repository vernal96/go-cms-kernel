package forms

import (
	"encoding/json"
	"fmt"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

// ElementDefinition is the declarative path for an element with ordinary
// configuration fields. Special domain checks may be added with Validate.
type ElementDefinition struct {
	Description ElementTypeMetadata
	FieldTypes  field.TypeResolver
	Validate    func(json.RawMessage) error
}

func (e ElementDefinition) Code() ElementTypeCode { return e.Description.Code }
func (e ElementDefinition) Metadata() ElementTypeMetadata {
	result := e.Description
	result.Fields = field.CloneConfigFields(result.Fields)
	return result
}
func (e ElementDefinition) ValidateConfig(raw json.RawMessage) error {
	values, err := field.DecodeOptions[map[string]any](raw)
	if err != nil || values == nil {
		return fmt.Errorf("%w: element config must be an object", ErrInvalid)
	}
	definitions := make([]field.Definition, len(e.Description.Fields))
	for i, item := range e.Description.Fields {
		options, err := field.EncodeOptionsJSON(item.Options)
		if err != nil {
			return err
		}
		decoded, err := field.DecodeOptionsJSON(item.Type, options)
		if err != nil {
			return err
		}
		required := item.Required
		definitions[i] = field.Definition{Key: item.Key, Type: item.Type, Label: item.Label, Required: &required, Rules: item.Rules, Options: decoded}
	}
	resolver := e.FieldTypes
	if resolver == nil {
		resolver = field.StandardTypes()
	}
	schema, err := field.Compile(definitions, resolver)
	if err != nil {
		return fmt.Errorf("%w: element schema: %v", ErrInvalid, err)
	}
	if _, err = schema.Validate(values); err != nil {
		return fmt.Errorf("%w: element config: %v", ErrInvalid, err)
	}
	if e.Validate != nil {
		return e.Validate(raw)
	}
	return nil
}
