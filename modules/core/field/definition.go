package field

import (
	"fmt"
	"reflect"

	"github.com/vernal96/go-cms-kernel/filesystem"
)

type TypeCode string

// StorageKind describes persistence and comparison semantics independently
// from a field's semantic type and editor.
type StorageKind string

const (
	StorageString    StorageKind = "string"
	StorageInteger   StorageKind = "integer"
	StorageFloat     StorageKind = "float"
	StorageBoolean   StorageKind = "boolean"
	StorageTimestamp StorageKind = "timestamp"
	StorageReference StorageKind = "reference"
	StorageJSON      StorageKind = "json"
)

func ValidStorageKind(kind StorageKind) bool {
	switch kind {
	case StorageString, StorageInteger, StorageFloat, StorageBoolean,
		StorageTimestamp, StorageReference, StorageJSON:
		return true
	default:
		return false
	}
}

const (
	TypeString   TypeCode = "string"
	TypeInteger  TypeCode = "int"
	TypeFloat    TypeCode = "float"
	TypeCheckbox TypeCode = "checkbox"
	TypeRadio    TypeCode = "radio"
	TypeSelect   TypeCode = "select"
	TypeTextarea TypeCode = "textarea"
	TypeEmail    TypeCode = "email"
	TypePhone    TypeCode = "phone"
	TypeFile     TypeCode = "file"
	TypeMedia    TypeCode = "media"
	TypeJSON     TypeCode = "json"
	TypeRepeater TypeCode = "repeater"
)

type Definition struct {
	// Public allows a Profile.Params value in public site projections.
	Public      bool
	Key         string
	Type        TypeCode
	Label       string
	Required    *bool
	Rules       []string
	Options     any
	Editor      EditorCode
	VisibleWhen *VisibleWhen
}

// EditorCode is optional admin presentation metadata. It deliberately does
// not change how a value is persisted or validated by a field Type.
type EditorCode string

// VisibleWhen is a small declarative condition for dynamic forms. It is not
// an expression language: a field is shown when another field equals Value.
type VisibleWhen struct {
	Field string `json:"field"`
	Value any    `json:"value"`
}

// ListOptions configures an ordered list of scalar values. Zero MaxItems is unbounded.
type ListOptions struct {
	Multiple bool `json:"multiple,omitempty"`
	MinItems int  `json:"min_items,omitempty"`
	MaxItems int  `json:"max_items,omitempty"`
}

type StringOptions = ListOptions
type MediaOptions struct {
	Multiple     bool   `json:"multiple,omitempty"`
	MinItems     int    `json:"min_items,omitempty"`
	MaxItems     int    `json:"max_items,omitempty"`
	SettingsCode string `json:"settings_code,omitempty"`
}

type IntegerOptions struct {
	Multiple bool `json:"multiple,omitempty"`
	MinItems int  `json:"min_items,omitempty"`
	MaxItems int  `json:"max_items,omitempty"`

	Step *int64 `json:"step,omitempty"`
}

type FloatOptions struct {
	Multiple bool `json:"multiple,omitempty"`
	MinItems int  `json:"min_items,omitempty"`
	MaxItems int  `json:"max_items,omitempty"`

	Step *float64 `json:"step,omitempty"`
}

type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type RadioOptions struct {
	Choices []Choice `json:"choices"`
}

type SelectOptions struct {
	Choices  []Choice `json:"choices"`
	Multiple bool     `json:"multiple"`
	MinItems int      `json:"min_items,omitempty"`
	MaxItems int      `json:"max_items,omitempty"`
}

type PhoneOptions struct {
	Multiple bool `json:"multiple,omitempty"`
	MinItems int  `json:"min_items,omitempty"`
	MaxItems int  `json:"max_items,omitempty"`

	Pattern string `json:"pattern,omitempty"`
}

type FileOptions struct {
	Multiple bool `json:"multiple,omitempty"`
	MinItems int  `json:"min_items,omitempty"`
	MaxItems int  `json:"max_items,omitempty"`

	Storages  []filesystem.Code `json:"storages,omitempty"`
	MIMETypes []string          `json:"mime_types,omitempty"`
}

// CompileContext carries the current resolver and composite ancestry through
// nested compilation. Composite types must reuse Compile for their children.
type CompileContext struct {
	Types      TypeResolver
	composites []TypeCode
}

func (ctx CompileContext) Compile(definitions []Definition) (*Schema, error) {
	return compile(definitions, ctx, false)
}

// EnterComposite rejects recursive use of a composite, including through
// contributed wrapper types, without knowing any concrete registry.
func (ctx CompileContext) EnterComposite(code TypeCode) (CompileContext, error) {
	for _, parent := range ctx.composites {
		if parent == code {
			return ctx, fmt.Errorf("nested %s fields are not supported", code)
		}
	}
	ctx.composites = append(append([]TypeCode(nil), ctx.composites...), code)
	return ctx, nil
}

// DefaultValueType supplies a semantic value for an omitted field. Its empty
// input still goes through normalization and validation.
type DefaultValueType interface{ DefaultValue() any }

type Type interface {
	Code() TypeCode
	Compile(ctx CompileContext, options any) (ValueType, error)
}

type ValueType interface {
	Normalize(any) (any, error)
	Empty(any) bool
	Validate(any) error
	Rules() []string
	Example() any
}

