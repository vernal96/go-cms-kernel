package field

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

func DecodeRequiredOptions(value any, target any, keys ...string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("validator options must be an object")
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return fmt.Errorf("option %q is required", key)
		}
	}
	decoded, err := DecodeOptions[map[string]any](value)
	if err != nil {
		return err
	}
	if len(decoded) != len(keys) {
		return errors.New("validator options contain unknown fields")
	}
	return json.Unmarshal(raw, target)
}

type builtinValidatorType struct {
	code  ValidatorCode
	label string
	kind  string
}
type builtinValidator struct {
	code   ValidatorCode
	params map[string]any
	test   func(any) bool
}

func (v builtinValidator) Validate(value any) error {
	if v.test(value) {
		return nil
	}
	return validatorFailure(v.code, v.params)
}
func (t builtinValidatorType) Code() ValidatorCode { return t.code }
func (t builtinValidatorType) ValidatorMetadata() ValidatorMetadata {
	m := ValidatorMetadata{Code: t.code, Label: t.label, Options: []ConfigField{}}
	switch t.kind {
	case "number":
		m.Options = []ConfigField{{Key: "value", Label: "Значение", Type: TypeFloat, Required: true}}
	case "count":
		m.Options = []ConfigField{{Key: "value", Label: "Количество", Type: TypeInteger, Required: true}}
	case "range_number":
		m.Options = []ConfigField{{Key: "min", Label: "Минимум", Type: TypeFloat, Required: true}, {Key: "max", Label: "Максимум", Type: TypeFloat, Required: true}}
	case "range_count":
		m.Options = []ConfigField{{Key: "min", Label: "Минимум", Type: TypeInteger, Required: true}, {Key: "max", Label: "Максимум", Type: TypeInteger, Required: true}}
	case "values":
		m.Options = []ConfigField{{Key: "values", Label: "Значения", Type: TypeJSON, Required: true}}
	case "text":
		m.Options = []ConfigField{{Key: "value", Label: "Текст", Type: TypeString, Required: true}}
	}
	stringsOnly := []TypeCode{TypeString, TypeTextarea, TypeEmail, TypePhone, TypeRadio, TypeSelect}
	lists := []TypeCode{TypeString, TypeTextarea, TypeEmail, TypePhone, TypeInteger, TypeFloat, TypeFile, TypeMedia, TypeSelect, TypeRepeater}
	switch t.code {
	case "min", "max", "between", "multiple_of":
		m.FieldTypes = []TypeCode{TypeInteger, TypeFloat}
	case "digits", "min_digits", "max_digits", "digits_between":
		m.FieldTypes = []TypeCode{TypeInteger}
	case "min_length", "max_length", "length", "length_between", "alpha", "alpha_dash", "alpha_numeric", "ascii", "lowercase", "uppercase", "starts_with", "ends_with", "doesnt_start_with", "doesnt_end_with", "regex", "not_regex", "url", "ip", "ipv4", "ipv6", "mac", "uuid", "ulid", "hex_color":
		m.FieldTypes = stringsOnly
	case "min_items", "max_items", "items_between", "items_count", "unique_items":
		b := true
		m.Multiple = &b
		m.FieldTypes = lists
	case "in", "not_in":
		m.FieldTypes = []TypeCode{TypeInteger, TypeFloat, TypeCheckbox, TypeString, TypeTextarea, TypeEmail, TypePhone, TypeRadio, TypeSelect}
	case "contains", "doesnt_contain":
		scalar, list := false, true
		m.Applicability = []ValidatorApplicability{{FieldTypes: stringsOnly, Multiple: &scalar}, {FieldTypes: []TypeCode{TypeString, TypeTextarea, TypeEmail, TypePhone, TypeInteger, TypeFloat, TypeSelect}, Multiple: &list}}
	case "accepted", "declined":
		m.FieldTypes = []TypeCode{TypeCheckbox}
	}
	return m
}

