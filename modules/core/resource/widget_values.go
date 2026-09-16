package resource

import "github.com/vernal96/go-cms-kernel/modules/core/widget"

func (r Resource) WidgetValues() widget.ResourceValues {
	properties := map[string]any{"title": r.Title, "menu_title": r.MenuTitle, "slug": r.Slug, "annotation": r.Annotation, "content": r.Content}
	if r.Path != nil {
		properties["path"] = *r.Path
	}
	if r.ImageMediaID != nil {
		properties["image_media_id"] = int64(*r.ImageMediaID)
	}
	return widget.ResourceValues{Fields: r.Fields, Properties: properties}
}
