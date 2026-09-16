package field

import (
	"strings"
	"testing"
)

func TestValidateEditorTabs(t *testing.T) {
	definitions := []Definition{
		{Key: "title"},
		{Key: "color"},
	}
	testCases := []struct {
		name  string
		tabs  []EditorTab
		match string
	}{
		{name: "absent"},
		{name: "valid", tabs: []EditorTab{
			{Code: "content", Label: "Content", Fields: []string{"title"}},
			{Code: "appearance", Label: "Appearance", Fields: []string{"color"}},
		}},
		{name: "invalid metadata", tabs: []EditorTab{{Code: " main", Label: "Main", Fields: []string{"title", "color"}}}, match: "is invalid"},
		{name: "duplicate code", tabs: []EditorTab{
			{Code: "main", Label: "Main", Fields: []string{"title"}},
			{Code: "main", Label: "Other", Fields: []string{"color"}},
		}, match: "duplicate editor tab code"},
		{name: "unknown field", tabs: []EditorTab{{Code: "main", Label: "Main", Fields: []string{"title", "missing"}}}, match: "unknown field"},
		{name: "duplicate field", tabs: []EditorTab{
			{Code: "main", Label: "Main", Fields: []string{"title", "color"}},
			{Code: "other", Label: "Other", Fields: []string{"title"}},
		}, match: "assigned to editor tabs"},
		{name: "unassigned field", tabs: []EditorTab{{Code: "main", Label: "Main", Fields: []string{"title"}}}, match: "not assigned"},
	}

	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateEditorTabs(definitions, test.tabs)
			if test.match == "" && err != nil {
				t.Fatalf("error = %v", err)
			}
			if test.match != "" && (err == nil || !strings.Contains(err.Error(), test.match)) {
				t.Fatalf("error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestCloneEditorTabsDoesNotShareFields(t *testing.T) {
	source := []EditorTab{{Code: "main", Label: "Main", Fields: []string{"title"}}}
	cloned := CloneEditorTabs(source)
	source[0].Fields[0] = "changed"
	if cloned[0].Fields[0] != "title" {
		t.Fatalf("cloned tabs = %#v", cloned)
	}
}
