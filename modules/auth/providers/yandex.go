package providers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/auth"
)

const (
	yandexAuthorizeURL = "https://oauth.yandex.ru/authorize"
	yandexTokenURL     = "https://oauth.yandex.ru/token"
	yandexIdentityURL  = "https://login.yandex.ru/info?format=json"
)

// YandexConfig contains the OAuth client and optional HTTP test seams.
type YandexConfig struct {
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
	TokenURL     string
	IdentityURL  string
}

// Yandex implements Yandex's OAuth authorization code flow.
type Yandex struct {
	cfg         config
	tokenURL    string
	identityURL string
}

func NewYandex(cfg YandexConfig) (*Yandex, error) {
	base := config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, HTTPClient: cfg.HTTPClient}
	if err := base.validate(); err != nil {
		return nil, err
	}
	if base.ClientSecret == "" {
		return nil, errors.New("Yandex client secret is required")
	}
	tokenURL := cfg.TokenURL
	if tokenURL == "" {
		tokenURL = yandexTokenURL
	}
	identityURL := cfg.IdentityURL
	if identityURL == "" {
		identityURL = yandexIdentityURL
	}
	if err := validateEndpoint(tokenURL); err != nil {
		return nil, err
	}
	if err := validateEndpoint(identityURL); err != nil {
		return nil, err
	}
	return &Yandex{cfg: base, tokenURL: tokenURL, identityURL: identityURL}, nil
}

func (p *Yandex) Code() string      { return "yandex" }
func (p *Yandex) Label() string     { return "Yandex" }
func (p *Yandex) Namespace() string { return "yandex:" + p.cfg.ClientID }

func (p *Yandex) AuthorizationURL(request auth.AuthorizationRequest) (string, error) {
	if request.CodeChallenge == "" {
		return "", errors.New("Yandex requires a PKCE code challenge")
	}
	return authorizationURL(yandexAuthorizeURL, p.cfg.ClientID,
		request.RedirectURI, request.State, "",
		request.CodeChallenge, url.Values{"scope": {"login:email login:info"}})
}

func (p *Yandex) Exchange(
	ctx context.Context,
	request auth.ExchangeRequest,
) (auth.Identity, error) {
	if err := requiredExchange(request.Code, request.RedirectURI); err != nil {
		return auth.Identity{}, err
	}
	if request.CodeVerifier == "" {
		return auth.Identity{}, errors.New("Yandex requires a PKCE code verifier")
	}
	form := url.Values{
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {request.Code},
		"redirect_uri":  {request.RedirectURI},
	}
	if request.CodeVerifier != "" {
		form.Set("code_verifier", request.CodeVerifier)
	}
	tokenRaw, err := postForm(ctx, p.cfg.client(), p.tokenURL, form, "")
	if err != nil {
		return auth.Identity{}, err
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := decode(tokenRaw, &token); err != nil {
		return auth.Identity{}, err
	}
	if token.AccessToken == "" ||
		(token.TokenType != "" && !strings.EqualFold(token.TokenType, "bearer")) {
		return auth.Identity{}, errors.New("Yandex token response is invalid")
	}
	identityRaw, err := getJSON(ctx, p.cfg.client(), p.identityURL, token.AccessToken)
	if err != nil {
		return auth.Identity{}, err
	}
	var profile struct {
		ID           string   `json:"id"`
		PSUID        string   `json:"psuid"`
		DefaultEmail string   `json:"default_email"`
		Emails       []string `json:"emails"`
		Name         string   `json:"real_name"`
		FirstName    string   `json:"first_name"`
		LastName     string   `json:"last_name"`
	}
	if err := decode(identityRaw, &profile); err != nil {
		return auth.Identity{}, err
	}
	subject := profile.PSUID
	if subject == "" {
		subject = profile.ID
	}
	email := profile.DefaultEmail
	if email == "" && len(profile.Emails) > 0 {
		email = profile.Emails[0]
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = strings.TrimSpace(profile.FirstName + " " + profile.LastName)
	}
	identity := auth.Identity{Subject: subject, Email: email, Name: name}
	if err := validateIdentity(identity.Subject, identity.Email); err != nil {
		return auth.Identity{}, err
	}
	return identity, nil
}
