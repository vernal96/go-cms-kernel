package validation

import "github.com/vernal96/go-cms-kernel/modules/core/field"

type NumberOptions struct {
	Value float64 `json:"value"`
}
type CountOptions struct {
	Value int `json:"value"`
}
type RangeOptions struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}
type CountRangeOptions struct {
	Min int `json:"min"`
	Max int `json:"max"`
}
type TextOptions struct {
	Value string `json:"value"`
}
type ValuesOptions struct {
	Values []any `json:"values"`
}

func Min(value float64) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "min", Options: NumberOptions{value}}
}
func Max(value float64) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "max", Options: NumberOptions{value}}
}
func MultipleOf(value float64) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "multiple_of", Options: NumberOptions{value}}
}
func Digits(value int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "digits", Options: CountOptions{value}}
}
func MinDigits(value int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "min_digits", Options: CountOptions{value}}
}
func MaxDigits(value int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "max_digits", Options: CountOptions{value}}
}
func MinLength(value int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "min_length", Options: CountOptions{value}}
}
func MaxLength(value int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "max_length", Options: CountOptions{value}}
}
func Length(value int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "length", Options: CountOptions{value}}
}
func MinItems(value int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "min_items", Options: CountOptions{value}}
}
func MaxItems(value int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "max_items", Options: CountOptions{value}}
}
func ItemsCount(value int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "items_count", Options: CountOptions{value}}
}
func Between(min, max float64) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "between", Options: RangeOptions{min, max}}
}
func DigitsBetween(min, max int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "digits_between", Options: CountRangeOptions{min, max}}
}
func LengthBetween(min, max int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "length_between", Options: CountRangeOptions{min, max}}
}
func ItemsBetween(min, max int) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "items_between", Options: CountRangeOptions{min, max}}
}
func StartsWith(value string) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "starts_with", Options: TextOptions{value}}
}
func EndsWith(value string) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "ends_with", Options: TextOptions{value}}
}
func DoesntStartWith(value string) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "doesnt_start_with", Options: TextOptions{value}}
}
func DoesntEndWith(value string) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "doesnt_end_with", Options: TextOptions{value}}
}
func Regex(value string) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "regex", Options: TextOptions{value}}
}
func NotRegex(value string) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "not_regex", Options: TextOptions{value}}
}
func In(values ...any) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "in", Options: ValuesOptions{append([]any(nil), values...)}}
}
func NotIn(values ...any) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "not_in", Options: ValuesOptions{append([]any(nil), values...)}}
}
func Contains(values ...any) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "contains", Options: ValuesOptions{append([]any(nil), values...)}}
}
func DoesntContain(values ...any) field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "doesnt_contain", Options: ValuesOptions{append([]any(nil), values...)}}
}
func Alpha() field.ValidatorDefinition     { return field.ValidatorDefinition{Type: "alpha"} }
func AlphaDash() field.ValidatorDefinition { return field.ValidatorDefinition{Type: "alpha_dash"} }
func AlphaNumeric() field.ValidatorDefinition {
	return field.ValidatorDefinition{Type: "alpha_numeric"}
}
func ASCII() field.ValidatorDefinition       { return field.ValidatorDefinition{Type: "ascii"} }
func Lowercase() field.ValidatorDefinition   { return field.ValidatorDefinition{Type: "lowercase"} }
func Uppercase() field.ValidatorDefinition   { return field.ValidatorDefinition{Type: "uppercase"} }
func UniqueItems() field.ValidatorDefinition { return field.ValidatorDefinition{Type: "unique_items"} }
func URL() field.ValidatorDefinition         { return field.ValidatorDefinition{Type: "url"} }
func IP() field.ValidatorDefinition          { return field.ValidatorDefinition{Type: "ip"} }
func IPv4() field.ValidatorDefinition        { return field.ValidatorDefinition{Type: "ipv4"} }
func IPv6() field.ValidatorDefinition        { return field.ValidatorDefinition{Type: "ipv6"} }
func MAC() field.ValidatorDefinition         { return field.ValidatorDefinition{Type: "mac"} }
func UUID() field.ValidatorDefinition        { return field.ValidatorDefinition{Type: "uuid"} }
func ULID() field.ValidatorDefinition        { return field.ValidatorDefinition{Type: "ulid"} }
func HexColor() field.ValidatorDefinition    { return field.ValidatorDefinition{Type: "hex_color"} }
func Accepted() field.ValidatorDefinition    { return field.ValidatorDefinition{Type: "accepted"} }
func Declined() field.ValidatorDefinition    { return field.ValidatorDefinition{Type: "declined"} }
