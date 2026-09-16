package field

import (
	"encoding/json"
	"fmt"
	"strconv"
)

type RepeaterOptions struct {
	Fields   []Definition `json:"fields"`
	MinItems int          `json:"min_items,omitempty"`
	MaxItems int          `json:"max_items,omitempty"`
}

// JSON configuration restores ordinary typed nested options through the same
// decoder used by all field definition transports. Go declarations stay typed.
func (o *RepeaterOptions) UnmarshalJSON(raw []byte) error {
	decoded, err := DecodeOptions[struct {
		Fields   []Descriptor `json:"fields"`
		MinItems int          `json:"min_items,omitempty"`
		MaxItems int          `json:"max_items,omitempty"`
	}](json.RawMessage(raw))
	if err != nil {
		return err
	}
	o.MinItems, o.MaxItems = decoded.MinItems, decoded.MaxItems
	o.Fields = make([]Definition, len(decoded.Fields))
	for i, desc := range decoded.Fields {
		options, err := DecodeOptionsJSON(desc.Type, desc.Options)
		if err != nil {
			return fmt.Errorf("field %q options: %w", desc.Key, err)
		}
		required := desc.Required
		o.Fields[i] = Definition{Key: desc.Key, Type: desc.Type, Label: desc.Label, Required: &required, Rules: desc.Rules, Options: options, Editor: desc.Editor, VisibleWhen: desc.VisibleWhen}
	}
	return nil
}

// MarshalJSON keeps declaration options transportable even outside an admin
// descriptor. Admin rendering uses DescribeOptions to resolve nested editors.
func (o RepeaterOptions) MarshalJSON() ([]byte, error) {
	fields := make([]Descriptor, len(o.Fields))
	for i, def := range o.Fields {
		raw, err := EncodeOptionsJSON(def.Options)
		if err != nil {
			return nil, err
		}
		fields[i] = definitionDescriptor(def, raw, def.Editor)
	}
	return json.Marshal(repeaterPresentation{fields, o.MinItems, o.MaxItems})
}

type repeaterType struct{}

func (repeaterType) Code() TypeCode { return TypeRepeater }
func (repeaterType) Compile(ctx CompileContext, options any) (ValueType, error) {
	ctx, err := ctx.EnterComposite(TypeRepeater)
	if err != nil {
		return nil, err
	}
	var config RepeaterOptions
	switch typed := options.(type) {
	case RepeaterOptions:
		config = typed
	case *RepeaterOptions:
		if typed == nil {
			return nil, fmt.Errorf("repeater options are nil")
		}
		config = *typed
	default:
		config, err = DecodeOptions[RepeaterOptions](options)
		if err != nil {
			return nil, err
		}
	}
	if config.MinItems < 0 || config.MaxItems < 0 || (config.MaxItems > 0 && config.MaxItems < config.MinItems) {
		return nil, fmt.Errorf("invalid repeater item limits")
	}
	if len(config.Fields) == 0 {
		return nil, fmt.Errorf("repeater fields are empty")
	}
	schema, err := ctx.Compile(config.Fields)
	if err != nil {
		return nil, err
	}
	return repeaterValue{schema: schema, types: ctx.Types, min: config.MinItems, max: config.MaxItems}, nil
}

type repeaterValue struct {
	schema   *Schema
	types    TypeResolver
	min, max int
}

func (repeaterValue) StorageKind() StorageKind { return StorageJSON }
func (repeaterValue) Multiple() bool           { return false }
func (repeaterValue) DefaultValue() any        { return []any{} }
func (repeaterValue) Rules() []string          { return nil }
func (repeaterValue) Example() any             { return []any{} }
func (repeaterValue) Empty(value any) bool     { return len(value.([]any)) == 0 }
func (repeaterValue) Validate(any) error       { return nil }
func (v repeaterValue) Normalize(value any) (any, error) {
	var rows []any
	switch typed := value.(type) {
	case []any:
		rows = typed
	case []map[string]any:
		rows = make([]any, len(typed))
		for i, row := range typed {
			rows[i] = row
		}
	default:
		return nil, fmt.Errorf("expected array of objects, got %T", value)
	}
	if len(rows) < v.min {
		return nil, RuleError{Rule: "min", Param: strconv.Itoa(v.min)}
	}
	if v.max > 0 && len(rows) > v.max {
		return nil, RuleError{Rule: "max", Param: strconv.Itoa(v.max)}
	}
	result := make([]any, len(rows))
	failures := ValidationErrors{}
	for i, value := range rows {
		prefix := fmt.Sprintf("[%d]", i)
		row, ok := value.(map[string]any)
		if !ok || row == nil {
			failures = append(failures, ValidationError{Key: prefix, Rule: "type"})
			continue
		}
		normalized, err := v.schema.Validate(row)
		if err != nil {
			failures = append(failures, prefixedValidationErrors(prefix, err, "value")...)
			continue
		}
		result[i] = normalized
	}
	if len(failures) > 0 {
		return nil, failures
	}
	return result, nil
}
func (v repeaterValue) References(value any) ([]Reference, error) {
	normalized, err := v.Normalize(value)
	if err != nil {
		return nil, err
	}
	result := []Reference{}
	for i, row := range normalized.([]any) {
		refs, err := v.schema.References(row.(map[string]any))
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			ref.Path = append([]string{strconv.Itoa(i)}, ref.Path...)
			ref.Key = ""
			result = append(result, ref)
		}
	}
	return result, nil
}

type repeaterPresentation struct {
	Fields   []Descriptor `json:"fields"`
	MinItems int          `json:"min_items,omitempty"`
	MaxItems int          `json:"max_items,omitempty"`
}

func (v repeaterValue) DescribeOptions() (any, error) {
	fields, err := DescribeDefinitions(v.schema.Definitions(), v.types)
	if err != nil {
		return nil, err
	}
	return repeaterPresentation{fields, v.min, v.max}, nil
}
