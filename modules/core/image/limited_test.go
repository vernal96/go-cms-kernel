package image

import (
	"context"
	"errors"
	"io"
	"testing"
)

type blockingProcessor struct{ entered, release chan struct{} }

func (p blockingProcessor) Transform(context.Context, io.Reader, TransformOptions) (Result, error) {
	close(p.entered)
	<-p.release
	return Result{}, nil
}
func TestImageCapacityIsBounded(t *testing.T) {
	p := blockingProcessor{make(chan struct{}), make(chan struct{})}
	limited := NewLimitedProcessor(p, 1)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = limited.Transform(context.Background(), nil, TransformOptions{}) }()
	<-p.entered
	if _, err := limited.Transform(context.Background(), nil, TransformOptions{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("unbounded work: %v", err)
	}
	close(p.release)
	<-done
}
