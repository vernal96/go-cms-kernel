package core

import (
	"context"
	"fmt"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/modules/core/widgets"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Runtime) Widgets() []widget.Widget {
	if r == nil {
		return nil
	}
	return append([]widget.Widget(nil), r.widgets...)
}

func buildWidgets(r *Runtime, types []resourcetype.Code, templates []template.Definition) error {
	if r == nil || r.database == nil {
		return fmt.Errorf("core runtime database is nil")
	}
	repository, ok := r.database.Resources().(resource.QueryRepository)
	if !ok {
		return fmt.Errorf("resource query repository is unavailable")
	}
	query, err := resource.NewQueryService(repository)
	if err != nil {
		return err
	}
	r.resourceQuery = query
	collectionQuery := func(ctx context.Context, input resource.LibraryItemQuery) (resource.LibraryItemPage, resource.LibraryCollection, error) {
		if r.services.LibraryItems == nil {
			return resource.LibraryItemPage{}, resource.LibraryCollection{}, fmt.Errorf("library service is unavailable")
		}
		return r.services.LibraryItems.QueryCollection(ctx, security.Guest(), input)
	}
	r.widgets = append(widgets.All(), widgets.NewResourceList(query, types, templates), widgets.NewLibraryResources(collectionQuery, false), widgets.NewLibraryResources(collectionQuery, true))
	return nil
}

var _ widget.Provider = (*Runtime)(nil)