var builtinSpecs = []builtinValidatorType{
	{"min", "Минимум", "number"}, {"max", "Максимум", "number"}, {"between", "Между", "range_number"}, {"multiple_of", "Кратно", "number"},
	{"digits", "Цифр", "count"}, {"min_digits", "Минимум цифр", "count"}, {"max_digits", "Максимум цифр", "count"}, {"digits_between", "Диапазон цифр", "range_count"},
	{"min_length", "Минимум символов", "count"}, {"max_length", "Максимум символов", "count"}, {"length", "Длина", "count"}, {"length_between", "Диапазон длины", "range_count"},
	{"alpha", "Буквы", "none"}, {"alpha_dash", "Буквы, цифры, дефис", "none"}, {"alpha_numeric", "Буквы и цифры", "none"}, {"ascii", "ASCII", "none"},
	{"lowercase", "Нижний регистр", "none"}, {"uppercase", "Верхний регистр", "none"},
	{"starts_with", "Начинается с", "text"}, {"ends_with", "Заканчивается на", "text"}, {"doesnt_start_with", "Не начинается с", "text"}, {"doesnt_end_with", "Не заканчивается на", "text"},
	{"contains", "Содержит", "values"}, {"doesnt_contain", "Не содержит", "values"},
	{"regex", "Регулярное выражение", "text"}, {"not_regex", "Не соответствует выражению", "text"},
	{"in", "В списке", "values"}, {"not_in", "Вне списка", "values"},
	{"min_items", "Минимум элементов", "count"}, {"max_items", "Максимум элементов", "count"}, {"items_between", "Диапазон элементов", "range_count"}, {"items_count", "Число элементов", "count"}, {"unique_items", "Уникальные элементы", "none"},
	{"url", "URL", "none"}, {"ip", "IP адрес", "none"}, {"ipv4", "IPv4", "none"}, {"ipv6", "IPv6", "none"}, {"mac", "MAC адрес", "none"}, {"uuid", "UUID", "none"}, {"ulid", "ULID", "none"}, {"hex_color", "HEX цвет", "none"},
	{"accepted", "Принято", "none"}, {"declined", "Отклонено", "none"},
}

func StandardValidatorTypes() ValidatorTypes {
	out := make(ValidatorTypes, len(builtinSpecs))
	for i, item := range builtinSpecs {
		out[i] = item
	}
	return out
}
func (types Types) ValidatorType(code ValidatorCode) (ValidatorType, bool) {
	return StandardValidatorTypes().ValidatorType(code)
}
func (types Types) ValidatorTypes() []ValidatorCode { return StandardValidatorTypes().ValidatorTypes() }

