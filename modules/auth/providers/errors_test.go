package providers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/vernal96/go-cms-kernel/modules/auth"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection unavailable")
}

func TestProviderHTTPErrorClassification(t *testing.T) {
	for _, scenario := range []struct {
		status   int
		body     string
		rejected bool
	}{
		{http.StatusBadRequest, `{"error":"invalid_grant","error_description":"sensitive-detail"}`, true},
		{http.StatusUnauthorized, `{"error":"invalid_client"}`, false},
		{http.StatusServiceUnavailable, `{"error":"invalid_grant"}`, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(scenario.status)
			_, _ = w.Write([]byte(scenario.body))
		}))
		_, err := getJSON(context.Background(), server.Client(), server.URL, "")
		server.Close()
		if err == nil || errors.Is(err, auth.ErrRejected) != scenario.rejected ||
			strings.Contains(err.Error(), "sensitive-detail") {
			t.Fatal("provider error classification or diagnostics leaked", err)
		}
	}
}

func TestProviderNetworkErrorsOmitURLCredentials(t *testing.T) {
	client := &http.Client{Transport: failingTransport{}}
	_, err := getJSON(context.Background(), client,
		"https://identity.example/profile?session_key=very-secret-token&code_verifier=secret-verifier", "")
	if err == nil || strings.Contains(err.Error(), "very-secret-token") ||
		strings.Contains(err.Error(), "secret-verifier") || errors.Is(err, auth.ErrRejected) {
		t.Fatal("network failure leaked credentials or became a user rejection", err)
	}
}

func TestGoogleClaimsRejectionKeepsSigningKeyOutageDistinct(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	claims := googleClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "https://accounts.google.com", Subject: "subject",
			Audience: jwt.ClaimStrings{"client"}, IssuedAt: jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		Nonce: "original-nonce",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	provider, err := NewGoogle(GoogleConfig{
		ClientID: "client", ClientSecret: "secret", KeysURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.verifyIDToken(context.Background(), signed, "original-nonce")
	if err == nil || errors.Is(err, auth.ErrRejected) {
		t.Fatal("JWKS outage was masked as user rejection", err)
	}
	provider.keys = map[string]*rsa.PublicKey{"test-key": &key.PublicKey}
	provider.keysUntil = time.Now().Add(time.Hour)
	_, err = provider.verifyIDToken(context.Background(), signed, "wrong-nonce")
	if !errors.Is(err, auth.ErrRejected) {
		t.Fatal("invalid nonce was not an authentication rejection", err)
	}
}
