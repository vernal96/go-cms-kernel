package image

import (
	"context"
	"errors"
	"io"
)

var ErrBusy = errors.New("image processing capacity exhausted")

// LimitedProcessor shares one application-wide budget across profiles and edits.
type LimitedProcessor struct {
	processor Processor
	slots     chan struct{}
}

func NewLimitedProcessor(processor Processor, concurrency int) *LimitedProcessor {
	if concurrency < 1 {
		panic("image concurrency must be positive")
	}
	return &LimitedProcessor{processor: processor, slots: make(chan struct{}, concurrency)}
}
func (p *LimitedProcessor) Transform(ctx context.Context, source io.Reader, options TransformOptions) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return Result{}, ErrBusy
	}
	return p.processor.Transform(ctx, source, options)
}
