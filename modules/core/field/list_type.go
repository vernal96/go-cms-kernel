package field

import (
	"fmt"
	"reflect"
	"strconv"
)

// listValue reuses the scalar type for normalization, validation and references.
// Schema applies configured rules to each scalar, not to the list length.
type listValue struct {
	item     StorageValueType
	min, max int
	unique   bool
}

func withList(item StorageValueType, multiple bool, min, max int, unique bool) (ValueType, error) {
	if min < 0 || max < 0 || (max > 0 && min > max) || (!multiple && (min != 0 || max != 0)) {
		return nil, fmt.Errorf("invalid list bounds: multiple=%t min_items=%d max_items=%d", multiple, min, max)
	}
	if !multiple {
		return item, nil
	}
	return listValue{item: item, min: min, max: max, unique: unique}, nil
}

func (v listValue) StorageKind() StorageKind { return v.item.StorageKind() }
func (listValue) Multiple() bool             { return true }
func (v listValue) Rules() []string          { return v.item.Rules() }
func (v listValue) Example() any             { return []any{v.item.Example()} }
func (listValue) Empty(value any) bool       { return reflect.ValueOf(value).Len() == 0 }
func (listValue) Validate(any) error         { return nil }

func (v listValue) Normalize(value any) (any, error) {
	items := reflect.ValueOf(value)
	if !items.IsValid() || (items.Kind() != reflect.Slice && items.Kind() != reflect.Array) {
		return nil, fmt.Errorf("expected array, got %T", value)
	}
	if items.Len() < v.min {
		return nil, RuleError{Rule: "min_items", Param: strconv.Itoa(v.min)}
	}
	if v.max > 0 && items.Len() > v.max {
		return nil, RuleError{Rule: "max_items", Param: strconv.Itoa(v.max)}
	}
	result := make([]any, items.Len())
	failures := ValidationErrors{}
	seen := map[string]bool{}
	for i := range result {
		key := fmt.Sprintf("[%d]", i)
		item := items.Index(i).Interface()
		if inputEmpty(item) {
			failures = append(failures, ValidationError{Key: key, Rule: "required"})
			continue
		}
		normalized, err := v.item.Normalize(item)
		if err != nil {
			failures = append(failures, prefixedValidationErrors(key, err, "type")...)
			continue
		}
		if v.item.Empty(normalized) {
			failures = append(failures, ValidationError{Key: key, Rule: "required"})
			continue
		}
		if err := v.item.Validate(normalized); err != nil {
			failures = append(failures, prefixedValidationErrors(key, err, "value")...)
			continue
		}
		if v.unique {
			text := normalized.(string)
			if seen[text] {
				failures = append(failures, ValidationError{Key: key, Rule: "unique"})
				continue
			}
			seen[text] = true
		}
		result[i] = normalized
	}
	if len(failures) != 0 {
		return nil, failures
	}
	if v.StorageKind() == StorageString {
		strings := make([]string, len(result))
		for i, item := range result {
			strings[i] = item.(string)
		}
		return strings, nil
	}
	return result, nil
}

func (v listValue) ReferenceTarget() string {
	if reference, ok := v.item.(ReferenceValueType); ok {
		return reference.ReferenceTarget()
	}
	return ""
}

func (v listValue) References(value any) ([]Reference, error) {
	collector, ok := v.item.(ReferenceCollector)
	if !ok {
		return nil, nil
	}
	items := reflect.ValueOf(value)
	refs := []Reference{}
	for i := 0; i < items.Len(); i++ {
		nested, err := collector.References(items.Index(i).Interface())
		if err != nil {
			return nil, err
		}
		for _, ref := range nested {
			ref.Path = append([]string{strconv.Itoa(i)}, ref.Path...)
			refs = append(refs, ref)
		}
	}
	return refs, nil
}
