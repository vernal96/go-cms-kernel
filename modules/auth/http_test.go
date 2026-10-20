package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type sessionIssuer struct{ calls int }

func (i *sessionIssuer) IssueAccessToken(_ context.Context, actor security.Actor) (security.AccessToken, error) {
	i.calls++
	if actor.SessionVersion() != 3 {
		return security.AccessToken{}, security.ErrInvalidAccessToken
	}
	return security.AccessToken{Value: "cms-session", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func authHTTP(service *Service, issuer security.AccessTokenIssuer) http.Handler {
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := httptransport.WithActor(r.Context(), security.Guest())
			ctx = httptransport.WithAccessTokenIssuer(ctx, issuer)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.Post("/api/identity/{provider}/start", service.serveStart)
	router.Get("/api/identity/{provider}/callback", service.serveCallback)
	router.Get("/api/identity/me", service.serveCurrent)
	return router
}

func TestHTTPFlowKeepsSessionOutOfRedirectAndRejectsReplay(t *testing.T) {
	service, _, _ := newAuthService(t, 1, &flowMemory{values: make(map[string]Flow)})
	issuer := &sessionIssuer{}
	handler := authHTTP(service, issuer)
	request := httptest.NewRequest(http.MethodPost, "https://site.example/api/identity/demo/start",
		strings.NewReader(`{"link":false}`))
	request.Header.Set("Origin", "https://site.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var start struct {
		URL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	location, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	state := location.Query().Get("state")
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure ||
		cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Value == "" {
		t.Fatal("browser binding cookie lacks protection")
	}
	if strings.Contains(start.URL, cookies[0].Value) ||
		strings.Contains(start.URL, "code_verifier") || strings.Contains(start.URL, "access_token") {
		t.Fatal("redirect leaks an authentication secret")
	}
	callbackURL := "https://site.example/api/identity/demo/callback?" +
		url.Values{"state": {state}, "code": {"provider-code"}}.Encode()
	for attempt := 0; attempt < 2; attempt++ {
		request = httptest.NewRequest(http.MethodGet, callbackURL, nil)
		request.AddCookie(cookies[0])
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if attempt == 0 {
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"access_token":"cms-session"`) {
				t.Fatal(response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" ||
				response.Header().Get("Referrer-Policy") != "no-referrer" || response.Header().Get("Location") != "" {
				t.Fatal("callback response can leak or cache a session")
			}
		} else if response.Code != http.StatusBadRequest {
			t.Fatal("callback replay succeeded", response.Code)
		}
	}
	if issuer.calls != 1 {
		t.Fatal("issued multiple sessions")
	}
}

func TestHTTPRejectsCrossOriginLinkingMalformedAndMissingBrowser(t *testing.T) {
	service, accounts, _ := newAuthService(t, 1, &flowMemory{values: make(map[string]Flow)})
	handler := authHTTP(service, &sessionIssuer{})
	for _, scenario := range []struct {
		body, origin string
		status       int
	}{
		{`{}`, "https://attacker.example", http.StatusForbidden},
		{`{"link":true}`, "https://site.example", http.StatusUnauthorized},
		{`{"group_ids":[1]}`, "", http.StatusBadRequest},
		{`{} {}`, "", http.StatusBadRequest},
	} {
		request := httptest.NewRequest(http.MethodPost, "https://site.example/api/identity/demo/start",
			strings.NewReader(scenario.body))
		request.Header.Set("Origin", scenario.origin)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != scenario.status {
			t.Fatalf("%s: %d", scenario.body, response.Code)
		}
	}
	for _, query := range []string{"state=x&code=y", "state=x&state=y&code=z"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
			"https://site.example/api/identity/demo/callback?"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatal("invalid callback accepted")
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/identity/me", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatal("guest accessed current user")
	}
	if accounts.calls.Load() != 0 {
		t.Fatal("invalid request authenticated account")
	}
}

func TestHTTPProviderRejectionUsesUnauthorizedWithoutDiagnosticLeak(t *testing.T) {
	service, _, provider := newAuthService(t, 1, &flowMemory{values: make(map[string]Flow)})
	provider.err = errors.New("provider outage detail")
	for _, scenario := range []struct {
		err    error
		status int
	}{
		{ErrRejected, http.StatusUnauthorized},
		{provider.err, http.StatusInternalServerError},
	} {
		provider.err = scenario.err
		start, err := service.Begin(context.Background(), security.Guest(), "demo", false)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "https://site.example/api/identity/demo/callback?"+
			url.Values{"state": {start.State}, "code": {"code"}}.Encode(), nil)
		request.AddCookie(&http.Cookie{Name: cookieName(start.State), Value: start.BrowserSecret})
		response := httptest.NewRecorder()
		authHTTP(service, &sessionIssuer{}).ServeHTTP(response, request)
		if response.Code != scenario.status || strings.Contains(response.Body.String(), "outage detail") {
			t.Fatal("incorrect provider error response", response.Code, response.Body.String())
		}
	}
}

func TestHTTPStartChecksActualOriginIncludingPort(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		requestURL string
		origin     string
		status     int
	}{
		{"same port", "http://site.example:8080", "http://site.example:8080", http.StatusOK},
		{"no Origin", "http://site.example:8080", "", http.StatusOK},
		{"different port", "http://site.example:8080", "http://site.example:8081", http.StatusForbidden},
		{"missing nondefault port", "http://site.example:8080", "http://site.example", http.StatusForbidden},
		{"different host", "http://site.example:8080", "http://other.example:8080", http.StatusForbidden},
		{"unconfigured request host", "http://other.example:8080", "http://other.example:8080", http.StatusForbidden},
		{"different scheme", "http://site.example:8080", "https://site.example:8080", http.StatusForbidden},
		{"default port", "http://site.example:80", "http://site.example", http.StatusOK},
		{"null origin", "http://site.example:8080", "null", http.StatusForbidden},
		{"Origin with path", "http://site.example:8080", "http://site.example:8080/path", http.StatusForbidden},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service, _, _ := newAuthService(t, 1, &flowMemory{values: make(map[string]Flow)})
			service.origin = "http://site.example"
			request := httptest.NewRequest(http.MethodPost, scenario.requestURL+"/api/identity/demo/start",
				strings.NewReader(`{}`))
			request.Header.Set("Origin", scenario.origin)
			response := httptest.NewRecorder()
			authHTTP(service, &sessionIssuer{}).ServeHTTP(response, request)
			if response.Code != scenario.status {
				t.Fatalf("status = %d, want %d", response.Code, scenario.status)
			}
		})
	}
}

func TestHTTPFlowPreservesCallbackPortAndRejectsAnotherPort(t *testing.T) {
	service, _, provider := newAuthService(t, 1, &flowMemory{values: make(map[string]Flow)})
	service.origin = "http://site.example"
	issuer := &sessionIssuer{}
	handler := authHTTP(service, issuer)
	request := httptest.NewRequest(http.MethodPost, "http://site.example:8080/api/identity/demo/start",
		strings.NewReader(`{}`))
	request.Header.Set("Origin", "http://site.example:8080")
	request.Header.Set("X-Forwarded-Host", "attacker.example:9999")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal("start failed", response.Code, response.Body.String())
	}
	var start struct {
		URL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	location, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	callback := location.Query().Get("redirect_uri")
	if callback != "http://site.example:8080/api/identity/demo/callback" {
		t.Fatal("callback lost the request port or trusted a forwarded host", callback)
	}
	query := url.Values{"state": {location.Query().Get("state")}, "code": {"provider-code"}}.Encode()
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing browser cookie")
	}
	for _, authority := range []string{"site.example:8081", "site.example:8080"} {
		request = httptest.NewRequest(http.MethodGet, "http://"+authority+"/api/identity/demo/callback?"+query, nil)
		request.AddCookie(cookies[0])
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if authority == "site.example:8081" {
			if response.Code != http.StatusBadRequest || provider.calls.Load() != 0 {
				t.Fatal("another port consumed or authenticated the flow", response.Code)
			}
		} else if response.Code != http.StatusOK || issuer.calls != 1 {
			t.Fatal("original callback failed", response.Code, response.Body.String())
		}
	}
	if provider.exchange.RedirectURI != callback {
		t.Fatal("exchange callback differs from authorization callback")
	}
}
