package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

func publicHTTP(service *Service) httptransport.Builder {
	return httptransport.BuilderFunc(func(context.Context) (httptransport.Contribution, error) {
		return httptransport.Contribution{Routes: func(registrar httptransport.Registrar) error {
			for _, route := range []httptransport.Route{
				{Name: "auth.providers", Method: http.MethodGet, Pattern: "/identity/providers",
					Handler: http.HandlerFunc(service.serveProviders)},
				{Name: "auth.current", Method: http.MethodGet, Pattern: "/identity/me",
					Handler: http.HandlerFunc(service.serveCurrent)},
				{Name: "auth.start", Method: http.MethodPost, Pattern: "/identity/{provider}/start",
					Handler: http.HandlerFunc(service.serveStart)},
				{Name: "auth.callback", Method: http.MethodGet, Pattern: "/identity/{provider}/callback",
					Handler: http.HandlerFunc(service.serveCallback)},
			} {
				if err := registrar.Route(route); err != nil {
					return err
				}
			}
			return nil
		}}, nil
	})
}

func (s *Service) serveCurrent(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	actor, ok := httptransport.ActorFromContext(request.Context())
	if !ok {
		actor = security.Guest()
	}
	account, err := s.accounts.Current(request.Context(), actor)
	if err != nil {
		writeAuthError(response, request, err)
		return
	}
	writeJSON(response, struct {
		ID    user.ID `json:"id"`
		Login string  `json:"login"`
		Email string  `json:"email,omitempty"`
		Name  string  `json:"name"`
	}{ID: account.ID, Login: account.Login, Email: account.Email, Name: account.Name})
}

func (s *Service) serveProviders(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, map[string]any{
		"providers": s.Providers(), "registration_enabled": s.registration,
	})
}

func (s *Service) serveStart(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Referrer-Policy", "no-referrer")
	origin, err := s.requestOrigin(request)
	if err != nil {
		writeAuthError(response, request, err)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, 1024)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Link bool `json:"link"`
	}
	var extra any
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&extra) != io.EOF {
		writeAuthError(response, request, ErrInvalid)
		return
	}
	actor, ok := httptransport.ActorFromContext(request.Context())
	if !ok {
		actor = security.Guest()
	}
	start, err := s.begin(request.Context(), actor, chi.URLParam(request, "provider"), input.Link, origin)
	if err != nil {
		writeAuthError(response, request, err)
		return
	}
	http.SetCookie(response, &http.Cookie{
		Name: cookieName(start.State), Value: start.BrowserSecret,
		Path: httptransport.APIPrefix + "/identity/", HttpOnly: true,
		Secure: strings.HasPrefix(s.origin, "https://"), SameSite: http.SameSiteLaxMode,
		Expires: start.ExpiresAt, MaxAge: int(s.ttl.Seconds()),
	})
	writeJSON(response, map[string]string{"authorization_url": start.AuthorizationURL})
}

func (s *Service) serveCallback(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	origin, err := s.requestOrigin(request)
	if err != nil {
		writeAuthError(response, request, err)
		return
	}
	if len(request.URL.RawQuery) > 8192 {
		writeAuthError(response, request, ErrInvalid)
		return
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(values["state"]) != 1 || len(values["code"]) != 1 ||
		len(values["error"]) != 0 || len(values["device_id"]) > 1 {
		writeAuthError(response, request, ErrInvalid)
		return
	}
	state := values.Get("state")
	if len(state) > 128 {
		writeAuthError(response, request, ErrInvalid)
		return
	}
	cookie, err := request.Cookie(cookieName(state))
	if err != nil {
		writeAuthError(response, request, ErrState)
		return
	}
	issuer, ok := httptransport.AccessTokenIssuerFromContext(request.Context())
	if !ok {
		writeAuthError(response, request, errors.New("authentication token issuer is unavailable"))
		return
	}
	account, err := s.Complete(request.Context(), CallbackInput{
		Provider: chi.URLParam(request, "provider"), State: state,
		BrowserSecret: cookie.Value, Code: values.Get("code"), DeviceID: values.Get("device_id"),
		Origin: origin,
	})
	http.SetCookie(response, &http.Cookie{
		Name: cookieName(state), Path: httptransport.APIPrefix + "/identity/",
		HttpOnly: true, Secure: strings.HasPrefix(s.origin, "https://"),
		SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	if err != nil {
		writeAuthError(response, request, err)
		return
	}
	token, err := issuer.IssueAccessToken(request.Context(),
		security.AuthenticatedUser(account.ID, account.SessionVersion))
	if err != nil {
		writeAuthError(response, request, err)
		return
	}
	writeJSON(response, map[string]string{
		"access_token": token.Value, "token_type": "Bearer",
		"expires_at": token.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func cookieName(state string) string { return "cms_identity_" + digest(state)[:24] }

func writeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(response).Encode(value)
}

func writeAuthError(response http.ResponseWriter, request *http.Request, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "authentication failed"
	switch {
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrState):
		status, code, message = http.StatusBadRequest, "invalid_authentication", "authentication request is invalid or expired"
	case errors.Is(err, ErrProvider):
		status, code, message = http.StatusNotFound, "provider_unavailable", "authentication provider is unavailable"
	case errors.Is(err, security.ErrUnauthenticated), errors.Is(err, user.ErrInvalidCredentials),
		errors.Is(err, ErrRejected):
		status, code, message = http.StatusUnauthorized, "unauthenticated", "authentication required"
	case errors.Is(err, security.ErrForbidden), errors.Is(err, user.ErrRegistrationDisabled):
		status, code, message = http.StatusForbidden, "forbidden", "authentication is not permitted"
	case errors.Is(err, user.ErrConflict):
		status, code = http.StatusConflict, "identity_conflict"
		message = "sign in to your existing account before linking this provider"
	case errors.Is(err, ErrBusy):
		status, code, message = http.StatusServiceUnavailable, "busy", "authentication capacity exhausted"
		response.Header().Set("Retry-After", "1")
	}
	if status >= http.StatusInternalServerError {
		httptransport.RecordError(request.Context(), err)
	}
	httptransport.WriteJSONError(response, status, code, message)
}
