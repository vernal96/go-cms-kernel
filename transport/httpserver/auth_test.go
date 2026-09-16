package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreuser "github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type stubAuthenticator struct {
	user  coreuser.User
	err   error
	input coreuser.AuthenticateInput
}

func (s *stubAuthenticator) Authenticate(
	_ context.Context,
	input coreuser.AuthenticateInput,
) (coreuser.User, error) {
	s.input = input
	return s.user, s.err
}

type stubAccessTokens struct {
	accessToken   security.AccessToken
	issueErr      error
	issuedActor   security.Actor
	verifiedActor security.Actor
	verifyErr     error
	verifiedValue string
}

func (s *stubAccessTokens) IssueAccessToken(
	_ context.Context,
	actor security.Actor,
) (security.AccessToken, error) {
	s.issuedActor = actor
	return s.accessToken, s.issueErr
}

func (s *stubAccessTokens) VerifyAccessToken(
	_ context.Context,
	value string,
) (security.Actor, error) {
	s.verifiedValue = value
	return s.verifiedActor, s.verifyErr
}

func TestLoginHandlerIssuesAccessToken(t *testing.T) {
	authenticator := &stubAuthenticator{
		user: coreuser.User{ID: 42},
	}
	tokens := &stubAccessTokens{accessToken: security.AccessToken{
		Value:     "signed-access-token",
		ExpiresAt: time.Date(2026, 7, 29, 12, 15, 0, 0, time.UTC),
	}}
	handler, err := newLoginHandler(authenticator, tokens)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/login",
		strings.NewReader(`{"identifier":"Admin","password":"secret"}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK ||
		!strings.Contains(
			response.Body.String(),
			`"access_token":"signed-access-token"`,
		) ||
		!strings.Contains(response.Body.String(), `"token_type":"Bearer"`) ||
		!strings.Contains(
			response.Body.String(),
			`"expires_at":"2026-07-29T12:15:00Z"`,
		) {
		t.Fatalf(
			"login response = %d, %q",
			response.Code,
			response.Body.String(),
		)
	}
	if authenticator.input.Identifier != "Admin" ||
		authenticator.input.Password != "secret" {
		t.Fatalf("authenticate input = %#v", authenticator.input)
	}
	id, exists := tokens.issuedActor.UserID()
	if !exists || id != 42 {
		t.Fatalf("issued actor = %#v", tokens.issuedActor)
	}
}

// JWT signing and claim verification belong to internal/security/jwt tests.
// The reusable transport exercises the public token contract end to end.
func TestLoginTokenAuthenticatesNextRequest(t *testing.T) {
	tokens := &stubAccessTokens{
		accessToken: security.AccessToken{
			Value:     "opaque-token",
			ExpiresAt: time.Date(2026, 7, 29, 12, 15, 0, 0, time.UTC),
		},
		verifiedActor: security.User(42),
	}
	handler, err := newLoginHandler(
		&stubAuthenticator{user: coreuser.User{ID: 42}},
		tokens,
	)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/login",
		strings.NewReader(`{"identifier":"admin","password":"secret"}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf(
			"login response = %d, %q",
			response.Code,
			response.Body.String(),
		)
	}

	var payload loginResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	var actor security.Actor
	protected := optionalAuthentication(tokens)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, _ = httptransport.ActorFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	nextRequest := httptest.NewRequest(http.MethodGet, "/resource", nil)
	nextRequest.Header.Set("Authorization", payload.TokenType+" "+payload.AccessToken)
	nextResponse := httptest.NewRecorder()
	protected.ServeHTTP(nextResponse, nextRequest)
	if nextResponse.Code != http.StatusNoContent || tokens.verifiedValue != tokens.accessToken.Value {
		t.Fatalf("authenticated response = %d, verified token = %q", nextResponse.Code, tokens.verifiedValue)
	}
	id, exists := actor.UserID()
	if !exists || id != 42 ||
		payload.TokenType != "Bearer" ||
		payload.ExpiresAt != "2026-07-29T12:15:00Z" {
		t.Fatalf("login payload = %#v, actor = %#v", payload, actor)
	}
}

