package field

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/vernal96/go-cms-kernel/filesystem"
)

func StandardTypes() Types {
	types := Types{
		DescribedType{Type: repeaterType{}, Presentation: Metadata{Label: "Конструктор", Editor: "repeater"}},
		DescribedType{Type: stringType{code: TypeString}, Presentation: Metadata{Label: "Строка", Editor: "string"}},
		DescribedType{Type: integerType{}, Presentation: Metadata{Label: "Целое число", Editor: "int", Options: []ConfigField{{Key: "step", Label: "Шаг", Type: TypeInteger}}}},
		DescribedType{Type: floatType{}, Presentation: Metadata{Label: "Дробное число", Editor: "float", Options: []ConfigField{{Key: "step", Label: "Шаг", Type: TypeFloat}}}},
		DescribedType{Type: boolType{}, Presentation: Metadata{Label: "Флаг", Editor: "checkbox"}},
		DescribedType{Type: choiceType{code: TypeRadio}, Presentation: Metadata{Label: "Один вариант", Editor: "radio", Options: []ConfigField{{Key: "choices", Label: "Варианты", Type: TypeJSON, Editor: "core.choices", Required: true}}}},
		DescribedType{Type: choiceType{code: TypeSelect}, Presentation: Metadata{Label: "Список", Editor: "select", Options: []ConfigField{{Key: "choices", Label: "Варианты", Type: TypeJSON, Editor: "core.choices", Required: true}}}},
		DescribedType{Type: stringType{code: TypeTextarea}, Presentation: Metadata{Label: "Многострочный текст", Editor: "textarea"}},
		DescribedType{Type: stringType{code: TypeEmail, rules: []string{"email"}}, Presentation: Metadata{Label: "Email", Editor: "email"}},
		DescribedType{Type: phoneType{}, Presentation: Metadata{Label: "Телефон", Editor: "phone", Options: []ConfigField{{Key: "pattern", Label: "Шаблон", Type: TypeString}}}},
		DescribedType{Type: fileType{}, Presentation: Metadata{Label: "Файл из библиотеки", Editor: "file", Options: []ConfigField{{Key: "storages", Label: "Хранилища", Type: TypeJSON, Editor: "core.string-list"}, {Key: "mime_types", Label: "MIME-типы", Type: TypeJSON, Editor: "core.string-list"}}}},
		DescribedType{Type: mediaType{}, Presentation: Metadata{Label: "Медиа (изображение)", Editor: "media"}},
		DescribedType{Type: jsonType{}, Presentation: Metadata{Label: "JSON", Editor: "json"}},
	}
	for i, item := range types {
		switch item.Code() {
		case TypeString, TypeTextarea, TypeEmail, TypeInteger, TypeFloat, TypePhone, TypeFile, TypeMedia, TypeSelect:
			described := item.(DescribedType)
			described.Presentation.Options = append(described.Presentation.Options,
				ConfigField{Key: "multiple", Label: "Несколько значений", Type: TypeCheckbox},
				ConfigField{Key: "min_items", Label: "Минимум значений", Type: TypeInteger},
				ConfigField{Key: "max_items", Label: "Максимум значений (0 — без ограничения)", Type: TypeInteger})
			types[i] = described
		}
	}
	return types
}

// jsonType represents structured widget configuration. Its editor is chosen
// independently through Definition.Editor.
type jsonType struct{}

func (jsonType) Code() TypeCode { return TypeJSON }
func (jsonType) Compile(ctx CompileContext, options any) (ValueType, error) {
	if options != nil {
		return nil, errors.New("json field does not support options")
	}
	return jsonValue{}, nil
}

type jsonValue struct{}

func (jsonValue) StorageKind() StorageKind { return StorageJSON }
func (jsonValue) Multiple() bool           { return false }

func (jsonValue) Normalize(value any) (any, error) {
	switch typed := value.(type) {
	case []any, map[string]any:
		return cloneJSONValue(typed), nil
	default:
		return nil, fmt.Errorf("expected JSON object or array, got %T", value)
	}
}
func (jsonValue) Empty(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}
func (jsonValue) Validate(any) error { return nil }
func (jsonValue) Rules() []string    { return nil }
func (jsonValue) Example() any       { return []any{} }

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneJSONValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = cloneJSONValue(item)
		}
		return result
	default:
		return typed
	}
}

type fileType struct{}

func (fileType) Code() TypeCode { return TypeFile }

