package providers

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/vernal96/go-cms-kernel/modules/auth"
)

const (
	googleAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL     = "https://oauth2.googleapis.com/token"
	googleKeysURL      = "https://www.googleapis.com/oauth2/v3/certs"
)

// GoogleConfig contains the OAuth client and optional HTTP test seams.
type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
	TokenURL     string
	KeysURL      string
}

// Google implements Google's OpenID Connect authorization code flow.
type Google struct {
	cfg       config
	tokenURL  string
	keysURL   string
	keyMu     sync.RWMutex
	keys      map[string]*rsa.PublicKey
	keysUntil time.Time
}

func NewGoogle(cfg GoogleConfig) (*Google, error) {
	base := config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, HTTPClient: cfg.HTTPClient}
	if err := base.validate(); err != nil {
		return nil, err
	}
	if base.ClientSecret == "" {
		return nil, errors.New("Google client secret is required")
	}
	tokenURL := cfg.TokenURL
	if tokenURL == "" {
		tokenURL = googleTokenURL
	}
	keysURL := cfg.KeysURL
	if keysURL == "" {
		keysURL = googleKeysURL
	}
	if err := validateEndpoint(tokenURL); err != nil {
		return nil, err
	}
	if err := validateEndpoint(keysURL); err != nil {
		return nil, err
	}
	return &Google{cfg: base, tokenURL: tokenURL, keysURL: keysURL}, nil
}

func (p *Google) Code() string      { return "google" }
func (p *Google) Label() string     { return "Google" }
func (p *Google) Namespace() string { return "google:" + p.cfg.ClientID }

func (p *Google) AuthorizationURL(request auth.AuthorizationRequest) (string, error) {
	if request.Nonce == "" || request.CodeChallenge == "" {
		return "", errors.New("Google requires nonce and PKCE code challenge")
	}
	return authorizationURL(googleAuthorizeURL, p.cfg.ClientID,
		request.RedirectURI, request.State, request.Nonce,
		request.CodeChallenge, url.Values{"scope": {"openid email profile"}})
}

func (p *Google) Exchange(
	ctx context.Context,
	request auth.ExchangeRequest,
) (auth.Identity, error) {
	if err := requiredExchange(request.Code, request.RedirectURI); err != nil {
		return auth.Identity{}, err
	}
	if request.Nonce == "" || request.CodeVerifier == "" {
		return auth.Identity{}, errors.New("Google requires nonce and PKCE code verifier")
	}
	form := url.Values{
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"code":          {request.Code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {request.RedirectURI},
		"code_verifier": {request.CodeVerifier},
	}
	raw, err := postForm(ctx, p.cfg.client(), p.tokenURL, form, "")
	if err != nil {
		return auth.Identity{}, err
	}
	var token struct {
		IDToken string `json:"id_token"`
	}
	if err := decode(raw, &token); err != nil {
		return auth.Identity{}, err
	}
	if token.IDToken == "" {
		return auth.Identity{}, errInvalidResponse
	}
	claims, err := p.verifyIDToken(ctx, token.IDToken, request.Nonce)
	if err != nil {
		return auth.Identity{}, err
	}
	identity := auth.Identity{
		Subject:       claims.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
		Name:          claims.Name,
	}
	if err := validateIdentity(identity.Subject, identity.Email); err != nil {
		return auth.Identity{}, err
	}
	return identity, nil
}

type googleClaims struct {
	jwt.RegisteredClaims
	AuthorizedParty string `json:"azp"`
	Nonce           string `json:"nonce"`
	Email           string `json:"email"`
	EmailVerified   bool   `json:"email_verified"`
	Name            string `json:"name"`
}

func (p *Google) verifyIDToken(
	ctx context.Context,
	token string,
	nonce string,
) (*googleClaims, error) {
	var keyFailure error
	keyFunc := func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, errors.New("Google ID token uses an unsupported signing algorithm")
		}
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errInvalidResponse
		}
		keys, err := p.publicKeys(ctx, false)
		if err != nil {
			keyFailure = err
			return nil, err
		}
		key := keys[kid]
		if key == nil {
			keys, err = p.publicKeys(ctx, true)
			if err != nil {
				keyFailure = err
				return nil, err
			}
			key = keys[kid]
		}
		if key == nil {
			return nil, errors.New("Google signing key was not found")
		}
		return key, nil
	}
	claims := new(googleClaims)
	parsed, err := jwt.ParseWithClaims(token, claims, keyFunc,
		jwt.WithAudience(p.cfg.ClientID),
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(time.Minute))
	if keyFailure != nil {
		return nil, keyFailure
	}
	if err != nil || parsed == nil || !parsed.Valid || claims.ExpiresAt == nil ||
		claims.IssuedAt == nil || claims.Subject == "" || claims.Nonce != nonce ||
		(claims.Issuer != "https://accounts.google.com" && claims.Issuer != "accounts.google.com") {
		return nil, fmt.Errorf("%w: Google ID token validation failed", auth.ErrRejected)
	}
	if len(claims.Audience) > 1 && claims.AuthorizedParty != p.cfg.ClientID {
		return nil, fmt.Errorf("%w: Google ID token authorized party validation failed", auth.ErrRejected)
	}
	return claims, nil
}

func (p *Google) publicKeys(
	ctx context.Context,
	forceRefresh bool,
) (map[string]*rsa.PublicKey, error) {
	p.keyMu.RLock()
	if !forceRefresh && time.Now().Before(p.keysUntil) && len(p.keys) != 0 {
		keys := p.keys
		p.keyMu.RUnlock()
		return keys, nil
	}
	p.keyMu.RUnlock()

	raw, err := getJSON(ctx, p.cfg.client(), p.keysURL, "")
	if err != nil {
		return nil, err
	}
	var document struct {
		Keys []struct {
			KID string `json:"kid"`
			KTY string `json:"kty"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := decode(raw, &document); err != nil {
		return nil, err
	}
	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, jwk := range document.Keys {
		if jwk.KID == "" || jwk.KTY != "RSA" || (jwk.Use != "" && jwk.Use != "sig") ||
			(jwk.Alg != "" && jwk.Alg != "RS256") {
			continue
		}
		modulus, errN := base64.RawURLEncoding.DecodeString(jwk.N)
		exponent, errE := base64.RawURLEncoding.DecodeString(jwk.E)
		if errN != nil || errE != nil || len(modulus) == 0 || len(exponent) == 0 || len(exponent) > 4 {
			continue
		}
		e := new(big.Int).SetBytes(exponent)
		if !e.IsInt64() || e.Int64() < 3 || e.Int64()%2 == 0 {
			continue
		}
		keys[jwk.KID] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(e.Int64())}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no supported Google signing keys", errInvalidResponse)
	}
	p.keyMu.Lock()
	p.keys = keys
	p.keysUntil = time.Now().Add(time.Hour)
	p.keyMu.Unlock()
	return keys, nil
}
