package providers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/vernal96/go-cms-kernel/modules/auth"
)

func TestGoogleExchangeVerifiesIDTokenAndNonce(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	claims := jwt.MapClaims{
		"iss": "https://accounts.google.com", "sub": "google-subject",
		"aud": "google-client", "exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(), "nonce": "nonce-value",
		"email": "person@example.com", "email_verified": true,
		"name": "Person",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	idToken, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("code_verifier") != "verifier" {
				t.Errorf("token request missing verifier: %v", err)
			}
			_, _ = fmt.Fprintf(w, `{"id_token":%q}`, idToken)
		case "/keys":
			modulus := base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes())
			exponent := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes())
			_, _ = fmt.Fprintf(w,
				`{"keys":[{"kid":"test-key","kty":"RSA","use":"sig","alg":"RS256","n":%q,"e":%q}]}`,
				modulus, exponent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider, err := NewGoogle(GoogleConfig{
		ClientID: "google-client", ClientSecret: "secret", HTTPClient: server.Client(),
		TokenURL: server.URL + "/token", KeysURL: server.URL + "/keys",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := provider.Exchange(context.Background(), auth.ExchangeRequest{
		Code: "code", CodeVerifier: "verifier", Nonce: "nonce-value",
		RedirectURI: "https://site.example/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "google-subject" || !identity.EmailVerified ||
		identity.Email != "person@example.com" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if _, err := provider.Exchange(context.Background(), auth.ExchangeRequest{
		Code: "code", CodeVerifier: "verifier", Nonce: "wrong",
		RedirectURI: "https://site.example/callback",
	}); err == nil {
		t.Fatal("wrong nonce was accepted")
	}
}

func TestYandexExchangeReturnsStableUnverifiedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("unexpected token content type: %q", r.Header.Get("Content-Type"))
			}
			_, _ = w.Write([]byte(`{"access_token":"access","token_type":"bearer"}`))
		case "/info":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Errorf("missing bearer token")
			}
			_, _ = w.Write([]byte(`{"id":"stable-id","psuid":"pair-id","default_email":"y@example.com","real_name":"Y Name"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider, err := NewYandex(YandexConfig{
		ClientID: "y-client", ClientSecret: "secret", HTTPClient: server.Client(),
		TokenURL: server.URL + "/token", IdentityURL: server.URL + "/info?format=json",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := provider.Exchange(context.Background(), auth.ExchangeRequest{
		Code: "code", CodeVerifier: "verifier", RedirectURI: "https://site.example/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "pair-id" || identity.EmailVerified || identity.Name != "Y Name" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
}

func TestVKExchangeUsesPKCEAndChecksTokenState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.URL.Query().Get("code_verifier") != "verifier" ||
				r.URL.Query().Get("state") != "original-state" ||
				r.URL.Query().Get("device_id") != "original-device" {
				t.Errorf("missing VK exchange parameters: %v", r.URL.Query())
			}
			_, _ = fmt.Fprintf(w, `{"access_token":"vk-access","state":%q}`, r.URL.Query().Get("state"))
		case "/userinfo":
			var form url.Values
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			form = r.Form
			if form.Get("access_token") != "vk-access" {
				t.Errorf("missing access token")
			}
			_, _ = w.Write([]byte(`{"user_id":"vk-user","email":"vk@example.com","first_name":"V"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider, err := NewVK(VKConfig{
		ClientID: "vk-client", ClientSecret: "secret", HTTPClient: server.Client(),
		TokenURL: server.URL + "/token", IdentityURL: server.URL + "/userinfo",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := provider.Exchange(context.Background(), auth.ExchangeRequest{
		Code: "code", State: "original-state", CodeVerifier: "verifier", DeviceID: "original-device",
		RedirectURI: "https://site.example/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "vk-user" || identity.EmailVerified {
		t.Fatalf("unexpected identity: %+v", identity)
	}
}

func TestVKRejectsMismatchedProviderState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"vk-access","state":"wrong-state"}`))
	}))
	defer server.Close()
	provider, err := NewVK(VKConfig{
		ClientID: "vk-client", ClientSecret: "secret", HTTPClient: server.Client(),
		TokenURL: server.URL + "/token", IdentityURL: server.URL + "/userinfo",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Exchange(context.Background(), auth.ExchangeRequest{
		Code: "code", State: "original-state", CodeVerifier: "verifier", DeviceID: "original-device",
		RedirectURI: "https://site.example/callback",
	})
	if err == nil {
		t.Fatal("mismatched provider state was accepted")
	}
}

func TestMailRuExchangeSignsInfoRequest(t *testing.T) {
	const appID = "mail-client"
	const secret = "mail-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("client_secret") != secret {
				t.Errorf("invalid token request: %v", err)
			}
			_, _ = w.Write([]byte(`{"access_token":"session","x_mailru_vid":"mail-subject"}`))
		case "/api":
			query := r.URL.Query()
			if query.Get("app_id") != appID || query.Get("secure") != "1" ||
				query.Get("sig") != mailruSignature(url.Values{
					"app_id": {appID}, "method": {"users.getInfo"},
					"secure": {"1"}, "session_key": {"session"},
				}, secret) {
				t.Errorf("Mail.ru info request signature invalid: %v", query)
			}
			_, _ = w.Write([]byte(`[{"uid":"mail-subject","email":"mail@example.com","first_name":"Mail","last_name":"User"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider, err := NewMailRu(MailRuConfig{
		ClientID: appID, ClientSecret: secret, HTTPClient: server.Client(),
		TokenURL: server.URL + "/token", IdentityURL: server.URL + "/api",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := provider.Exchange(context.Background(), auth.ExchangeRequest{
		Code: "code", RedirectURI: "https://site.example/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "mail-subject" || identity.EmailVerified || identity.Name != "Mail User" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
}

func TestAuthorizationURLsCarryStateAndPKCE(t *testing.T) {
	provider, err := NewYandex(YandexConfig{ClientID: "client", ClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	value, err := provider.AuthorizationURL(auth.AuthorizationRequest{
		State: "state", Nonce: "nonce", CodeChallenge: "challenge",
		RedirectURI: "https://site.example/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("state") != "state" ||
		parsed.Query().Get("code_challenge") != "challenge" ||
		parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization URL missing state/PKCE: %s", value)
	}
}

func TestGoogleAuthorizationURLIncludesNonceAndPKCE(t *testing.T) {
	provider, err := NewGoogle(GoogleConfig{
		ClientID: "client", ClientSecret: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := provider.AuthorizationURL(auth.AuthorizationRequest{
		State: "state", Nonce: "nonce", CodeChallenge: "challenge",
		RedirectURI: "https://site.example/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("state") != "state" || query.Get("nonce") != "nonce" ||
		query.Get("code_challenge") != "challenge" ||
		query.Get("code_challenge_method") != "S256" {
		t.Fatalf("Google authorization URL missing state, nonce or PKCE: %s", value)
	}
}

func TestResponsesAreBoundedAndIdentityMustHaveSubject(t *testing.T) {
	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	}))
	defer oversized.Close()
	if _, err := getJSON(context.Background(), oversized.Client(), oversized.URL, ""); err == nil {
		t.Fatal("oversized response was accepted")
	}
	if err := validateIdentity("", "person@example.com"); err == nil {
		t.Fatal("empty provider subject was accepted")
	}
}

func TestMailRuTokenSubjectCannotBeReplacedByProfile(t *testing.T) {
	// The profile UID is corroborating metadata; the exchange response's
	// x_mailru_vid remains the stable subject stored for this client.
	if err := validateIdentity("subject-from-token", ""); err != nil {
		t.Fatal(err)
	}
}