func (fileType) Compile(ctx CompileContext, options any) (ValueType, error) {
	config, err := FileOptionsValue(options)
	if err != nil {
		return nil, err
	}
	seenStorages := make(map[string]struct{}, len(config.Storages))
	for _, storage := range config.Storages {
		value := strings.TrimSpace(string(storage))
		if value == "" || value != string(storage) {
			return nil, errors.New("file storage is invalid")
		}
		if _, exists := seenStorages[value]; exists {
			return nil, errors.New("file storage is duplicated")
		}
		seenStorages[value] = struct{}{}
	}
	seenMIME := make(map[string]struct{}, len(config.MIMETypes))
	for _, mimeType := range config.MIMETypes {
		if !validMIMEPattern(mimeType) {
			return nil, fmt.Errorf("file MIME pattern %q is invalid", mimeType)
		}
		if _, exists := seenMIME[mimeType]; exists {
			return nil, errors.New("file MIME pattern is duplicated")
		}
		seenMIME[mimeType] = struct{}{}
	}
	return withList(fileValue{options: config}, config.Multiple, config.MinItems, config.MaxItems, false)
}

type fileValue struct{ options FileOptions }

func (fileValue) StorageKind() StorageKind { return StorageReference }
func (fileValue) Multiple() bool           { return false }

func (fileValue) Normalize(value any) (any, error) {
	result, ok := normalizeInteger(value)
	if !ok || result <= 0 {
		return nil, fmt.Errorf("expected positive file id, got %T", value)
	}
	return result, nil
}

func (fileValue) Empty(any) bool     { return false }
func (fileValue) Validate(any) error { return nil }
func (fileValue) Rules() []string    { return nil }
func (fileValue) Example() any       { return int64(1) }

func FileOptionsValue(value any) (FileOptions, error) {
	switch options := value.(type) {
	case nil:
		return FileOptions{}, nil
	case FileOptions:
		return options, nil
	case *FileOptions:
		if options != nil {
			return *options, nil
		}
	}
	return FileOptions{}, fmt.Errorf("got %T", value)
}

