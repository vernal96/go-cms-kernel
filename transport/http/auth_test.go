package httptransport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/security"
)

func TestRequireAuthenticated(t *testing.T) {
	tests := []struct {
		name       string
		actor      *security.Actor
		wantStatus int
		wantCalled bool
	}{
		{
			name:       "missing actor",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "guest",
			actor:      actorPointer(security.Guest()),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "user",
			actor:      actorPointer(security.User(42)),
			wantStatus: http.StatusNoContent,
			wantCalled: true,
		},
		{
			name:       "system is not an API user",
			actor:      actorPointer(security.System()),
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := RequireAuthenticated(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				called = true
				response.WriteHeader(http.StatusNoContent)
			}))

			request := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if test.actor != nil {
				request = request.WithContext(
					WithActor(request.Context(), *test.actor),
				)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus || called != test.wantCalled {
				t.Fatalf(
					"response = %d, called = %t",
					response.Code,
					called,
				)
			}
			if test.wantStatus == http.StatusUnauthorized {
				if response.Header().Get("WWW-Authenticate") != "Bearer" ||
					!strings.Contains(
						response.Body.String(),
						`"code":"unauthenticated"`,
					) {
					t.Fatalf(
						"unauthorized response = %#v, %q",
						response.Header(),
						response.Body.String(),
					)
				}
			}
		})
	}
}

func actorPointer(actor security.Actor) *security.Actor {
	return &actor
}
