package resource

import (
	"context"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

// WidgetOccurrenceReferences uses the pinned site runtime during lifecycle
// mutations. Maintenance callers provide explicit already-normalized references.
func WidgetOccurrenceReferences(ctx context.Context, siteID site.ID, binding widget.Binding) ([]field.Reference, error) {
	m := mutationFrom(ctx)
	if m == nil {
		return binding.References, nil
	}
	runtime, ok := m.service.runtime(ctx, siteID)
	if !ok {
		return nil, ErrInvalid
	}
	w, ok := runtime.Profile().Widget(binding.Code)
	if !ok {
		return nil, ErrInvalid
	}
	return w.FieldSchema().StoredReferences(binding.Params)
}
