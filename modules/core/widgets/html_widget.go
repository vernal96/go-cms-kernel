package widgets

import (
	"context"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"time"
)

var HTML = widget.NewRef("html")

func htmlWidget() widget.Widget {
	return widget.Functional{CachePolicy: func(widget.RenderInput) widget.ResultCachePolicy {
		return widget.ResultCachePolicy{TTL: 5 * time.Minute}
	}, Description: widget.Definition{Reference: HTML, Label: "Текстовый контент", Description: "Редактируемый HTML-контент", Fields: []field.Definition{{Key: "html", Type: field.TypeString, Label: "HTML", Editor: "html"}}}, Render: func(ctx context.Context, input widget.RenderInput, params map[string]any) (map[string]any, error) {
		html, _ := params["html"].(string)
		return map[string]any{"html": html}, nil
	}}
}
