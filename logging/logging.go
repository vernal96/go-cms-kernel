package logging

import (
	"context"
	"errors"
	"log/slog"
)

// Connector owns the project logger and its external resources.
type Connector interface {
	Logger() *slog.Logger
	Ping(context.Context) error
	Close() error
}

// Factory opens the single logger configured for an application instance.
type Factory interface {
	Open(context.Context) (Connector, error)
}

type reportedError struct {
	err error
}

func (e reportedError) Error() string {
	return e.err.Error()
}

func (e reportedError) Unwrap() error {
	return e.err
}

func Reported(err error) error {
	if err == nil || IsReported(err) {
		return err
	}
	return reportedError{err: err}
}

func IsReported(err error) bool {
	var target reportedError
	return errors.As(err, &target)
}
