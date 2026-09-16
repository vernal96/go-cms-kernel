package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Config describes the HTTP listener independently of project configuration.
type Config struct {
	Address         string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

type Server struct {
	server *http.Server
	config Config
	logger *slog.Logger
}

func NewServer(
	config Config,
	handler http.Handler,
	logger *slog.Logger,
) (*Server, error) {
	if handler == nil {
		return nil, errors.New("HTTP handler is nil")
	}
	if logger == nil {
		return nil, errors.New("HTTP server logger is nil")
	}
	return &Server{
		server: &http.Server{
			Addr:         config.Address,
			Handler:      handler,
			ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelError),
			ReadTimeout:  config.ReadTimeout,
			WriteTimeout: config.WriteTimeout,
			IdleTimeout:  config.IdleTimeout,
		},
		config: config,
		logger: logger,
	}, nil
}

func (s *Server) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("HTTP server context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.logger.InfoContext(
		ctx,
		"HTTP server starting",
		slog.String("event", "http.server.started"),
		slog.String("server.address", s.server.Addr),
	)

	result := make(chan error, 1)

	go func() {
		err := s.server.ListenAndServe()

		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}

		result <- err
	}()

	select {
	case err := <-result:
		if err != nil {
			s.logger.ErrorContext(
				context.WithoutCancel(ctx),
				"HTTP server stopped with an error",
				slog.String("event", "http.server.failed"),
				slog.Any("error", err),
			)
		} else {
			s.logger.InfoContext(
				context.WithoutCancel(ctx),
				"HTTP server stopped",
				slog.String("event", "http.server.stopped"),
			)
		}
		return err

	case <-ctx.Done():
		s.logger.Info(
			"HTTP server shutdown started",
			slog.String("event", "http.server.shutdown.started"),
		)
		shutdownContext, cancel := context.WithTimeout(
			context.Background(),
			s.config.ShutdownTimeout,
		)
		defer cancel()

		err := s.server.Shutdown(shutdownContext)
		if err != nil {
			s.logger.Error(
				"HTTP server shutdown failed",
				slog.String("event", "http.server.shutdown.failed"),
				slog.Any("error", err),
			)
			return err
		}
		s.logger.Info(
			"HTTP server shutdown completed",
			slog.String("event", "http.server.shutdown.completed"),
		)
		return nil
	}
}
