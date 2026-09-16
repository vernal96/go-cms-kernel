package field

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/go-playground/validator/v10"
)

type compiledField struct {
	definition Definition
	valueType  ValueType
	required   bool
	rules      string
}

type Schema struct {
	definitions []Definition
	fields      map[string]compiledField
	validator   *validator.Validate
}

// ValueShape describes binding compatibility, independently of value constraints.
type ValueShape struct {
	Type     TypeCode `json:"type"`
	Multiple bool     `json:"multiple"`
}

func (s *Schema) Shape(key string) (ValueShape, bool) {
	if s == nil {
		return ValueShape{}, false
	}
	compiled, exists := s.fields[key]
	if !exists {
		return ValueShape{}, false
	}
	shape := ValueShape{Type: compiled.definition.Type}
	if cardinality, ok := compiled.valueType.(interface{ Multiple() bool }); ok {
		shape.Multiple = cardinality.Multiple()
	}
	return shape, true
}

func Compile(
	definitions []Definition,
	resolver TypeResolver,
) (*Schema, error) {
	return compile(definitions, CompileContext{Types: resolver}, false)
}

// CompilePersistent compiles a schema whose normalized values are persisted
// as typed resource-field rows. Generic profile/widget schemas intentionally
// use Compile and may contain transient value types.
func CompilePersistent(
	definitions []Definition,
	resolver TypeResolver,
) (*Schema, error) {
	return compile(definitions, CompileContext{Types: resolver}, true)
}

func compile(
	definitions []Definition,
	ctx CompileContext,
	requireStorage bool,
) (*Schema, error) {
	if ctx.Types == nil {
		return nil, errors.New("field type resolver is nil")
	}

	definitions = CloneDefinitions(definitions)
	schema := &Schema{
		definitions: definitions,
		fields:      make(map[string]compiledField, len(definitions)),
		validator:   validator.New(),
	}

	for index, definition := range definitions {
		if definition.Key == "" ||
			strings.TrimSpace(definition.Key) != definition.Key {
			return nil, fmt.Errorf(
				"field at index %d has invalid key %q",
				index,
				definition.Key,
			)
		}
		if definition.Label == "" ||
			strings.TrimSpace(definition.Label) != definition.Label {
			return nil, fmt.Errorf(
				"field %q has invalid label %q",
				definition.Key,
				definition.Label,
			)
		}
		if definition.Type == "" {
			return nil, fmt.Errorf(
				"field %q has empty type",
				definition.Key,
			)
		}
		if definition.Editor != "" && strings.TrimSpace(string(definition.Editor)) != string(definition.Editor) {
			return nil, fmt.Errorf("field %q has invalid editor %q", definition.Key, definition.Editor)
		}
		if definition.VisibleWhen != nil && (definition.VisibleWhen.Field == "" || strings.TrimSpace(definition.VisibleWhen.Field) != definition.VisibleWhen.Field) {
			return nil, fmt.Errorf("field %q has invalid visibility condition", definition.Key)
		}
		if _, exists := schema.fields[definition.Key]; exists {
			return nil, fmt.Errorf(
				"duplicate field key %q",
				definition.Key,
			)
		}

		fieldType, exists := ctx.Types.FieldType(definition.Type)
		if !exists {
			return nil, fmt.Errorf(
				"field %q references unknown type %q",
				definition.Key,
				definition.Type,
			)
		}
		if nilInterface(fieldType) {
			return nil, fmt.Errorf(
				"field %q type %q is nil",
				definition.Key,
				definition.Type,
			)
		}

		valueType, err := fieldType.Compile(ctx, definition.Options)
		if err != nil {
			return nil, fmt.Errorf(
				"compile field %q type %q: %w",
				definition.Key,
				definition.Type,
				err,
			)
		}
		if nilInterface(valueType) {
			return nil, fmt.Errorf(
				"field type %q returned nil value type",
				definition.Type,
			)
		}
		if requireStorage {
			storage, ok := valueType.(StorageValueType)
			if !ok {
				return nil, fmt.Errorf(
					"field %q type %q returned value type without storage semantics",
					definition.Key,
					definition.Type,
				)
			}
			if !ValidStorageKind(storage.StorageKind()) {
				return nil, fmt.Errorf(
					"field %q type %q returned unsupported storage kind %q",
					definition.Key,
					definition.Type,
					storage.StorageKind(),
				)
			}
		}

		rules, err := compileRules(valueType.Rules(), definition.Rules)
		if err != nil {
			return nil, fmt.Errorf(
				"compile field %q rules: %w",
				definition.Key,
				err,
			)
		}
		if rules != "" {
			example := valueType.Example()
			if list, ok := valueType.(listValue); ok {
				example = list.item.Example()
			}
			err := safeValidate(
				schema.validator,
				definition.Key,
				example,
				rules,
			)
			var validationErrors validator.ValidationErrors
			if err != nil && !errors.As(err, &validationErrors) {
				return nil, fmt.Errorf(
					"compile field %q rules: %w",
					definition.Key,
					err,
				)
			}
		}

		schema.fields[definition.Key] = compiledField{
			definition: definition,
			valueType:  valueType,
			required: definition.Required != nil &&
				*definition.Required,
			rules: rules,
		}
	}

	return schema, nil
}