func TestLoginHandlerRejectsInvalidRequestsAndCredentials(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		authErr       error
		issueErr      error
		user          coreuser.User
		wantStatus    int
		wantErrorCode string
	}{
		{
			name:          "malformed JSON",
			body:          `{`,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: "invalid_request",
		},
		{
			name:          "unknown field",
			body:          `{"identifier":"admin","password":"x","extra":true}`,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: "invalid_request",
		},
		{
			name:          "multiple values",
			body:          `{} {}`,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: "invalid_request",
		},
		{
			name:          "missing credentials",
			body:          `{}`,
			wantStatus:    http.StatusBadRequest,
			wantErrorCode: "invalid_request",
		},
		{
			name:          "invalid credentials",
			body:          `{"identifier":"admin","password":"wrong"}`,
			authErr:       coreuser.ErrInvalidCredentials,
			wantStatus:    http.StatusUnauthorized,
			wantErrorCode: "unauthenticated",
		},
		{
			name:          "authentication failure",
			body:          `{"identifier":"admin","password":"secret"}`,
			authErr:       errors.New("database unavailable"),
			wantStatus:    http.StatusInternalServerError,
			wantErrorCode: "internal_error",
		},
		{
			name:          "invalid authenticated user",
			body:          `{"identifier":"admin","password":"secret"}`,
			wantStatus:    http.StatusInternalServerError,
			wantErrorCode: "internal_error",
		},
		{
			name:          "token issuance failure",
			body:          `{"identifier":"admin","password":"secret"}`,
			user:          coreuser.User{ID: 42},
			issueErr:      errors.New("signing failed"),
			wantStatus:    http.StatusInternalServerError,
			wantErrorCode: "internal_error",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := &stubAuthenticator{
				user: test.user,
				err:  test.authErr,
			}
			tokens := &stubAccessTokens{issueErr: test.issueErr}
			handler, err := newLoginHandler(authenticator, tokens)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/auth/login",
				strings.NewReader(test.body),
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus ||
				!strings.Contains(
					response.Body.String(),
					`"code":"`+test.wantErrorCode+`"`,
				) {
				t.Fatalf(
					"response = %d, %q",
					response.Code,
					response.Body.String(),
				)
			}
			if test.wantStatus == http.StatusUnauthorized &&
				response.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf(
					"WWW-Authenticate = %q",
					response.Header().Get("WWW-Authenticate"),
				)
			}
			if strings.Contains(response.Body.String(), "database unavailable") ||
				strings.Contains(response.Body.String(), "signing failed") ||
				strings.Contains(response.Body.String(), "secret") ||
				strings.Contains(response.Body.String(), "wrong") {
				t.Fatalf("internal error leaked: %q", response.Body.String())
			}
		})
	}
}

