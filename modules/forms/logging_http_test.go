package forms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/httplog/v3"
)

func TestServerErrorsReachRequestLog(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		write  func(context.Context, http.ResponseWriter, error)
		cause  error
		status int
	}{
		{name: "management", write: writeManagementError, cause: errors.New("Forms database operation detail"), status: 500},
		{name: "public", write: writePublicError, cause: errors.New("Forms submission database detail"), status: 500},
		{name: "draining", write: writePublicError, cause: ErrRuntimeDraining, status: 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			handler := httplog.RequestLogger(logger, &httplog.Options{Schema: httplog.SchemaOTEL})(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				test.write(request.Context(), response, test.cause)
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/test", nil))
			if response.Code != test.status || strings.Contains(response.Body.String(), test.cause.Error()) {
				t.Fatalf("unsafe or unexpected response: %d %s", response.Code, response.Body.String())
			}
			decoder := json.NewDecoder(&logs)
			var record map[string]any
			if err := decoder.Decode(&record); err != nil {
				t.Fatal(err)
			}
			if record["level"] != "ERROR" || record["error.message"] != test.cause.Error() || record["http.response.status_code"] != float64(test.status) {
				t.Fatalf("access record = %#v", record)
			}
			if decoder.Decode(&map[string]any{}) != io.EOF {
				t.Fatal("expected exactly one access record")
			}
		})
	}
}
