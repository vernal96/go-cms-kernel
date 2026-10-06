package resource

import (
	"context"
	"fmt"
)

func validateContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s context is nil", operation)
	}
	return ctx.Err()
}

func boolDefault(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

func resourceTypeID(value *ID) *int64 {
	if value == nil {
		return nil
	}
	result := int64(*value)
	return &result
}

func resourceID(value *int64) *ID {
	if value == nil {
		return nil
	}
	result := ID(*value)
	return &result
}

func validLookupPath(path string) bool {
	_, err := NormalizeLookupPath(path)
	return err == nil
}

func equalStrings(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
