package background

import (
	"context"
	"errors"
	"time"
)

// RunPeriodic runs immediately, then at each interval until canceled. The first
// operation error stops the task; cancellation is a normal shutdown.
func RunPeriodic(ctx context.Context, interval time.Duration, run func(context.Context) error) error {
	if ctx == nil || interval <= 0 || run == nil {
		return errors.New("invalid periodic task")
	}
	if ctx.Err() != nil {
		return nil
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := run(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if ctx.Err() != nil {
				return nil
			}
		}
	}
}
