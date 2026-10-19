package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v3"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	coreuser "github.com/vernal96/go-cms-kernel/modules/core/user"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

func TestAccessLogServerErrors(t *testing.T) {
	t.Parallel()
	cause := errors.New("storage connection failed")
	for _, test := range []struct {
		name    string
		handler http.Handler
		status  int
		cause   string
	}{
		{
			name: "login authentication",
			handler: &loginHandler{
				authenticator: &stubAuthenticator{err: cause},
				tokens:        &stubAccessTokens{},
				limits:        newLoginLimits(),
			},
			status: http.StatusInternalServerError, cause: cause.Error(),
		},
		{
			name: "token issuance",
			handler: &loginHandler{
				authenticator: &stubAuthenticator{user: coreuser.User{ID: 42}},
				tokens:        &stubAccessTokens{issueErr: cause},
				limits:        newLoginLimits(),
			},
			status: http.StatusInternalServerError, cause: cause.Error(),
		},
		{
			name: "token verification before routing",
			handler: optionalAuthentication(&stubAccessTokens{verifyErr: cause})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("authentication failure reached module handler")
			})),
			status: http.StatusInternalServerError, cause: cause.Error(),
		},
		{
			name: "site runtime unavailable",
			handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				writeSiteManagementError(request.Context(), response, site.ErrUnavailable)
			}),
			status: http.StatusServiceUnavailable, cause: site.ErrUnavailable.Error(),
		},
		{
			name: "status without cause",
			handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				httptransport.WriteJSONError(response, http.StatusBadGateway, "upstream_unavailable", "service unavailable")
			}),
			status: http.StatusBadGateway, cause: http.StatusText(http.StatusBadGateway),
		},
		{
			name: "panic",
			handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				panic("panic-secret")
			}),
			status: http.StatusInternalServerError, cause: "panic recovered",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			router := chi.NewRouter()
			router.Use(chimiddleware.RequestID)
			router.Use(httplog.RequestLogger(accessLogger(slog.New(slog.NewJSONHandler(&logs, nil))), accessLogOptions()))
			router.Use(requestLogMetadata)
			router.Method(http.MethodPost, "/api/test", test.handler)
			request := httptest.NewRequest(http.MethodPost, "/api/test?private=query-secret", strings.NewReader(`{"identifier":"admin","password":"body-secret"}`))
			request.Header.Set("Authorization", "Bearer token-secret")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status || strings.Contains(response.Body.String(), cause.Error()) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			decoder := json.NewDecoder(&logs)
			var record map[string]any
			if err := decoder.Decode(&record); err != nil {
				t.Fatal(err)
			}
			if record["level"] != "ERROR" || record["http.request.method"] != http.MethodPost ||
				record["http.response.status_code"] != float64(test.status) ||
				record["http.request.id"] == "" || record["http.route"] != "/api/test" {
				t.Fatalf("access record = %#v", record)
			}
			if test.cause != "" && record["error.message"] != test.cause {
				t.Fatalf("error cause = %#v", record)
			}
			if decoder.Decode(&map[string]any{}) != io.EOF {
				t.Fatal("expected exactly one access record")
			}
			serialized, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"query-secret", "body-secret", "token-secret", "panic-secret"} {
				if bytes.Contains(serialized, []byte(secret)) {
					t.Fatalf("access log leaked %s: %s", secret, serialized)
				}
			}
		})
	}
}
