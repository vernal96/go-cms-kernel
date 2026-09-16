package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type currentUserService struct {
	user.Service
	current user.User
	err     error
}

func (s currentUserService) Current(
	context.Context,
	security.Actor,
) (user.User, error) {
	return s.current, s.err
}

type accessAuthorizer struct {
	err   error
	code  permission.Code
	codes []permission.Code
}

func (a *accessAuthorizer) Check(
	_ context.Context,
	_ security.Actor,
	code permission.Code,
) error {
	a.code = code
	a.codes = append(a.codes, code)
	return a.err
}

func TestAdminSessionRequiresAuthenticationAndPermission(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		actor      security.Actor
		accessErr  error
		currentErr error
		wantStatus int
	}{
		{
			name:       "guest",
			actor:      security.Guest(),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "forbidden",
			actor:      security.User(1),
			accessErr:  security.ErrForbidden,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "inactive user",
			actor:      security.User(1),
			currentErr: security.ErrUnauthenticated,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "authorized",
			actor:      security.User(1),
			wantStatus: http.StatusOK,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			authorizer := &accessAuthorizer{err: test.accessErr}
			runtime := &Runtime{
				users: currentUserService{
					current: user.User{
						ID:    1,
						Login: "admin",
						Email: "admin@example.test",
						Name:  "Администратор",
					},
					err: test.currentErr,
				},
				authorization: authorizer,
			}
			handler, err := runtime.SessionHandler()
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(
				http.MethodGet,
				"/api/admin/session",
				nil,
			)
			request = request.WithContext(httptransport.WithActor(
				request.Context(),
				test.actor,
			))
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, body = %q",
					response.Code,
					response.Body.String(),
				)
			}
			if test.actor.IsUser() &&
				(len(authorizer.codes) == 0 || authorizer.codes[0] != AccessPermission) {
				t.Fatalf("permissions = %q", authorizer.codes)
			}
			if response.Code == http.StatusUnauthorized &&
				response.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf(
					"WWW-Authenticate = %q",
					response.Header().Get("WWW-Authenticate"),
				)
			}
		})
	}
}

func TestAdminSessionReturnsSafeCurrentUserPayload(t *testing.T) {
	t.Parallel()

	lastName := "Иванов"
	middleName := "Иванович"
	avatarID := media.ID(7)
	authorizer := &accessAuthorizer{}
	runtime := &Runtime{
		users: currentUserService{current: user.User{
			ID:            42,
			Login:         "admin",
			Email:         "admin@example.test",
			Name:          "Иван",
			LastName:      &lastName,
			MiddleName:    &middleName,
			AvatarMediaID: &avatarID,
			ColorScheme:   user.ColorSchemeDark,
			AccentColor:   user.AccentColorViolet,
		}},
		authorization: authorizer,
	}
	handler, err := runtime.SessionHandler()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/admin/session",
		nil,
	)
	request = request.WithContext(httptransport.WithActor(
		request.Context(),
		security.User(42),
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf(
			"status = %d, body = %q",
			response.Code,
			response.Body.String(),
		)
	}
	var payload sessionResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.User.ID != 42 ||
		payload.User.Login != "admin" ||
		payload.User.Email != "admin@example.test" ||
		payload.User.DisplayName != "Иванов Иван Иванович" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.User.ColorScheme != user.ColorSchemeDark ||
		payload.User.AccentColor != user.AccentColorViolet || !payload.User.HasAvatar ||
		payload.User.AvatarUpdatedAt == nil {
		t.Fatalf("profile session payload = %#v", payload.User)
	}
}

func TestAdminAuthorizationMapsUnexpectedErrors(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{
		users: currentUserService{},
		authorization: &accessAuthorizer{
			err: errors.New("database unavailable"),
		},
	}
	handler, err := runtime.SessionHandler()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/admin/session",
		nil,
	)
	request = request.WithContext(httptransport.WithActor(
		request.Context(),
		security.User(1),
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf(
			"status = %d, body = %q",
			response.Code,
			response.Body.String(),
		)
	}
}

func TestAdminRouterNoLongerOwnsCMSCRUD(t *testing.T) {
	t.Parallel()
	router := chi.NewRouter()
	registerManagementRoutes(router, &Management{})
	for _, path := range []string{"/sites", "/sites/7/resources", "/filesystem/disks"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, body = %q", path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/navigation", nil))
	if response.Code == http.StatusNotFound {
		t.Fatal("admin navigation route is missing")
	}
}
