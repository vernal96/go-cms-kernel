package core

import (
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
)

func TestWidgetResourceSnapshotProjectsClonedResourceData(t *testing.T) {
	publishedAt := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	imageID := media.ID(23)
	path := "/news/featured"
	item := resource.Resource{
		ID: 42, Title: "Featured", Content: "Body", Path: &path,
		PublishedAt: &publishedAt, ImageMediaID: &imageID,
		Fields: map[string]any{"coordinates": map[string]any{"latitude": 55.7}},
	}

	snapshot := widgetResourceSnapshot(item)
	if snapshot.ID != int64(item.ID) || snapshot.Title != item.Title || snapshot.Content != item.Content ||
		snapshot.Path != path || snapshot.PublishedAt == nil || !snapshot.PublishedAt.Equal(publishedAt) ||
		snapshot.ImageMediaID == nil || *snapshot.ImageMediaID != int64(imageID) {
		t.Fatalf("resource snapshot = %#v", snapshot)
	}
	coordinates, ok := snapshot.Fields["coordinates"].(map[string]any)
	if !ok || coordinates["latitude"] != 55.7 {
		t.Fatalf("resource fields = %#v", snapshot.Fields)
	}

	coordinates["latitude"] = 0.0
	*snapshot.PublishedAt = snapshot.PublishedAt.Add(time.Hour)
	*snapshot.ImageMediaID = 99
	if item.Fields["coordinates"].(map[string]any)["latitude"] != 55.7 ||
		!item.PublishedAt.Equal(publishedAt) || *item.ImageMediaID != imageID {
		t.Fatal("widget snapshot mutations changed the resource")
	}
}
