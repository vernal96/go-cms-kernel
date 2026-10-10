package field

// ValidateStored permits an explicitly removed required File selection to stay
// empty across reloads. Populated values and all other nested required fields
// retain their ordinary validation rules. Interactive Validate remains strict.
func (s *Schema) ValidateStored(values map[string]any) (map[string]any, error) {
	return s.withoutFileRequirements().ValidateIncomplete(values)
}

// StoredSchema retains the compiled contracts while allowing required File
// selections removed through the explicit permanent-delete action to be absent.
func (s *Schema) StoredSchema() *Schema { return s.withoutFileRequirements() }

func (s *Schema) withoutFileRequirements() *Schema {
	if s == nil {
		return s
	}
	result := &Schema{definitions: CloneDefinitions(s.definitions), fields: make(map[string]compiledField, len(s.fields))}
	for key, item := range s.fields {
		if item.definition.Type == TypeFile {
			item.required = false
		}
		item.valueType = storedValueType(item.valueType)
		result.fields[key] = item
	}
	return result
}

func storedValueType(value ValueType) ValueType {
	switch v := value.(type) {
	case repeaterValue:
		v.schema = v.schema.withoutFileRequirements()
		return v
	case listValue:
		v.item = storedValueType(v.item).(StorageValueType)
		return v
	default:
		return value
	}
}

// StoredReferences collects actual persisted selections after explicit removal.
func (s *Schema) StoredReferences(values map[string]any) ([]Reference, error) {
	return s.withoutFileRequirements().References(values)
}