func FileMatches(options FileOptions, storage filesystem.Code, mimeType string) bool {
	if len(options.Storages) > 0 {
		matched := false
		for _, allowed := range options.Storages {
			if allowed == storage {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(options.MIMETypes) > 0 {
		for _, allowed := range options.MIMETypes {
			if allowed == mimeType || (strings.HasSuffix(allowed, "/*") && strings.HasPrefix(mimeType, strings.TrimSuffix(allowed, "*"))) {
				return true
			}
		}
		return false
	}
	return true
}

func validMIMEPattern(value string) bool {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	return parts[0] != "*" && !strings.ContainsAny(value, " \t\r\n") && (parts[1] == "*" || !strings.Contains(parts[1], "*"))
}

type stringType struct {
	code  TypeCode
	rules []string
}

func (t stringType) Code() TypeCode {
	return t.code
}

func (t stringType) Compile(ctx CompileContext, options any) (ValueType, error) {
	config, err := DecodeOptions[StringOptions](options)
	if err != nil {
		return nil, err
	}
	return withList(stringValue{rules: append([]string(nil), t.rules...)}, config.Multiple, config.MinItems, config.MaxItems, false)
}

type stringValue struct {
	rules []string
}

func (stringValue) StorageKind() StorageKind { return StorageString }
func (stringValue) Multiple() bool           { return false }

func (v stringValue) Normalize(value any) (any, error) {
	result, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("expected string, got %T", value)
	}

	return result, nil
}

func (stringValue) Empty(value any) bool {
	result, ok := value.(string)
	return ok && result == ""
}

func (stringValue) Validate(any) error {
	return nil
}

func (v stringValue) Rules() []string {
	return append([]string(nil), v.rules...)
}

func (stringValue) Example() any {
	return "example"
}

type integerType struct{}

func (integerType) Code() TypeCode {
	return TypeInteger
}

func (integerType) Compile(ctx CompileContext, options any) (ValueType, error) {
	config, err := integerOptions(options)
	if err != nil {
		return nil, err
	}
	if config.Step != nil && *config.Step <= 0 {
		return nil, errors.New("integer step must be greater than zero")
	}

	return withList(integerValue{}, config.Multiple, config.MinItems, config.MaxItems, false)
}

type integerValue struct{}

func (integerValue) StorageKind() StorageKind { return StorageInteger }
func (integerValue) Multiple() bool           { return false }

func (integerValue) Normalize(value any) (any, error) {
	result, ok := normalizeInteger(value)
	if !ok {
		return nil, fmt.Errorf("expected integer, got %T", value)
	}

	return result, nil
}

func (integerValue) Empty(any) bool {
	return false
}

func (integerValue) Validate(any) error {
	return nil
}

func (integerValue) Rules() []string {
	return nil
}

func (integerValue) Example() any {
	return int64(1)
}

type floatType struct{}

func (floatType) Code() TypeCode {
	return TypeFloat
}

func (floatType) Compile(ctx CompileContext, options any) (ValueType, error) {
	config, err := floatOptions(options)
	if err != nil {
		return nil, err
	}
	if config.Step != nil &&
		(*config.Step <= 0 || math.IsNaN(*config.Step) || math.IsInf(*config.Step, 0)) {
		return nil, errors.New("float step must be finite and greater than zero")
	}

	return withList(floatValue{}, config.Multiple, config.MinItems, config.MaxItems, false)
}

type floatValue struct{}

func (floatValue) StorageKind() StorageKind { return StorageFloat }
func (floatValue) Multiple() bool           { return false }

func (floatValue) Normalize(value any) (any, error) {
	result, ok := normalizeFloat(value)
	if !ok {
		return nil, fmt.Errorf("expected number, got %T", value)
	}

	return result, nil
}

func (floatValue) Empty(any) bool {
	return false
}

func (floatValue) Validate(any) error {
	return nil
}

func (floatValue) Rules() []string {
	return nil
}

func (floatValue) Example() any {
	return float64(1)
}

type boolType struct{}

func (boolType) Code() TypeCode {
	return TypeCheckbox
}

func (boolType) Compile(ctx CompileContext, options any) (ValueType, error) {
	if options != nil {
		return nil, errors.New("checkbox field does not support options")
	}

	return boolValue{}, nil
}

type boolValue struct{}

func (boolValue) StorageKind() StorageKind { return StorageBoolean }
func (boolValue) Multiple() bool           { return false }

func (boolValue) Normalize(value any) (any, error) {
	result, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("expected bool, got %T", value)
	}

	return result, nil
}

func (boolValue) Empty(any) bool {
	return false
}

func (boolValue) Validate(any) error {
	return nil
}

func (boolValue) Rules() []string {
	return nil
}

func (boolValue) Example() any {
	return false
}

type choiceType struct {
	code TypeCode
}

func (t choiceType) Code() TypeCode {
	return t.code
}

func (t choiceType) Compile(ctx CompileContext, options any) (ValueType, error) {
	var (
		choices            []Choice
		multiple           bool
		minItems, maxItems int
		err                error
	)

	switch t.code {
	case TypeRadio:
		config, configErr := radioOptions(options)
		if configErr != nil {
			err = configErr
		} else {
			choices = config.Choices
		}

	case TypeSelect:
		config, configErr := selectOptions(options)
		if configErr != nil {
			err = configErr
		} else {
			choices = config.Choices
			multiple = config.Multiple
			minItems, maxItems = config.MinItems, config.MaxItems
		}

	default:
		err = fmt.Errorf("unsupported choice type %q", t.code)
	}
	if err != nil {
		return nil, err
	}

	allowed, err := compileChoices(choices)
	if err != nil {
		return nil, err
	}

	return withList(choiceValue{allowed: allowed}, multiple, minItems, maxItems, true)
}

type choiceValue struct{ allowed map[string]struct{} }

func (choiceValue) StorageKind() StorageKind { return StorageString }
func (choiceValue) Multiple() bool           { return false }
func (choiceValue) Normalize(value any) (any, error) {
	result, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("expected string, got %T", value)
	}
	return result, nil
}
func (choiceValue) Empty(value any) bool { return value == "" }
func (v choiceValue) Validate(value any) error {
	if _, exists := v.allowed[value.(string)]; !exists {
		return RuleError{Rule: "oneof"}
	}
	return nil
}
func (choiceValue) Rules() []string { return nil }
func (choiceValue) Example() any    { return "example" }

type phoneType struct{}

func (phoneType) Code() TypeCode {
	return TypePhone
}

func (phoneType) Compile(ctx CompileContext, options any) (ValueType, error) {
	config, err := phoneOptions(options)
	if err != nil {
		return nil, err
	}

	result := phoneValue{}
	if config.Pattern == "" {
		result.rules = []string{"e164"}
		return withList(result, config.Multiple, config.MinItems, config.MaxItems, false)
	}

	pattern, err := regexp.Compile(config.Pattern)
	if err != nil {
		return nil, fmt.Errorf("compile phone pattern: %w", err)
	}
	result.pattern = pattern
	return withList(result, config.Multiple, config.MinItems, config.MaxItems, false)
}

type phoneValue struct {
	pattern *regexp.Regexp
	rules   []string
}

func (phoneValue) StorageKind() StorageKind { return StorageString }
func (phoneValue) Multiple() bool           { return false }

func (phoneValue) Normalize(value any) (any, error) {
	result, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("expected string, got %T", value)
	}

	return result, nil
}

func (phoneValue) Empty(value any) bool {
	result, ok := value.(string)
	return ok && result == ""
}

func (v phoneValue) Validate(value any) error {
	if v.pattern == nil {
		return nil
	}
	if !v.pattern.MatchString(value.(string)) {
		return RuleError{Rule: "pattern", Param: v.pattern.String()}
	}

	return nil
}

