package resource

import "github.com/vernal96/go-cms-kernel/modules/core/widget"

func SnapshotFromResource(item Resource) Snapshot {
	widgets := make([]WidgetSnapshot, len(item.Widgets))
	for index, binding := range item.Widgets {
		widgets[index] = WidgetSnapshot{
			Code: binding.Code, Area: binding.Area, Position: binding.Position,
			View: binding.Presentation.View, Columns: binding.Presentation.Columns,
			MarginTop: binding.Presentation.MarginTop, MarginBottom: binding.Presentation.MarginBottom,
			Enabled: binding.Presentation.Enabled, Params: binding.Params, ParamBindings: widget.CloneParamBindings(binding.ParamBindings),
		}
	}
	return Snapshot{
		StorageKind: StorageTree, ParentID: item.ParentID, Type: item.Type,
		Template: item.Template, ContentType: item.ContentType, Title: item.Title,
		MenuTitle: item.MenuTitle, Slug: item.Slug, Annotation: item.Annotation,
		Content: item.Content, ImageMediaID: item.ImageMediaID,
		TargetResourceID: item.TargetResourceID, ExternalURL: item.ExternalURL,
		IsPublic: item.IsPublic, IsSearchable: item.IsSearchable, InMenu: item.InMenu,
		InSitemap: item.InSitemap, Sort: item.Sort, PublishedAt: item.PublishedAt,
		UnpublishedAt: item.UnpublishedAt, Fields: item.Fields,
		TypeSettings: item.TypeSettings, Widgets: widgets,
	}
}

func SnapshotFromLibraryItem(item LibraryItem) Snapshot {
	widgets := make([]WidgetSnapshot, len(item.Widgets))
	for index, binding := range item.Widgets {
		widgets[index] = WidgetSnapshot{
			Code: binding.Code, Area: binding.Area, Position: binding.Position,
			View: binding.Presentation.View, Columns: binding.Presentation.Columns,
			MarginTop: binding.Presentation.MarginTop, MarginBottom: binding.Presentation.MarginBottom,
			Enabled: binding.Presentation.Enabled, Params: binding.Params, ParamBindings: widget.CloneParamBindings(binding.ParamBindings),
		}
	}
	libraryID := item.LibraryID
	return Snapshot{
		StorageKind: StorageLibraryItem, LibraryID: &libraryID,
		Template: item.Template, ContentType: item.ContentType, Title: item.Title,
		Slug: item.Slug, Annotation: item.Annotation, Content: item.Content,
		ImageMediaID: item.ImageMediaID, IsPublic: item.IsPublic,
		IsSearchable: item.IsSearchable, PublishedAt: item.PublishedAt,
		UnpublishedAt: item.UnpublishedAt, Fields: item.Fields,
		TypeSettings: map[string]any{}, Widgets: widgets,
	}
}
