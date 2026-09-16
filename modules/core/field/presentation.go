package field

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ConfigField describes a configuration editor. Validation remains owned by
// the contributed type, action or element, not by the frontend.
type ConfigField struct {
	Default  any            `json:"default,omitempty"`
	Key      string         `json:"key"`
	Label    string         `json:"label"`
	Type     TypeCode       `json:"type"`
	Required bool           `json:"required"`
	Editor   EditorCode     `json:"editor,omitempty"`
	Rules    []string       `json:"rules,omitempty"`
	Options  map[string]any `json:"options,omitempty"`
}

type Metadata struct {
	Code          TypeCode      `json:"code"`
	Label         string        `json:"label"`
	Editor        EditorCode    `json:"editor,omitempty"`
	Options       []ConfigField `json:"options"`
	OptionsEditor EditorCode    `json:"options_editor,omitempty"`
}

type MetadataProvider interface{ FieldMetadata() Metadata }

// DescribedType adds editor metadata to a semantic type without forwarding
// its compiler by hand. Metadata is copied when exposed to callers.
type DescribedType struct {
	Type
	Presentation Metadata
}

func (t DescribedType) Code() TypeCode {
	if nilInterface(t.Type) {
		return ""
	}
	return t.Type.Code()
}
func (t DescribedType) Compile(ctx CompileContext, options any) (ValueType, error) {
	if nilInterface(t.Type) {
		return nil, fmt.Errorf("described field type is nil")
	}
	return t.Type.Compile(ctx, options)
}

// SnapshotType freezes presentation metadata at registration. The semantic
// compiler must itself be immutable, as with every registered field type.
func SnapshotType(t Type) Type {
	return DescribedType{Type: t, Presentation: DescribeType(t)}
}

func (t DescribedType) FieldMetadata() Metadata { return CloneMetadata(t.Presentation) }

func CloneConfigFields(fields []ConfigField) []ConfigField {
	result := append([]ConfigField{}, fields...)
	for i := range result {
		result[i].Default = cloneEditorValue(fields[i].Default)
		result[i].Rules = append([]string{}, fields[i].Rules...)
		if fields[i].Options != nil {
			result[i].Options = cloneEditorValue(fields[i].Options).(map[string]any)
		}
	}
	return result
}
func CloneMetadata(m Metadata) Metadata { m.Options = CloneConfigFields(m.Options); return m }
func DescribeType(t Type) Metadata {
	result := Metadata{Label: string(t.Code()), Options: []ConfigField{}}
	if provider, ok := t.(MetadataProvider); ok {
		result = CloneMetadata(provider.FieldMetadata())
	}
	result.Code = t.Code()
	return result
}

type Descriptor struct {
	Public      *bool           `json:"public,omitempty"`
	Key         string          `json:"key"`
	Type        TypeCode        `json:"type"`
	Label       string          `json:"label"`
	Required    bool            `json:"required"`
	Rules       []string        `json:"rules"`
	Options     json.RawMessage `json:"options,omitempty"`
	Editor      EditorCode      `json:"editor,omitempty"`
	VisibleWhen *VisibleWhen    `json:"visible_when,omitempty"`
}

// Describe uses the same registered type as schema compilation. Custom types
// serialize their own JSON-shaped options and may choose a built-in editor.
func Describe(definition Definition, resolver TypeResolver) (Descriptor, error) {
	if resolver == nil {
		return Descriptor{}, fmt.Errorf("field type resolver is nil")
	}
	t, exists := resolver.FieldType(definition.Type)
	if !exists || t == nil {
		return Descriptor{}, fmt.Errorf("field %q type %q is unavailable", definition.Key, definition.Type)
	}
	valueType, err := t.Compile(CompileContext{Types: resolver}, definition.Options)
	if err != nil {
		return Descriptor{}, fmt.Errorf("field %q: %w", definition.Key, err)
	}
	definition = CloneDefinitions([]Definition{definition})[0]
	presentationOptions := definition.Options
	presentationType := valueType
	if list, ok := valueType.(listValue); ok {
		presentationType = list.item
	}
	if presenter, ok := presentationType.(OptionsPresenter); ok {
		presentationOptions, err = presenter.DescribeOptions()
		if err != nil {
			return Descriptor{}, fmt.Errorf("field %q options: %w", definition.Key, err)
		}
	}
	options, err := json.Marshal(presentationOptions)
	if err != nil {
		return Descriptor{}, fmt.Errorf("field %q options: %w", definition.Key, err)
	}
	if string(options) == "null" {
		options = nil
	}
	editor := definition.Editor
	if editor == "" {
		editor = DescribeType(t).Editor
	}
	return definitionDescriptor(definition, options, editor), nil
}
func DescribeDefinitions(definitions []Definition, resolver TypeResolver) ([]Descriptor, error) {
	result := make([]Descriptor, len(definitions))
	for i, definition := range definitions {
		item, err := Describe(definition, resolver)
		if err != nil {
			return nil, err
		}
		result[i] = item
	}
	return result, nil
}

// DecodeOptions lets an extension use the same typed options from Go
// declarations and JSON configuration. Unknown keys and trailing data fail.
func DecodeOptions[T any](value any) (T, error) {
	var result T
	if value == nil {
		return result, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return result, fmt.Errorf("invalid trailing options data")
	}
	return result, nil
}

// OptionsPresenter exposes compiled composite options through ordinary field descriptors.
type OptionsPresenter interface{ DescribeOptions() (any, error) }

// definitionDescriptor is also used when encoding typed declaration options.
// Resolver-dependent editor selection remains in Describe.
func definitionDescriptor(definition Definition, options json.RawMessage, editor EditorCode) Descriptor {
	return Descriptor{Key: definition.Key, Type: definition.Type, Label: definition.Label,
		Required: definition.Required != nil && *definition.Required,
		Rules:    append([]string{}, definition.Rules...), Options: options,
		Editor: editor, VisibleWhen: definition.VisibleWhen}
}
