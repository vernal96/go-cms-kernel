package lokilogger

import (
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func testLokiConfig(endpoint string) Config {
	return Config{
		Endpoint:         endpoint,
		Level:            slog.LevelInfo,
		ServiceName:      "cms-test",
		Environment:      "test",
		ReadinessTimeout: time.Second,
		ExportTimeout:    time.Second,
		ShutdownTimeout:  time.Second,
	}
}

func TestConnectorChecksReadinessAndFlushesOTLPLogs(t *testing.T) {
	var (
		mu                sync.Mutex
		readinessCalls    int
		exportCalls       int
		exportBody        string
		contentEncoding   string
		exportContentType string
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/ready":
			mu.Lock()
			readinessCalls++
			mu.Unlock()
			response.WriteHeader(http.StatusOK)
		case "/otlp/v1/logs":
			reader, err := gzip.NewReader(request.Body)
			if err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			data, err := io.ReadAll(reader)
			_ = reader.Close()
			if err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			exportCalls++
			exportBody = string(data)
			contentEncoding = request.Header.Get("Content-Encoding")
			exportContentType = request.Header.Get("Content-Type")
			mu.Unlock()
			response.WriteHeader(http.StatusOK)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	connector, err := New(context.Background(), testLokiConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := connector.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	connector.Logger().Info(
		"exported record",
		slog.String("component", "test"),
	)
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}
	if err := connector.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if readinessCalls != 1 {
		t.Fatalf("readiness calls = %d", readinessCalls)
	}
	if exportCalls != 1 {
		t.Fatalf("export calls = %d", exportCalls)
	}
	if contentEncoding != "gzip" ||
		exportContentType != "application/x-protobuf" {
		t.Fatalf(
			"OTLP headers = encoding %q, content type %q",
			contentEncoding,
			exportContentType,
		)
	}
	for _, expected := range []string{
		"exported record",
		"component",
		"cms-test",
		"deployment.environment.name",
	} {
		if !strings.Contains(exportBody, expected) {
			t.Fatalf("OTLP body does not contain %q", expected)
		}
	}
}

func TestConnectorRejectsNonReadyLoki(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		http.Error(response, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	connector, err := New(context.Background(), testLokiConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connector.Close() }()
	err = connector.Ping(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP status 503") {
		t.Fatalf("readiness error = %v", err)
	}
}

func TestConnectorReadinessHonorsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		time.Sleep(100 * time.Millisecond)
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := testLokiConfig(server.URL)
	config.ReadinessTimeout = 20 * time.Millisecond
	config.HTTPClient = &http.Client{}
	connector, err := New(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connector.Close() }()

	started := time.Now()
	err = connector.Ping(context.Background())
	if err == nil {
		t.Fatal("readiness timeout was accepted")
	}
	if elapsed := time.Since(started); elapsed >= 90*time.Millisecond {
		t.Fatalf("readiness timeout took %s", elapsed)
	}
}

func TestConnectorRejectsInvalidConfiguration(t *testing.T) {
	valid := testLokiConfig("http://localhost:3100")
	tests := []struct {
		name   string
		ctx    context.Context
		config Config
	}{
		{name: "nil context", config: valid},
		{
			name: "empty endpoint",
			ctx:  context.Background(),
			config: Config{
				ServiceName: "cms",
				Environment: "test",
			},
		},
		{
			name: "endpoint path",
			ctx:  context.Background(),
			config: func() Config {
				config := valid
				config.Endpoint += "/loki"
				return config
			}(),
		},
		{
			name: "empty service",
			ctx:  context.Background(),
			config: func() Config {
				config := valid
				config.ServiceName = ""
				return config
			}(),
		},
		{
			name: "empty environment",
			ctx:  context.Background(),
			config: func() Config {
				config := valid
				config.Environment = ""
				return config
			}(),
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := New(testCase.ctx, testCase.config); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}
