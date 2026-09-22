package widget

import (
	"context"
	"errors"
)

// Functional implements a simple widget with a declaration and a renderer.
// Catalog still owns parameter validation and site-scoped compilation.
type Functional struct {
	Description Definition
	CachePolicy func(RenderInput) ResultCachePolicy
	Render      func(context.Context, RenderInput, map[string]any) (map[string]any, error)
}

func (w Functional) Definition() Definition { return w.Description }
func (w Functional) New(params map[string]any) (Instance, error) {
	if w.Render == nil {
		return nil, errors.New("widget renderer is nil")
	}
	return functionalInstance{render: w.Render, params: params, policy: w.CachePolicy}, nil
}

type functionalInstance struct {
	policy func(RenderInput) ResultCachePolicy
	render func(context.Context, RenderInput, map[string]any) (map[string]any, error)
	params map[string]any
}

func (i functionalInstance) Render(ctx context.Context, input RenderInput) (map[string]any, error) {
	if ctx == nil {
		return nil, errors.New("widget render context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return i.render(ctx, input, i.params)
}

func (i functionalInstance) ResultCachePolicy(input RenderInput) ResultCachePolicy {
	if i.policy == nil {
		return ResultCachePolicy{}
	}
	return i.policy(input)
}
