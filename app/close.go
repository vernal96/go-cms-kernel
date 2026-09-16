package app

import (
	"errors"
	"fmt"
	"log/slog"
)

func (a *App) Close() error {
	if a == nil {
		return nil
	}

	a.closeOnce.Do(func() {
		a.closed.Store(true)
		if a.logger != nil {
			a.logger.Info(
				"application shutdown started",
				slog.String("event", "app.shutdown.started"),
			)
		}

		var closeErrors []error
		a.lifecycleMu.Lock()
		if a.workerCancel != nil {
			a.workerCancel()
		}
		a.workers.Wait()
		if a.eventBus != nil {
			if err := a.eventBus.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf(
					"close project event bus: %w",
					err,
				))
			}
		}

		for index := len(a.connectors) - 1; index >= 0; index-- {
			connector := a.connectors[index]
			if err := connector.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf(
					"close database connector %q: %w",
					connector.Code(),
					err,
				))
			}
		}
		if a.caches != nil {
			if err := a.caches.Close(); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		if a.filesystems != nil {
			if err := a.filesystems.Close(); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		a.lifecycleMu.Unlock()

		dependencyCloseErr := errors.Join(closeErrors...)
		if a.logger != nil {
			if dependencyCloseErr != nil {
				a.logger.Error(
					"application shutdown failed",
					slog.String("event", "app.shutdown.failed"),
					slog.Any("error", dependencyCloseErr),
				)
			} else {
				a.logger.Info(
					"application shutdown completed",
					slog.String("event", "app.shutdown.completed"),
				)
			}
		}
		if a.loggerConnector != nil {
			if err := a.loggerConnector.Close(); err != nil {
				closeErrors = append(
					closeErrors,
					fmt.Errorf("close project logger: %w", err),
				)
			}
		}

		a.closeErr = errors.Join(closeErrors...)
	})

	return a.closeErr
}