func (v phoneValue) Rules() []string {
	return append([]string(nil), v.rules...)
}

func (phoneValue) Example() any {
	return "+79991234567"
}

func integerOptions(options any) (IntegerOptions, error) {
	switch typed := options.(type) {
	case nil:
		return IntegerOptions{}, nil
	case IntegerOptions:
		return typed, nil
	case *IntegerOptions:
		if typed == nil {
			return IntegerOptions{}, errors.New("integer options are nil")
		}
		return *typed, nil
	default:
		return IntegerOptions{}, fmt.Errorf(
			"invalid integer options type %T",
			options,
		)
	}
}

func floatOptions(options any) (FloatOptions, error) {
	switch typed := options.(type) {
	case nil:
		return FloatOptions{}, nil
	case FloatOptions:
		return typed, nil
	case *FloatOptions:
		if typed == nil {
			return FloatOptions{}, errors.New("float options are nil")
		}
		return *typed, nil
	default:
		return FloatOptions{}, fmt.Errorf(
			"invalid float options type %T",
			options,
		)
	}
}

func radioOptions(options any) (RadioOptions, error) {
	switch typed := options.(type) {
	case RadioOptions:
		return typed, nil
	case *RadioOptions:
		if typed == nil {
			return RadioOptions{}, errors.New("radio options are nil")
		}
		return *typed, nil
	default:
		return RadioOptions{}, fmt.Errorf(
			"invalid radio options type %T",
			options,
		)
	}
}

func selectOptions(options any) (SelectOptions, error) {
	switch typed := options.(type) {
	case SelectOptions:
		return typed, nil
	case *SelectOptions:
		if typed == nil {
			return SelectOptions{}, errors.New("select options are nil")
		}
		return *typed, nil
	default:
		return SelectOptions{}, fmt.Errorf(
			"invalid select options type %T",
			options,
		)
	}
}

func phoneOptions(options any) (PhoneOptions, error) {
	switch typed := options.(type) {
	case nil:
		return PhoneOptions{}, nil
	case PhoneOptions:
		return typed, nil
	case *PhoneOptions:
		if typed == nil {
			return PhoneOptions{}, errors.New("phone options are nil")
		}
		return *typed, nil
	default:
		return PhoneOptions{}, fmt.Errorf(
			"invalid phone options type %T",
			options,
		)
	}
}

func compileChoices(choices []Choice) (map[string]struct{}, error) {
	if len(choices) == 0 {
		return nil, errors.New("choices are empty")
	}

	result := make(map[string]struct{}, len(choices))
	for index, choice := range choices {
		if choice.Value == "" || strings.TrimSpace(choice.Value) != choice.Value {
			return nil, fmt.Errorf(
				"choice at index %d has invalid value %q",
				index,
				choice.Value,
			)
		}
		if choice.Label == "" || strings.TrimSpace(choice.Label) != choice.Label {
			return nil, fmt.Errorf(
				"choice %q has invalid label %q",
				choice.Value,
				choice.Label,
			)
		}
		if _, exists := result[choice.Value]; exists {
			return nil, fmt.Errorf("duplicate choice value %q", choice.Value)
		}
		result[choice.Value] = struct{}{}
	}

	return result, nil
}

func normalizeInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case float32:
		return integralFloat(float64(typed))
	case float64:
		return integralFloat(typed)
	case json.Number:
		if result, err := typed.Int64(); err == nil {
			return result, true
		}
		result, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return integralFloat(result)
	default:
		return 0, false
	}
}

func integralFloat(value float64) (int64, bool) {
	const (
		minInt64Float     = -9223372036854775808.0
		maxInt64Exclusive = 9223372036854775808.0
	)

	if math.IsNaN(value) ||
		math.IsInf(value, 0) ||
		math.Trunc(value) != value ||
		value < minInt64Float ||
		value >= maxInt64Exclusive {
		return 0, false
	}

	return int64(value), true
}

func normalizeFloat(value any) (float64, bool) {
	var result float64

	switch typed := value.(type) {
	case int:
		result = float64(typed)
	case int8:
		result = float64(typed)
	case int16:
		result = float64(typed)
	case int32:
		result = float64(typed)
	case int64:
		result = float64(typed)
	case uint:
		result = float64(typed)
	case uint8:
		result = float64(typed)
	case uint16:
		result = float64(typed)
	case uint32:
		result = float64(typed)
	case uint64:
		result = float64(typed)
	case float32:
		result = float64(typed)
	case float64:
		result = typed
	case json.Number:
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err != nil {
			return 0, false
		}
		result = parsed
	default:
		return 0, false
	}

	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0, false
	}

	return result, true
}

func inputEmpty(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Array, reflect.Slice, reflect.String:
		return reflected.Len() == 0
	default:
		return false
	}
}
