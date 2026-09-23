package app

import (
	"context"
	"time"
)

func (a *App) startSiteSynchronization(ctx context.Context) {
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		prune := time.NewTicker(time.Hour)
		defer prune.Stop()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-prune.C:
				if a.services.Sessions != nil {
					check, cancel := context.WithTimeout(ctx, 10*time.Second)
					err := a.services.Sessions.PruneSessions(check)
					cancel()
					if err != nil {
						a.logger.ErrorContext(ctx, "prune sessions", "error", err)
					}
				}
			case <-ticker.C:
				check, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := a.sites.Synchronize(check)
				cancel()
				if err != nil && ctx.Err() == nil {
					a.logger.ErrorContext(ctx, "synchronize site runtimes", "error", err)
				}
			}
		}
	}()
}