func (t builtinValidatorType) Compile(ctx ValidatorContext, options any) (Validator, error) {
	code := t.code
	isList := ctx.Multiple || ctx.FieldType == TypeRepeater
	storage := ctx.Storage
	isNumber := storage == StorageInteger || storage == StorageFloat
	isString := storage == StorageString
	numeric := code == "min" || code == "max" || code == "between" || code == "multiple_of" || strings.Contains(string(code), "digits")
	digits := strings.Contains(string(code), "digits")
	length := code == "min_length" || code == "max_length" || code == "length" || code == "length_between"
	items := code == "min_items" || code == "max_items" || code == "items_between" || code == "items_count" || code == "unique_items"
	boolRule := code == "accepted" || code == "declined"
	membership := code == "in" || code == "not_in"
	contains := code == "contains" || code == "doesnt_contain"
	if (numeric && !isNumber) || (digits && storage != StorageInteger) || (length && !isString) || (items && !isList) || (boolRule && storage != StorageBoolean) || (membership && !(isNumber || isString || storage == StorageBoolean)) || (!numeric && !length && !items && !boolRule && !membership && !contains && !isString) {
		return nil, fmt.Errorf("validator %q is incompatible with field %q", code, ctx.FieldType)
	}
	if contains && !isString && !isList {
		return nil, fmt.Errorf("validator %q requires string or list", code)
	}
	params := map[string]any{}
	test := func(any) bool { return true }
	switch t.kind {
	case "number", "count":
		var o struct {
			Value float64 `json:"value"`
		}
		if err := DecodeRequiredOptions(options, &o, "value"); err != nil {
			return nil, err
		}
		n := o.Value
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, errors.New("value must be finite")
		}
		if t.kind == "count" && (n < 0 || math.Trunc(n) != n) {
			return nil, errors.New("count must be a nonnegative integer")
		}
		if code == "multiple_of" && n <= 0 {
			return nil, errors.New("multiple_of must be positive")
		}
		params["value"] = n
		switch code {
		case "min":
			test = func(v any) bool { return toFloat(v) >= n }
		case "max":
			test = func(v any) bool { return toFloat(v) <= n }
		case "multiple_of":
			test = func(v any) bool { q := toFloat(v) / n; return math.Abs(q-math.Round(q)) < 1e-9 }
		case "digits", "min_digits", "max_digits":
			test = func(v any) bool {
				d := digitCount(v)
				switch code {
				case "digits":
					return float64(d) == n
				case "min_digits":
					return float64(d) >= n
				default:
					return float64(d) <= n
				}
			}
		case "min_length":
			test = func(v any) bool { return float64(utf8.RuneCountInString(v.(string))) >= n }
		case "max_length":
			test = func(v any) bool { return float64(utf8.RuneCountInString(v.(string))) <= n }
		case "length":
			test = func(v any) bool { return float64(utf8.RuneCountInString(v.(string))) == n }
		case "min_items":
			test = func(v any) bool { return float64(reflect.ValueOf(v).Len()) >= n }
		case "max_items":
			test = func(v any) bool { return float64(reflect.ValueOf(v).Len()) <= n }
		case "items_count":
			test = func(v any) bool { return float64(reflect.ValueOf(v).Len()) == n }
		}
	case "range_number", "range_count":
		var o struct {
			Min float64 `json:"min"`
			Max float64 `json:"max"`
		}
		if err := DecodeRequiredOptions(options, &o, "min", "max"); err != nil {
			return nil, err
		}
		if math.IsNaN(o.Min) || math.IsNaN(o.Max) || math.IsInf(o.Min, 0) || math.IsInf(o.Max, 0) || o.Min > o.Max {
			return nil, errors.New("invalid range")
		}
		if t.kind == "range_count" && (o.Min < 0 || math.Trunc(o.Min) != o.Min || math.Trunc(o.Max) != o.Max) {
			return nil, errors.New("range counts must be nonnegative integers")
		}
		params["min"], params["max"] = o.Min, o.Max
		switch code {
		case "between":
			test = func(v any) bool { x := toFloat(v); return x >= o.Min && x <= o.Max }
		case "digits_between":
			test = func(v any) bool { x := float64(digitCount(v)); return x >= o.Min && x <= o.Max }
		case "length_between":
			test = func(v any) bool { x := float64(utf8.RuneCountInString(v.(string))); return x >= o.Min && x <= o.Max }
		case "items_between":
			test = func(v any) bool { x := float64(reflect.ValueOf(v).Len()); return x >= o.Min && x <= o.Max }
		}
	case "text":
		var o struct {
			Value string `json:"value"`
		}
		if err := DecodeRequiredOptions(options, &o, "value"); err != nil {
			return nil, err
		}
		params["value"] = o.Value
		switch code {
		case "starts_with":
			test = func(v any) bool { return strings.HasPrefix(v.(string), o.Value) }
		case "ends_with":
			test = func(v any) bool { return strings.HasSuffix(v.(string), o.Value) }
		case "doesnt_start_with":
			test = func(v any) bool { return !strings.HasPrefix(v.(string), o.Value) }
		case "doesnt_end_with":
			test = func(v any) bool { return !strings.HasSuffix(v.(string), o.Value) }
		case "regex", "not_regex":
			pattern, err := regexp.Compile(o.Value)
			if err != nil {
				return nil, err
			}
			test = func(v any) bool { matched := pattern.MatchString(v.(string)); return matched == (code == "regex") }
		}
	case "values":
		var o struct {
			Values []any `json:"values"`
		}
		if err := DecodeRequiredOptions(options, &o, "values"); err != nil {
			return nil, err
		}
		if len(o.Values) == 0 {
			return nil, errors.New("values cannot be empty")
		}
		values := make([]any, len(o.Values))
		for i, v := range o.Values {
			normalized, err := normalizeMember(v, storage)
			if err != nil {
				return nil, fmt.Errorf("value at index %d: %w", i, err)
			}
			values[i] = normalized
		}
		params["values"] = values
		if membership {
			test = func(v any) bool { found := memberOf(v, values); return found == (code == "in") }
		}
		if contains {
			if isList {
				test = func(v any) bool {
					items := reflect.ValueOf(v)
					found := false
					for i := 0; i < items.Len(); i++ {
						if memberOf(items.Index(i).Interface(), values) {
							found = true
							break
						}
					}
					return found == (code == "contains")
				}
			}
			if isString && !isList {
				test = func(v any) bool {
					found := false
					for _, item := range values {
						if strings.Contains(v.(string), item.(string)) {
							found = true
							break
						}
					}
					return found == (code == "contains")
				}
			}
		}
	case "none":
		if options != nil {
			raw, err := json.Marshal(options)
			if err != nil {
				return nil, err
			}
			if string(raw) != "{}" && string(raw) != "null" {
				return nil, errors.New("validator does not accept options")
			}
		}
		switch code {
		case "unique_items":
			test = func(v any) bool {
				items := reflect.ValueOf(v)
				for i := 0; i < items.Len(); i++ {
					for j := i + 1; j < items.Len(); j++ {
						if reflect.DeepEqual(items.Index(i).Interface(), items.Index(j).Interface()) {
							return false
						}
					}
				}
				return true
			}
		case "accepted":
			test = func(v any) bool { return v == true }
		case "declined":
			test = func(v any) bool { return v == false }
		default:
			test = stringTest(code)
		}
	}
	return builtinValidator{code: code, params: params, test: test}, nil
}
func toFloat(v any) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case float64:
		return x
	default:
		return math.NaN()
	}
}
func digitCount(v any) int { s := fmt.Sprint(v); s = strings.TrimPrefix(s, "-"); return len(s) }
func normalizeMember(v any, kind StorageKind) (any, error) {
	switch kind {
	case StorageString:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("expected string")
		}
		return s, nil
	case StorageInteger:
		i, ok := normalizeInteger(v)
		if !ok {
			return nil, errors.New("expected integer")
		}
		return i, nil
	case StorageFloat:
		f, ok := normalizeFloat(v)
		if !ok {
			return nil, errors.New("expected number")
		}
		return f, nil
	case StorageBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("expected boolean")
		}
		return b, nil
	}
	return nil, errors.New("unsupported member type")
}
func memberOf(v any, values []any) bool {
	for _, item := range values {
		if reflect.DeepEqual(v, item) {
			return true
		}
	}
	return false
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var ulidPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{3}(?:[0-9a-fA-F]{3})?(?:[0-9a-fA-F]{2})?$`)

func stringTest(code ValidatorCode) func(any) bool {
	return func(v any) bool {
		s := v.(string)
		switch code {
		case "alpha":
			return allRunes(s, unicode.IsLetter)
		case "alpha_dash":
			return allRunes(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' })
		case "alpha_numeric":
			return allRunes(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
		case "ascii":
			for _, r := range s {
				if r > 127 {
					return false
				}
			}
			return true
		case "lowercase":
			return s == strings.ToLower(s)
		case "uppercase":
			return s == strings.ToUpper(s)
		case "url":
			u, e := url.ParseRequestURI(s)
			return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
		case "ip":
			return net.ParseIP(s) != nil
		case "ipv4":
			return net.ParseIP(s) != nil && net.ParseIP(s).To4() != nil
		case "ipv6":
			return net.ParseIP(s) != nil && net.ParseIP(s).To4() == nil
		case "mac":
			_, e := net.ParseMAC(s)
			return e == nil
		case "uuid":
			return uuidPattern.MatchString(s)
		case "ulid":
			return ulidPattern.MatchString(strings.ToUpper(s))
		case "hex_color":
			return colorPattern.MatchString(s)
		}
		return false
	}
}
func allRunes(s string, p func(rune) bool) bool {
	for _, r := range s {
		if !p(r) {
			return false
		}
	}
	return true
}
