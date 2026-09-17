package jwt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/vernal96/go-cms-kernel/security"
)

var testNow = time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

func testConfig() Config {
	return Config{
		SigningKey: strings.Repeat("k", 32),
		Issuer:     "cms.test",
		Audience:   "cms-api.test",
		AccessTTL:  15 * time.Minute,
		ClockSkew:  30 * time.Second,
	}
}

func TestServiceIssuesAndVerifiesAccessToken(t *testing.T) {
	service, err := New(testConfig(), WithClock(func() time.Time {
		return testNow
	}))
	if err != nil {
		t.Fatal(err)
	}

	accessToken, err := service.IssueAccessToken(
		context.Background(),
		security.User(42),
	)
	if err != nil {
		t.Fatal(err)
	}
	if accessToken.Value == "" ||
		!accessToken.ExpiresAt.Equal(testNow.Add(15*time.Minute)) {
		t.Fatalf("access token = %#v", accessToken)
	}

	unverified := jwtlib.MapClaims{}
	if _, _, err := jwtlib.NewParser().ParseUnverified(
		accessToken.Value,
		unverified,
	); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"sub",
		"iss",
		"aud",
		"iat",
		"nbf",
		"exp",
	} {
		if _, exists := unverified[required]; !exists {
			t.Fatalf("required claim %q is missing: %#v", required, unverified)
		}
	}
	if len(unverified) != 6 {
		t.Fatalf("unexpected access token claims: %#v", unverified)
	}

	actor, err := service.VerifyAccessToken(
		context.Background(),
		accessToken.Value,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, exists := actor.UserID()
	if !exists || id != 42 {
		t.Fatalf("verified actor = %#v", actor)
	}
}

func TestServiceRejectsInvalidClaimsSignaturesAndMethods(t *testing.T) {
	config := testConfig()
	service, err := New(config, WithClock(func() time.Time {
		return testNow
	}))
	if err != nil {
		t.Fatal(err)
	}

	baseClaims := func() jwtlib.RegisteredClaims {
		return jwtlib.RegisteredClaims{
			Issuer:    config.Issuer,
			Subject:   "42",
			Audience:  jwtlib.ClaimStrings{config.Audience},
			ExpiresAt: jwtlib.NewNumericDate(testNow.Add(time.Minute)),
			NotBefore: jwtlib.NewNumericDate(testNow),
			IssuedAt:  jwtlib.NewNumericDate(testNow),
		}
	}

	tests := []struct {
		name   string
		claims func() jwtlib.RegisteredClaims
		method jwtlib.SigningMethod
		key    any
	}{
		{
			name: "expired",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.ExpiresAt = jwtlib.NewNumericDate(
					testNow.Add(-time.Minute),
				)
				return claims
			},
		},
		{
			name: "not active",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.NotBefore = jwtlib.NewNumericDate(
					testNow.Add(time.Minute),
				)
				return claims
			},
		},
		{
			name: "issued in future",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.IssuedAt = jwtlib.NewNumericDate(
					testNow.Add(time.Minute),
				)
				return claims
			},
		},
		{
			name: "wrong issuer",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.Issuer = "other"
				return claims
			},
		},
		{
			name: "missing issuer",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.Issuer = ""
				return claims
			},
		},
		{
			name: "wrong audience",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.Audience = jwtlib.ClaimStrings{"other"}
				return claims
			},
		},
		{
			name: "missing audience",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.Audience = nil
				return claims
			},
		},
		{
			name: "missing expiration",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.ExpiresAt = nil
				return claims
			},
		},
		{
			name: "missing not before",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.NotBefore = nil
				return claims
			},
		},
		{
			name: "missing issued at",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.IssuedAt = nil
				return claims
			},
		},
		{
			name: "missing subject",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.Subject = ""
				return claims
			},
		},
		{
			name: "invalid subject",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.Subject = "not-a-user"
				return claims
			},
		},
		{
			name: "zero subject",
			claims: func() jwtlib.RegisteredClaims {
				claims := baseClaims()
				claims.Subject = "0"
				return claims
			},
		},
		{
			name:   "wrong signing key",
			claims: baseClaims,
			key:    []byte(strings.Repeat("x", 32)),
		},
		{
			name:   "wrong algorithm",
			claims: baseClaims,
			method: jwtlib.SigningMethodHS384,
		},
		{
			name:   "none algorithm",
			claims: baseClaims,
			method: jwtlib.SigningMethodNone,
			key:    jwtlib.UnsafeAllowNoneSignatureType,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			method := test.method
			if method == nil {
				method = jwtlib.SigningMethodHS256
			}
			key := test.key
			if key == nil {
				key = []byte(config.SigningKey)
			}
			token := jwtlib.NewWithClaims(method, test.claims())
			value, err := token.SignedString(key)
			if err != nil {
				t.Fatal(err)
			}

			_, err = service.VerifyAccessToken(context.Background(), value)
			if !errors.Is(err, security.ErrInvalidAccessToken) {
				t.Fatalf("verify error = %v", err)
			}
		})
	}
}

func TestServiceValidatesConfigurationActorAndContext(t *testing.T) {
	valid := testConfig()
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "missing key",
			mutate: func(config *Config) {
				config.SigningKey = ""
			},
		},
		{
			name: "short key",
			mutate: func(config *Config) {
				config.SigningKey = "secret"
			},
		},
		{
			name: "empty issuer",
			mutate: func(config *Config) {
				config.Issuer = ""
			},
		},
		{
			name: "invalid audience",
			mutate: func(config *Config) {
				config.Audience = " cms "
			},
		},
		{
			name: "zero TTL",
			mutate: func(config *Config) {
				config.AccessTTL = 0
			},
		},
		{
			name: "negative skew",
			mutate: func(config *Config) {
				config.ClockSkew = -time.Second
			},
		},
		{
			name: "excessive skew",
			mutate: func(config *Config) {
				config.ClockSkew = 6 * time.Minute
			},
		},
		{
			name: "skew exceeds TTL",
			mutate: func(config *Config) {
				config.AccessTTL = 10 * time.Second
				config.ClockSkew = 10 * time.Second
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			_, err := New(config)
			if err == nil {
				t.Fatal("expected configuration error")
			}
			if config.SigningKey != "" &&
				strings.Contains(err.Error(), config.SigningKey) {
				t.Fatalf("configuration error leaked signing key: %v", err)
			}
		})
	}

	service, err := New(valid, WithClock(func() time.Time { return testNow }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.IssueAccessToken(
		context.Background(),
		security.Guest(),
	); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("guest issuance error = %v", err)
	}
	if _, err := service.VerifyAccessToken(
		nil,
		"token",
	); err == nil || errors.Is(err, security.ErrInvalidAccessToken) {
		t.Fatalf("nil context error = %v", err)
	}
	sensitiveValue := "sensitive.jwt.value"
	if _, err := service.VerifyAccessToken(
		context.Background(),
		sensitiveValue,
	); !errors.Is(err, security.ErrInvalidAccessToken) ||
		strings.Contains(err.Error(), sensitiveValue) {
		t.Fatalf("malformed token error was not safely normalized: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.IssueAccessToken(
		ctx,
		security.User(1),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled issuance error = %v", err)
	}
}
