package field

import (
	"fmt"
	"strings"
)

// EditorTab groups fields for an admin editor without changing their
// persistence or validation semantics.
type EditorTab struct {
	Code   string
	Label  string
	Fields []string
}

// ValidateEditorTabs validates optional, exhaustive field grouping metadata.
// When tabs are declared, every field must belong to exactly one tab.
func ValidateEditorTabs(definitions []Definition, tabs []EditorTab) error {
	fields := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		fields[definition.Key] = struct{}{}
	}

	seenTabs := make(map[string]struct{}, len(tabs))
	assigned := make(map[string]string, len(definitions))
	for index, tab := range tabs {
		if tab.Code == "" || strings.TrimSpace(tab.Code) != tab.Code ||
			tab.Label == "" || strings.TrimSpace(tab.Label) != tab.Label {
			return fmt.Errorf("editor tab at index %d is invalid", index)
		}
		if _, exists := seenTabs[tab.Code]; exists {
			return fmt.Errorf("duplicate editor tab code %q", tab.Code)
		}
		seenTabs[tab.Code] = struct{}{}

		for _, fieldCode := range tab.Fields {
			if _, exists := fields[fieldCode]; !exists {
				return fmt.Errorf("editor tab %q references unknown field %q", tab.Code, fieldCode)
			}
			if previous, exists := assigned[fieldCode]; exists {
				return fmt.Errorf("field %q is assigned to editor tabs %q and %q", fieldCode, previous, tab.Code)
			}
			assigned[fieldCode] = tab.Code
		}
	}

	if len(tabs) > 0 {
		for _, definition := range definitions {
			if _, exists := assigned[definition.Key]; !exists {
				return fmt.Errorf("field %q is not assigned to an editor tab", definition.Key)
			}
		}
	}

	return nil
}

func CloneEditorTabs(source []EditorTab) []EditorTab {
	if source == nil {
		return nil
	}
	result := make([]EditorTab, len(source))
	for index, tab := range source {
		result[index] = tab
		result[index].Fields = append([]string(nil), tab.Fields...)
	}
	return result
}
