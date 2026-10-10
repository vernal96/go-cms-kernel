package app

import (
	"context"
	"time"
)

func (a *App) startMediaFileDeletionCleanup(ctx context.Context, cleanup interface{ RetryPending(context.Context) error }) {
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			check, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := cleanup.RetryPending(check)
			cancel()
			if err != nil && ctx.Err() == nil {
				a.logger.ErrorContext(ctx, "retry media file deletion", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
