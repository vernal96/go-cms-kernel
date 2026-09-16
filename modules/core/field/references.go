package field

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const ReferenceFile = "file"

// Reference describes an entity contained in a normalized value. Path contains
// JSON object keys/array indices relative to that value; an empty path addresses
// a scalar. Key is the logical field path when collected by a Schema.
type Reference struct {
	Key     string      `json:"key,omitempty"`
	Path    []string    `json:"path,omitempty"`
	Target  string      `json:"target"`
	ID      int64       `json:"id"`
	Options FileOptions `json:"options,omitempty"`
}

// ReferenceCollector lets scalar, composite and contributed value types expose
// references without requiring consumers to know their semantic type codes.
type ReferenceCollector interface {
	References(value any) ([]Reference, error)
}

func (v fileValue) References(value any) ([]Reference, error) {
	normalized, err := v.Normalize(value)
	if err != nil {
		return nil, err
	}
	return []Reference{{Target: ReferenceFile, ID: normalized.(int64), Options: cloneOptions(v.options).(FileOptions)}}, nil
}
func (v mediaValue) References(value any) ([]Reference, error) {
	normalized, err := v.Normalize(value)
	if err != nil {
		return nil, err
	}
	return []Reference{{Target: ReferenceMedia, ID: normalized.(int64)}}, nil
}
func (s *Schema) References(values map[string]any) ([]Reference, error) {
	if s == nil {
		return nil, errors.New("field schema is nil")
	}
	result := []Reference{}
	for _, def := range s.definitions {
		value, exists := values[def.Key]
		if !exists || inputEmpty(value) {
			continue
		}
		collector, ok := s.fields[def.Key].valueType.(ReferenceCollector)
		if !ok {
			continue
		}
		refs, err := collector.References(value)
		if err != nil {
			return nil, fmt.Errorf("field %q references: %w", def.Key, err)
		}
		for _, ref := range refs {
			ref.Path = append([]string{def.Key}, ref.Path...)
			ref.Key = ReferenceKey(ref.Path)
			result = append(result, ref)
		}
	}
	return result, nil
}

func ReferenceKey(path []string) string {
	var result strings.Builder
	for i, part := range path {
		if _, err := strconv.Atoi(part); i > 0 && err == nil {
			result.WriteString("[" + part + "]")
		} else {
			if i > 0 {
				result.WriteByte('.')
			}
			result.WriteString(part)
		}
	}
	return result.String()
}

// MediaReferences includes scalar storage references and references inside a
// structured value. ReferenceTarget still describes scalar storage semantics.
func (v StoredValue) MediaReferences() ([]Reference, error) {
	if v.ReferenceTarget != "" {
		id, ok := v.Value.(int64)
		if v.ReferenceTarget != ReferenceMedia || v.Kind != StorageReference || !ok || id <= 0 {
			return nil, fmt.Errorf("invalid reference field %q", v.Key)
		}
		return []Reference{{Target: ReferenceMedia, ID: id}}, nil
	}
	refs := []Reference{}
	for _, ref := range v.References {
		if ref.Target != ReferenceMedia {
			continue
		}
		if ref.ID <= 0 || len(ref.Path) == 0 || v.Kind != StorageJSON {
			return nil, fmt.Errorf("invalid structured reference field %q", v.Key)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}
