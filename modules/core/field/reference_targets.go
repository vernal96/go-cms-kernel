package field

import "fmt"

// ValidateReferenceTargets rejects reference shapes whose lifecycle a consumer
// cannot support, including references hidden inside lists and repeaters.
func (s *Schema) ValidateReferenceTargets(allowed ...string) error {
	for _, def := range s.definitions {
		if err := validateReferenceTargets(s.fields[def.Key].valueType, allowed); err != nil {
			return fmt.Errorf("field %q: %w", def.Key, err)
		}
	}
	return nil
}
func validateReferenceTargets(value ValueType, allowed []string) error {
	switch v := value.(type) {
	case listValue:
		return validateReferenceTargets(v.item, allowed)
	case repeaterValue:
		return v.schema.ValidateReferenceTargets(allowed...)
	}
	target := ""
	if _, ok := value.(fileValue); ok {
		target = ReferenceFile
	} else if ref, ok := value.(ReferenceValueType); ok {
		target = ref.ReferenceTarget()
	} else if _, ok := value.(ReferenceCollector); ok {
		return fmt.Errorf("opaque reference collector is unsupported in this schema")
	} else if storage, ok := value.(StorageValueType); ok && storage.StorageKind() == StorageReference {
		return fmt.Errorf("reference target is unspecified")
	}
	if target == "" {
		return nil
	}
	for _, item := range allowed {
		if target == item {
			return nil
		}
	}
	return fmt.Errorf("reference target %q is unsupported in this schema", target)
}