// StoredValues converts already normalized values to adapter-neutral typed
// rows. Validation remains owned by Schema.Validate.
func (s *Schema) StoredValues(values map[string]any) ([]StoredValue, error) {
	if s == nil {
		return nil, errors.New("field schema is nil")
	}
	result := make([]StoredValue, 0, len(values))
	for _, definition := range s.definitions {
		value, exists := values[definition.Key]
		if !exists {
			continue
		}
		compiled := s.fields[definition.Key]
		storage, ok := compiled.valueType.(StorageValueType)
		if !ok {
			return nil, fmt.Errorf("field %q has no storage semantics", definition.Key)
		}
		if !storage.Multiple() {
			stored := StoredValue{Key: definition.Key, Kind: storage.StorageKind(), Value: value}
			if collector, ok := compiled.valueType.(ReferenceCollector); ok && storage.StorageKind() == StorageJSON {
				refs, err := collector.References(value)
				if err != nil {
					return nil, fmt.Errorf("field %q references: %w", definition.Key, err)
				}
				stored.References = refs
			}
			if reference, ok := storage.(ReferenceValueType); ok {
				stored.ReferenceTarget = reference.ReferenceTarget()
			}
			result = append(result, stored)
			continue
		}
		referenceTarget := ""
		if reference, ok := storage.(ReferenceValueType); ok {
			referenceTarget = reference.ReferenceTarget()
		}
		switch items := value.(type) {
		case []string:
			for position, item := range items {
				result = append(result, StoredValue{Key: definition.Key, Position: position, Kind: storage.StorageKind(), Multiple: true, Value: item, ReferenceTarget: referenceTarget})
			}
		case []any:
			for position, item := range items {
				result = append(result, StoredValue{Key: definition.Key, Position: position, Kind: storage.StorageKind(), Multiple: true, Value: item, ReferenceTarget: referenceTarget})
			}
		default:
			return nil, fmt.Errorf("field %q normalized multi-value has type %T", definition.Key, value)
		}
	}
	return result, nil
}

func (s *Schema) Definitions() []Definition {
	if s == nil {
		return nil
	}

	return CloneDefinitions(s.definitions)
}

func (s *Schema) StorageKind(key string) (StorageKind, bool) {
	metadata, exists := s.Storage(key)
	return metadata.Kind, exists
}

// StorageMetadata describes the adapter-neutral persistence shape of a field.
// Multiple is true when one normalized value is stored as ordered rows.
type StorageMetadata struct {
	Kind     StorageKind
	Multiple bool
}

func (s *Schema) Storage(key string) (StorageMetadata, bool) {
	if s == nil {
		return StorageMetadata{}, false
	}
	compiled, exists := s.fields[key]
	if !exists {
		return StorageMetadata{}, false
	}
	storage, ok := compiled.valueType.(StorageValueType)
	if !ok || !ValidStorageKind(storage.StorageKind()) {
		return StorageMetadata{}, false
	}
	return StorageMetadata{Kind: storage.StorageKind(), Multiple: storage.Multiple()}, true
}

