package widgets

import (
	"context"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"time"
)

var Content = widget.NewRef("content")

func contentWidget() widget.Widget {
	return widget.Functional{CachePolicy: func(widget.RenderInput) widget.ResultCachePolicy {
		return widget.ResultCachePolicy{TTL: 5 * time.Minute}
	}, Description: widget.Definition{Reference: Content, Label: "Content", Description: "Returns the resource content"}, Render: func(ctx context.Context, input widget.RenderInput, params map[string]any) (map[string]any, error) {
		return map[string]any{"content": input.Resource.Content}, nil
	}}
}
