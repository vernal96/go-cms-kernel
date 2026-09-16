package background

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPeriodicRunsImmediatelyAndStopsOnError(t *testing.T) {
	failure := errors.New("operation failed")
	calls := 0
	if err := RunPeriodic(context.Background(), time.Hour, func(context.Context) error { calls++; return failure }); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
func TestPeriodicStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	if err := RunPeriodic(ctx, time.Millisecond, func(context.Context) error {
		calls++
		if calls == 2 {
			cancel()
			return ctx.Err()
		}
		return nil
	}); err != nil || calls != 2 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
	if err := RunPeriodic(ctx, time.Hour, func(context.Context) error { t.Fatal("canceled task ran"); return nil }); err != nil {
		t.Fatal(err)
	}
}
