package filelogger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/libtnb/logrotate"
	"github.com/vernal96/go-cms-kernel/logging"
)

const (
	defaultMaxSize    = 100 * logrotate.MB
	defaultMaxBackups = 14
	defaultMaxAge     = 14 * logrotate.Day
)

type Config struct {
	Path        string
	Level       slog.Level
	ServiceName string
	Environment string
	Location    *time.Location
	MaxSize     int64
	MaxBackups  int
	MaxAge      time.Duration
	Compress    bool
	Clock       logrotate.Clock
}

type Connector struct {
	logger    *slog.Logger
	writer    *logrotate.Writer
	closeOnce sync.Once
	closeErr  error
}

func New(ctx context.Context, config Config) (*Connector, error) {
	if ctx == nil {
		return nil, errors.New("file logger context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config.Path = strings.TrimSpace(config.Path)
	config.ServiceName = strings.TrimSpace(config.ServiceName)
	config.Environment = strings.TrimSpace(config.Environment)
	if config.Path == "" {
		return nil, errors.New("file logger path is empty")
	}
	if config.ServiceName == "" {
		return nil, errors.New("file logger service name is empty")
	}
	if config.Environment == "" {
		return nil, errors.New("file logger environment is empty")
	}
	if config.Location == nil {
		config.Location = time.Local
	}
	if config.MaxSize == 0 {
		config.MaxSize = defaultMaxSize
	}
	if config.MaxSize < 0 {
		return nil, errors.New("file logger max size is invalid")
	}
	if config.MaxBackups == 0 {
		config.MaxBackups = defaultMaxBackups
	}
	if config.MaxBackups < 0 {
		return nil, errors.New("file logger max backups is invalid")
	}
	if config.MaxAge == 0 {
		config.MaxAge = defaultMaxAge
	}
	if config.MaxAge < 0 {
		return nil, errors.New("file logger max age is invalid")
	}

	options := []logrotate.Option{
		logrotate.WithFileMode(0o600),
		logrotate.WithLocation(config.Location),
		logrotate.WithMaxSize(config.MaxSize),
		logrotate.WithRotateEvery(24 * time.Hour),
		logrotate.WithMaxBackups(config.MaxBackups),
		logrotate.WithMaxAge(config.MaxAge),
	}
	if config.Clock != nil {
		options = append(options, logrotate.WithClock(config.Clock))
	}
	if config.Compress {
		options = append(options, logrotate.WithCompress())
	}

	writer, err := logrotate.New(config.Path, options...)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(writer.Filename(), 0o600); err != nil {
		return nil, errors.Join(
			fmt.Errorf("secure active log file: %w", err),
			writer.Close(),
		)
	}

	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{
		AddSource: true,
		Level:     config.Level,
	})
	return &Connector{
		logger: slog.New(handler).With(
			slog.String("service.name", config.ServiceName),
			slog.String(
				"deployment.environment.name",
				config.Environment,
			),
		),
		writer: writer,
	}, nil
}

func (c *Connector) Logger() *slog.Logger {
	if c == nil {
		return nil
	}
	return c.logger
}

func (c *Connector) Ping(ctx context.Context) error {
	if c == nil || c.writer == nil {
		return errors.New("file logger is unavailable")
	}
	if ctx == nil {
		return errors.New("file logger ping context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.writer.Sync()
}

func (c *Connector) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		if c.writer != nil {
			c.closeErr = c.writer.Close()
		}
	})
	return c.closeErr
}

var _ logging.Connector = (*Connector)(nil)
