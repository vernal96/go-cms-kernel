package lokilogger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIntegrationLokiExportAndQuery(t *testing.T) {
	endpoint := os.Getenv("LOKI_INTEGRATION_ENDPOINT")
	if endpoint == "" {
		t.Skip("LOKI_INTEGRATION_ENDPOINT is not set")
	}

	marker := fmt.Sprintf("cms-integration-%d", time.Now().UnixNano())
	connector, err := New(context.Background(), Config{
		Endpoint:         endpoint,
		Level:            slog.LevelInfo,
		ServiceName:      "go-cms-integration",
		Environment:      "integration",
		ReadinessTimeout: 5 * time.Second,
		ExportTimeout:    5 * time.Second,
		ShutdownTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := connector.Ping(context.Background()); err != nil {
		_ = connector.Close()
		t.Fatal(err)
	}
	connector.Logger().Info(marker)
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}

	queryURL, err := url.Parse(endpoint + "/loki/api/v1/query_range")
	if err != nil {
		t.Fatal(err)
	}
	values := queryURL.Query()
	values.Set(
		"query",
		`{service_name="go-cms-integration"} |= "`+marker+`"`,
	)
	values.Set("since", "5m")
	values.Set("limit", "20")
	queryURL.RawQuery = values.Encode()

	deadline := time.Now().Add(15 * time.Second)
	for {
		request, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			queryURL.String(),
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr == nil &&
				response.StatusCode == http.StatusOK &&
				strings.Contains(string(body), marker) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Loki query did not return marker %q", marker)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