func TestLoginHandlerRejectsOversizedBody(t *testing.T) {
	handler, err := newLoginHandler(
		&stubAuthenticator{},
		&stubAccessTokens{},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/login",
		strings.NewReader(
			`{"identifier":"admin","password":"`+
				strings.Repeat("x", maximumLoginBodyBytes)+`"}`,
		),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(response.Body.String(), "request_too_large") {
		t.Fatalf(
			"oversized response = %d, %q",
			response.Code,
			response.Body.String(),
		)
	}
}

func TestOptionalAuthenticationSetsGuestOrVerifiedUser(t *testing.T) {
	tests := []struct {
		name          string
		authorization []string
		query         string
		cookie        string
		initialActor  *security.Actor
		verifyActor   security.Actor
		verifyErr     error
		wantStatus    int
		wantUser      security.UserID
		wantGuest     bool
	}{
		{
			name:       "missing header is guest",
			wantStatus: http.StatusNoContent,
			wantGuest:  true,
		},
		{
			name:       "query token is ignored",
			query:      "?access_token=query-token",
			wantStatus: http.StatusNoContent,
			wantGuest:  true,
		},
		{
			name:       "cookie token is ignored",
			cookie:     "cookie-token",
			wantStatus: http.StatusNoContent,
			wantGuest:  true,
		},
		{
			name:         "existing actor becomes guest",
			initialActor: actorPointer(security.User(7)),
			wantStatus:   http.StatusNoContent,
			wantGuest:    true,
		},
		{
			name:          "valid bearer token",
			authorization: []string{"Bearer signed"},
			verifyActor:   security.User(42),
			wantStatus:    http.StatusNoContent,
			wantUser:      42,
		},
		{
			name:          "case insensitive scheme",
			authorization: []string{"bearer signed"},
			verifyActor:   security.User(42),
			wantStatus:    http.StatusNoContent,
			wantUser:      42,
		},
		{
			name:          "invalid token",
			authorization: []string{"Bearer sensitive.jwt.value"},
			verifyErr:     security.ErrInvalidAccessToken,
			wantStatus:    http.StatusUnauthorized,
		},
		{
			name:          "wrong scheme",
			authorization: []string{"Basic abc"},
			wantStatus:    http.StatusUnauthorized,
		},
		{
			name:          "empty token",
			authorization: []string{"Bearer "},
			wantStatus:    http.StatusUnauthorized,
		},
		{
			name:          "missing token",
			authorization: []string{"Bearer"},
			wantStatus:    http.StatusUnauthorized,
		},
		{
			name:          "ambiguous header",
			authorization: []string{"Bearer one", "Bearer two"},
			wantStatus:    http.StatusUnauthorized,
		},
		{
			name: "oversized header",
			authorization: []string{
				"Bearer " + strings.Repeat("x", maximumAuthorizationBytes),
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:          "verifier internal failure",
			authorization: []string{"Bearer signed"},
			verifyErr:     errors.New("key service unavailable"),
			wantStatus:    http.StatusInternalServerError,
		},
		{
			name:          "verifier cannot return guest",
			authorization: []string{"Bearer signed"},
			verifyActor:   security.Guest(),
			wantStatus:    http.StatusUnauthorized,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tokens := &stubAccessTokens{
				verifiedActor: test.verifyActor,
				verifyErr:     test.verifyErr,
			}
			var actor security.Actor
			handler := optionalAuthentication(tokens)(http.HandlerFunc(func(
				response http.ResponseWriter,
				request *http.Request,
			) {
				var exists bool
				actor, exists = httptransport.ActorFromContext(
					request.Context(),
				)
				if !exists {
					t.Fatal("request actor is missing")
				}
				response.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(
				http.MethodGet,
				"/resource"+test.query,
				nil,
			)
			for _, value := range test.authorization {
				request.Header.Add("Authorization", value)
			}
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{
					Name:  "access_token",
					Value: test.cookie,
				})
			}
			if test.initialActor != nil {
				request = request.WithContext(
					httptransport.WithActor(
						request.Context(),
						*test.initialActor,
					),
				)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf(
					"response = %d, %q",
					response.Code,
					response.Body.String(),
				)
			}
			if test.wantGuest && !actor.IsGuest() {
				t.Fatalf("actor = %#v", actor)
			}
			if test.wantUser > 0 {
				id, exists := actor.UserID()
				if !exists || id != test.wantUser {
					t.Fatalf("actor = %#v", actor)
				}
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
			if strings.Contains(
				response.Body.String(),
				"key service unavailable",
			) {
				t.Fatalf("internal verifier error leaked")
			}
			if tokens.verifiedValue != "" &&
				strings.Contains(
					response.Body.String(),
					tokens.verifiedValue,
				) {
				t.Fatal("access token leaked into error response")
			}
		})
	}
}

func actorPointer(actor security.Actor) *security.Actor {
	return &actor
}