type FileReference struct {
	Key     string
	ID      int64
	Options FileOptions
}

func (s *Schema) FileReferences(values map[string]any) ([]FileReference, error) {
	if s == nil {
		return nil, errors.New("field schema is nil")
	}
	refs, err := s.References(values)
	if err != nil {
		return nil, err
	}
	result := make([]FileReference, 0)
	for _, ref := range refs {
		if ref.Target == ReferenceFile {
			result = append(result, FileReference{Key: ref.Key, ID: ref.ID, Options: ref.Options})
		}
	}
	return result, nil
}

func (s *Schema) Validate(
	values map[string]any,
) (map[string]any, error) {
	return s.validate(values, true)
}

// ValidatePartial validates and normalizes only supplied values. It is useful
// for partial configuration such as defaults, where required fields may be
// intentionally omitted and are enforced only after the final values are
// assembled.
func (s *Schema) ValidatePartial(
	values map[string]any,
) (map[string]any, error) {
	return s.validate(values, false)
}

func (s *Schema) validate(
	values map[string]any,
	requireAll bool,
) (map[string]any, error) {
	return s.validateDeferred(values, requireAll, nil)
}

// ValidateDeferred validates literal values while leaving explicitly deferred
// fields for a later full Validate call. Deferred values cannot also be literals.
func (s *Schema) ValidateDeferred(values map[string]any, deferred map[string]struct{}) (map[string]any, error) {
	if s == nil {
		return nil, errors.New("field schema is nil")
	}
	for key := range deferred {
		if _, exists := s.fields[key]; !exists {
			return nil, fmt.Errorf("unknown deferred field %q", key)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("field %q has both a literal and a deferred value", key)
		}
	}
	return s.validateDeferred(values, true, deferred)
}

func (s *Schema) validateDeferred(values map[string]any, requireAll bool, deferred map[string]struct{}) (map[string]any, error) {
	if s == nil {
		return nil, errors.New("field schema is nil")
	}

	result := make(map[string]any, len(values))
	validationErrors := make(ValidationErrors, 0)

	unknownKeys := make([]string, 0)
	for key := range values {
		if _, exists := s.fields[key]; !exists {
			unknownKeys = append(unknownKeys, key)
		}
	}
	sort.Strings(unknownKeys)
	for _, key := range unknownKeys {
		validationErrors = append(validationErrors, ValidationError{
			Key:  key,
			Rule: "defined",
		})
	}

	for _, definition := range s.definitions {
		if _, skip := deferred[definition.Key]; skip {
			continue
		}
		compiled := s.fields[definition.Key]
		value, exists := values[definition.Key]
		list, isList := compiled.valueType.(listValue)
		if isList && ((!exists && requireAll) || (exists && value == nil)) {
			if list.min > 0 {
				validationErrors = append(validationErrors, ValidationError{Key: definition.Key, Rule: "min_items", Param: fmt.Sprint(list.min)})
				continue
			}
			if exists {
				value = []any{}
			}
		}

		defaults, hasDefault := compiled.valueType.(DefaultValueType)
		if !exists && requireAll && hasDefault {
			value, exists = defaults.DefaultValue(), true
		}
		if !exists {
			if requireAll && compiled.required {
				validationErrors = append(
					validationErrors,
					ValidationError{
						Key:  definition.Key,
						Rule: "required",
					},
				)
			}
			continue
		}
		if inputEmpty(value) && !hasDefault && !isList {
			if compiled.required {
				validationErrors = append(
					validationErrors,
					ValidationError{
						Key:  definition.Key,
						Rule: "required",
					},
				)
			}
			continue
		}

		normalized, err := compiled.valueType.Normalize(value)
		if err != nil {
			validationErrors = append(validationErrors, prefixedValidationErrors(definition.Key, err, "type")...)
			continue
		}

		if compiled.valueType.Empty(normalized) {
			if hasDefault && !compiled.required {
				result[definition.Key] = normalized
			}
			if compiled.required {
				validationErrors = append(
					validationErrors,
					ValidationError{
						Key:  definition.Key,
						Rule: "required",
					},
				)
			}
			continue
		}

		if err := compiled.valueType.Validate(normalized); err != nil {
			validationErrors = append(validationErrors, prefixedValidationErrors(definition.Key, err, "value")...)
			continue
		}

		if compiled.rules != "" {
			failures := validateValueRules(s.validator, definition.Key, normalized, compiled.rules, compiled.valueType)
			if len(failures) > 0 {
				validationErrors = append(validationErrors, failures...)
				continue
			}
		}

		result[definition.Key] = normalized
	}

	if len(validationErrors) > 0 {
		return nil, validationErrors
	}

	return result, nil
}