// StorageValueType is implemented by compiled value types that can be stored
// as typed resource-field rows. Multiple reports whether a normalized slice is
// persisted as ordered rows rather than as opaque JSON.
type StorageValueType interface {
	ValueType
	StorageKind() StorageKind
	Multiple() bool
}

// ReferenceValueType identifies the entity addressed by a reference value.
type ReferenceValueType interface {
	StorageValueType
	ReferenceTarget() string
}

const ReferenceMedia = "media"

type StoredValue struct {
	ReferenceTarget string      `json:"reference_target,omitempty"`
	References      []Reference `json:"references,omitempty"`
	Key             string
	Position        int
	Kind            StorageKind
	Multiple        bool `json:"multiple"`
	Value           any
}

type TypeResolver interface {
	FieldType(TypeCode) (Type, bool)
}

type RuleError struct {
	Rule  string
	Param string
}

func (e RuleError) Error() string {
	if e.Param == "" {
		return e.Rule
	}

	return fmt.Sprintf("%s=%s", e.Rule, e.Param)
}

type ValidationError struct {
	Key   string
	Rule  string
	Param string
}

type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return "field validation failed"
	}

	first := e[0]
	if first.Param == "" {
		return fmt.Sprintf(
			"field %q failed validation rule %q",
			first.Key,
			first.Rule,
		)
	}

	return fmt.Sprintf(
		"field %q failed validation rule %q with parameter %q",
		first.Key,
		first.Rule,
		first.Param,
	)
}

func CloneDefinitions(source []Definition) []Definition {
	if source == nil {
		return nil
	}

	result := make([]Definition, len(source))
	for index, definition := range source {
		result[index] = definition
		result[index].Rules = append([]string(nil), definition.Rules...)
		result[index].Options = cloneOptions(definition.Options)
		if definition.VisibleWhen != nil {
			condition := *definition.VisibleWhen
			condition.Value = cloneEditorValue(condition.Value)
			result[index].VisibleWhen = &condition
		}

		if definition.Required != nil {
			required := *definition.Required
			result[index].Required = &required
		}
	}

	return result
}

func cloneEditorValue(value any) any {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneEditorValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = cloneEditorValue(item)
		}
		return result
	default:
		return typed
	}
}

func cloneOptions(options any) any {
	switch typed := options.(type) {
	case *MediaOptions:
		if typed == nil {
			return (*MediaOptions)(nil)
		}
		result := *typed
		return &result
	case *ListOptions:
		if typed == nil {
			return (*ListOptions)(nil)
		}
		result := *typed
		return &result
	case RepeaterOptions:
		typed.Fields = CloneDefinitions(typed.Fields)
		return typed
	case *RepeaterOptions:
		if typed == nil {
			return (*RepeaterOptions)(nil)
		}
		result := *typed
		result.Fields = CloneDefinitions(typed.Fields)
		return &result
	case RadioOptions:
		typed.Choices = append([]Choice(nil), typed.Choices...)
		return typed

	case *RadioOptions:
		if typed == nil {
			return (*RadioOptions)(nil)
		}
		result := *typed
		result.Choices = append([]Choice(nil), typed.Choices...)
		return &result

	case SelectOptions:
		typed.Choices = append([]Choice(nil), typed.Choices...)
		return typed

	case *SelectOptions:
		if typed == nil {
			return (*SelectOptions)(nil)
		}
		result := *typed
		result.Choices = append([]Choice(nil), typed.Choices...)
		return &result

	case IntegerOptions:
		if typed.Step != nil {
			step := *typed.Step
			typed.Step = &step
		}
		return typed

	case *IntegerOptions:
		if typed == nil {
			return (*IntegerOptions)(nil)
		}
		result := *typed
		if typed.Step != nil {
			step := *typed.Step
			result.Step = &step
		}
		return &result

	case FloatOptions:
		if typed.Step != nil {
			step := *typed.Step
			typed.Step = &step
		}
		return typed

	case *FloatOptions:
		if typed == nil {
			return (*FloatOptions)(nil)
		}
		result := *typed
		if typed.Step != nil {
			step := *typed.Step
			result.Step = &step
		}
		return &result

	case PhoneOptions:
		return typed

	case *PhoneOptions:
		if typed == nil {
			return (*PhoneOptions)(nil)
		}
		result := *typed
		return &result

	case FileOptions:
		typed.Storages = append([]filesystem.Code(nil), typed.Storages...)
		typed.MIMETypes = append([]string(nil), typed.MIMETypes...)
		return typed

	case *FileOptions:
		if typed == nil {
			return (*FileOptions)(nil)
		}
		result := *typed
		result.Storages = append([]filesystem.Code(nil), typed.Storages...)
		result.MIMETypes = append([]string(nil), typed.MIMETypes...)
		return &result

	default:
		return cloneEditorValue(typed)
	}
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// Types is a local immutable-by-convention collection; it is never a global registry.
type Types []Type

func (types Types) FieldType(code TypeCode) (Type, bool) {
	for _, item := range types {
		if item.Code() == code {
			return item, true
		}
	}
	return nil, false
}
func (types Types) FieldTypes() []TypeCode {
	result := make([]TypeCode, len(types))
	for i, item := range types {
		result[i] = item.Code()
	}
	return result
}

type TypeCatalog interface {
	TypeResolver
	FieldTypes() []TypeCode
}

// PublicDefinitions returns detached definitions explicitly allowed for public site use.
func PublicDefinitions(definitions []Definition) []Definition {
	result := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Public {
			result = append(result, definition)
		}
	}
	return CloneDefinitions(result)
}
