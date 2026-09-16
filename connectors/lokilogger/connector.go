package lokilogger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vernal96/go-cms-kernel/logging"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/processors/minsev"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

const instrumentationName = "github.com/vernal96/go-cms"

type Config struct {
	Endpoint         string
	Level            slog.Level
	ServiceName      string
	Environment      string
	ReadinessTimeout time.Duration
	ExportTimeout    time.Duration
	ShutdownTimeout  time.Duration
	HTTPClient       *http.Client
}

type Connector struct {
	logger           *slog.Logger
	provider         *log.LoggerProvider
	readinessURL     string
	httpClient       *http.Client
	readinessTimeout time.Duration
	shutdownTimeout  time.Duration
	closeOnce        sync.Once
	closeErr         error
}

func New(ctx context.Context, config Config) (*Connector, error) {
	if ctx == nil {
		return nil, errors.New("Loki logger context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config.ServiceName = strings.TrimSpace(config.ServiceName)
	config.Environment = strings.TrimSpace(config.Environment)
	if config.ServiceName == "" {
		return nil, errors.New("Loki logger service name is empty")
	}
	if config.Environment == "" {
		return nil, errors.New("Loki logger environment is empty")
	}
	endpoint, err := parseEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	if config.ReadinessTimeout <= 0 {
		config.ReadinessTimeout = 5 * time.Second
	}
	if config.ExportTimeout <= 0 {
		config.ExportTimeout = 5 * time.Second
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 5 * time.Second
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{
			Timeout: config.ReadinessTimeout,
		}
	}

	otlpURL := *endpoint
	otlpURL.Path = "/otlp/v1/logs"
	exporter, err := otlploghttp.New(
		ctx,
		otlploghttp.WithEndpointURL(otlpURL.String()),
		otlploghttp.WithCompression(otlploghttp.GzipCompression),
		otlploghttp.WithTimeout(config.ExportTimeout),
	)
	if err != nil {
		return nil, fmt.Errorf("open Loki OTLP exporter: %w", err)
	}

	processor := log.NewBatchProcessor(exporter)
	filtered := minsev.NewLogProcessor(
		processor,
		minimumSeverity(config.Level),
	)
	provider := log.NewLoggerProvider(
		log.WithProcessor(filtered),
		log.WithResource(resource.NewSchemaless(
			attribute.String("service.name", config.ServiceName),
			attribute.String(
				"deployment.environment.name",
				config.Environment,
			),
		)),
	)

	readinessURL := *endpoint
	readinessURL.Path = "/ready"
	return &Connector{
		logger: slog.New(otelslog.NewHandler(
			instrumentationName,
			otelslog.WithLoggerProvider(provider),
			otelslog.WithSource(true),
		)),
		provider:         provider,
		readinessURL:     readinessURL.String(),
		httpClient:       config.HTTPClient,
		readinessTimeout: config.ReadinessTimeout,
		shutdownTimeout:  config.ShutdownTimeout,
	}, nil
}

func parseEndpoint(value string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("Loki logger endpoint is empty")
	}
	endpoint, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("parse Loki logger endpoint: %w", err)
	}
	if (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.Host == "" ||
		(endpoint.Path != "" && endpoint.Path != "/") ||
		endpoint.RawQuery != "" ||
		endpoint.Fragment != "" ||
		endpoint.User != nil {
		return nil, errors.New(
			"Loki logger endpoint must be an HTTP(S) origin without path, credentials, query, or fragment",
		)
	}
	endpoint.Path = ""
	return endpoint, nil
}

func minimumSeverity(level slog.Level) minsev.Severity {
	switch {
	case level <= slog.LevelDebug:
		return minsev.SeverityDebug
	case level <= slog.LevelInfo:
		return minsev.SeverityInfo
	case level <= slog.LevelWarn:
		return minsev.SeverityWarn
	default:
		return minsev.SeverityError
	}
}

func (c *Connector) Logger() *slog.Logger {
	if c == nil {
		return nil
	}
	return c.logger
}

func (c *Connector) Ping(ctx context.Context) error {
	if c == nil || c.httpClient == nil || c.readinessURL == "" {
		return errors.New("Loki logger is unavailable")
	}
	if ctx == nil {
		return errors.New("Loki logger ping context is nil")
	}
	pingContext, cancel := context.WithTimeout(ctx, c.readinessTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		pingContext,
		http.MethodGet,
		c.readinessURL,
		nil,
	)
	if err != nil {
		return fmt.Errorf("create Loki readiness request: %w", err)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("check Loki readiness: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf(
			"check Loki readiness: unexpected HTTP status %d",
			response.StatusCode,
		)
	}
	return nil
}

func (c *Connector) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		if c.provider == nil {
			return
		}
		ctx, cancel := context.WithTimeout(
			context.Background(),
			c.shutdownTimeout,
		)
		defer cancel()
		c.closeErr = errors.Join(
			c.provider.ForceFlush(ctx),
			c.provider.Shutdown(ctx),
		)
	})
	return c.closeErr
}

var _ logging.Connector = (*Connector)(nil)