func ruleErrorFrom(err error) (RuleError, bool) {
	var value RuleError
	if errors.As(err, &value) {
		return value, true
	}

	var pointer *RuleError
	if errors.As(err, &pointer) && pointer != nil {
		return *pointer, true
	}

	return RuleError{}, false
}

func compileRules(defaults, configured []string) (string, error) {
	rules := make([]string, 0, len(defaults)+len(configured))
	seen := make(map[string]struct{}, len(defaults)+len(configured))

	for _, source := range [][]string{defaults, configured} {
		for _, rule := range source {
			rule = strings.TrimSpace(rule)
			if rule == "" {
				return "", errors.New("validation rule is empty")
			}
			if strings.Contains(rule, ",") {
				return "", fmt.Errorf(
					"validation rule %q must be a single tag",
					rule,
				)
			}

			name := rule
			if separator := strings.IndexByte(name, '='); separator >= 0 {
				name = name[:separator]
			}
			if name == "required" || name == "omitempty" {
				return "", fmt.Errorf(
					"validation rule %q is managed by Required",
					name,
				)
			}
			if _, exists := seen[rule]; exists {
				continue
			}

			seen[rule] = struct{}{}
			rules = append(rules, rule)
		}
	}

	return strings.Join(rules, ","), nil
}

func safeValidate(
	validate *validator.Validate,
	key string,
	value any,
	rules string,
) (resultErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			resultErr = fmt.Errorf(
				"invalid validation rules %q: %v",
				rules,
				recovered,
			)
		}
	}()

	return validate.VarWithKey(key, value, rules)
}

func prefixedValidationErrors(prefix string, err error, fallback string) ValidationErrors {
	var nested ValidationErrors
	if errors.As(err, &nested) {
		result := make(ValidationErrors, len(nested))
		for i, item := range nested {
			separator := "."
			if strings.HasPrefix(item.Key, "[") || item.Key == "" {
				separator = ""
			}
			item.Key = prefix + separator + item.Key
			result[i] = item
		}
		return result
	}
	if rule, ok := ruleErrorFrom(err); ok {
		return ValidationErrors{{Key: prefix, Rule: rule.Rule, Param: rule.Param}}
	}
	return ValidationErrors{{Key: prefix, Rule: fallback}}
}

func validateValueRules(validate *validator.Validate, key string, value any, rules string, valueType ValueType) ValidationErrors {
	if list, ok := valueType.(listValue); ok {
		result := ValidationErrors{}
		items := reflect.ValueOf(value)
		for i := 0; i < items.Len(); i++ {
			result = append(result, validateValueRules(validate, fmt.Sprintf("%s[%d]", key, i), items.Index(i).Interface(), rules, list.item)...)
		}
		return result
	}
	err := safeValidate(validate, key, value, rules)
	if err == nil {
		return nil
	}
	var fields validator.ValidationErrors
	if !errors.As(err, &fields) {
		return ValidationErrors{{Key: key, Rule: "validation"}}
	}
	result := make(ValidationErrors, 0, len(fields))
	for _, failure := range fields {
		result = append(result, ValidationError{Key: key, Rule: failure.Tag(), Param: failure.Param()})
	}
	return result
}
