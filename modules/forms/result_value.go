package forms

import "sort"

// ResultFieldValue reconstructs a field using its historical cardinality,
// independently of the current form configuration or number of stored rows.
func ResultFieldValue(values []ResultValue) any {
	if len(values) == 0 {
		return nil
	}
	if !values[0].Multiple {
		return values[0].Value
	}
	ordered := append([]ResultValue(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })
	result := make([]any, len(ordered))
	for i, item := range ordered {
		result[i] = item.Value
	}
	return result
}
