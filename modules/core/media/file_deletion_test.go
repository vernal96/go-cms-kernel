package media

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestPruneFileOccurrencePreservesRepeaterAndOrderedSelections(t *testing.T) {
	original := map[string]any{"portals": []any{map[string]any{"name": "first", "icons": []any{int64(7), int64(8), int64(9)}}, map[string]any{"name": "second", "icon": json.Number("10")}}}
	pruned, err := PruneFileOccurrence(original, []string{"portals", "0", "icons", "1"}, 8)
	if err != nil {
		t.Fatal(err)
	}
	rows := pruned.(map[string]any)["portals"].([]any)
	if len(rows) != 2 || !reflect.DeepEqual(rows[0].(map[string]any)["icons"], []any{int64(7), int64(9)}) {
		t.Fatalf("pruned=%v", pruned)
	}
	if len(original["portals"].([]any)[0].(map[string]any)["icons"].([]any)) != 3 {
		t.Fatal("published value mutated")
	}
	pruned, err = PruneFileOccurrence(pruned, []string{"portals", "1", "icon"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	row := pruned.(map[string]any)["portals"].([]any)[1].(map[string]any)
	if len(row) != 1 || row["name"] != "second" {
		t.Fatalf("repeater row removed: %v", row)
	}
}

func TestPruneFileOccurrenceRejectsStaleIdentityAndPath(t *testing.T) {
	for _, path := range [][]string{{"files", "9"}, {"files", "0"}, {"missing"}} {
		if _, err := PruneFileOccurrence(map[string]any{"files": []any{int64(1)}}, path, 2); !errors.Is(err, ErrFileDeleteConflict) {
			t.Fatalf("path=%v error=%v", path, err)
		}
	}
}
