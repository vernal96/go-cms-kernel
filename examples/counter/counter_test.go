package counter_test

import (
	"context"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/examples/counter"
	"testing"
)

func TestCounterHasIndependentRuntimeAndTypedLimit(t *testing.T) {
	ctx := context.Background()
	declaration := counter.New(17)
	first, err := declaration.Build(ctx, kernel.ModuleContext{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := declaration.Build(ctx, kernel.ModuleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first.(*counter.Runtime).Limit() != 17 || second.(*counter.Runtime).Limit() != 17 {
		t.Fatal("runtime configuration or ownership is invalid")
	}
	if err := counter.New(0).Validate(ctx, kernel.ModuleValidationContext{}); err == nil {
		t.Fatal("invalid limit accepted")
	}
}
