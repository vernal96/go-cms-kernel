package core

import (
	"fmt"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/modules/core/widgets"
)

func (r *Runtime) Widgets() []widget.Widget {
	if r == nil {
		return nil
	}
	return append([]widget.Widget(nil), r.widgets...)
}

func buildWidgets(r *Runtime, store cache.Store, types []resourcetype.Code, templates []template.Definition) error {
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
	r.widgets = append(widgets.All(), widgets.NewResourceList(query, store, types, templates))
	return nil
}

var _ widget.Provider = (*Runtime)(nil)
