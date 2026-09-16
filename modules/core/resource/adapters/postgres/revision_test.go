package postgres

import (
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func TestSnapshotFromResourceIncludesFieldsAndSemanticWidgets(t *testing.T) {
	item := resource.Resource{
		ID: 17, Version: 9, Title: "Article",
		Fields: map[string]any{"headline": "Stored"},
		Widgets: []widget.Binding{{
			ID: 99, Code: "content_summary", Area: widget.AreaBody, Position: 2,
			Presentation: widget.Presentation{View: "compact", Columns: 8, MarginTop: 1, MarginBottom: 2, Enabled: true},
			Params:       map[string]any{"title": "Summary"},
		}},
	}
	snapshot := snapshotFromResource(item)
	if snapshot.Fields["headline"] != "Stored" {
		t.Fatalf("snapshot fields = %#v", snapshot.Fields)
	}
	if len(snapshot.Widgets) != 1 || snapshot.Widgets[0].Code != "content_summary" || snapshot.Widgets[0].Params["title"] != "Summary" {
		t.Fatalf("snapshot widgets = %#v", snapshot.Widgets)
	}
	// Persistence binding IDs are intentionally not part of WidgetSnapshot.
	if snapshot.Widgets[0].Position != 2 || snapshot.Widgets[0].Columns != 8 {
		t.Fatalf("semantic widget state = %#v", snapshot.Widgets[0])
	}
}
