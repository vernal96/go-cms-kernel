package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/auth"
)

const (
	vkAuthorizeURL = "https://id.vk.ru/authorize"
	vkTokenURL     = "https://id.vk.ru/oauth2/auth"
	vkIdentityURL  = "https://id.vk.ru/oauth2/user_info"
)

// VKConfig contains the VK ID application and optional HTTP test seams.
type VKConfig struct {
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
	TokenURL     string
	IdentityURL  string
}

// VK implements VK ID's OAuth 2.1 authorization code flow.
type VK struct {
	cfg         config
	tokenURL    string
	identityURL string
}

func NewVK(cfg VKConfig) (*VK, error) {
	base := config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, HTTPClient: cfg.HTTPClient}
	if err := base.validate(); err != nil {
		return nil, err
	}
	tokenURL := cfg.TokenURL
	if tokenURL == "" {
		tokenURL = vkTokenURL
	}
	identityURL := cfg.IdentityURL
	if identityURL == "" {
		identityURL = vkIdentityURL
	}
	if err := validateEndpoint(tokenURL); err != nil {
		return nil, err
	}
	if err := validateEndpoint(identityURL); err != nil {
		return nil, err
	}
	return &VK{cfg: base, tokenURL: tokenURL, identityURL: identityURL}, nil
}

func (p *VK) Code() string      { return "vk" }
func (p *VK) Label() string     { return "VK ID" }
func (p *VK) Namespace() string { return "vk:" + p.cfg.ClientID }

func (p *VK) AuthorizationURL(request auth.AuthorizationRequest) (string, error) {
	if request.CodeChallenge == "" {
		return "", errors.New("VK ID requires a PKCE code challenge")
	}
	extra := url.Values{"scope": {"email"}}
	return authorizationURL(vkAuthorizeURL, p.cfg.ClientID,
		request.RedirectURI, request.State, "",
		request.CodeChallenge, extra)
}

func (p *VK) Exchange(ctx context.Context, request auth.ExchangeRequest) (auth.Identity, error) {
	if err := requiredExchange(request.Code, request.RedirectURI); err != nil {
		return auth.Identity{}, err
	}
	if request.CodeVerifier == "" {
		return auth.Identity{}, errors.New("VK ID requires a PKCE code verifier")
	}
	if request.State == "" {
		return auth.Identity{}, errors.New("VK ID requires the original OAuth state")
	}
	if request.DeviceID == "" {
		return auth.Identity{}, fmt.Errorf("%w: VK ID device ID is missing", auth.ErrRejected)
	}
	endpoint, _ := url.Parse(p.tokenURL)
	query := endpoint.Query()
	query.Set("grant_type", "authorization_code")
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("client_id", p.cfg.ClientID)
	query.Set("code_verifier", request.CodeVerifier)
	query.Set("state", request.State)
	query.Set("device_id", request.DeviceID)
	endpoint.RawQuery = query.Encode()
	tokenRaw, err := postForm(ctx, p.cfg.client(), endpoint.String(),
		url.Values{"code": {request.Code}}, "")
	if err != nil {
		return auth.Identity{}, err
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		State       string `json:"state"`
	}
	if err := decode(tokenRaw, &token); err != nil {
		return auth.Identity{}, err
	}
	if token.AccessToken == "" || token.State != request.State ||
		(token.TokenType != "" && !strings.EqualFold(token.TokenType, "bearer")) {
		return auth.Identity{}, fmt.Errorf("%w: VK ID token response is invalid", auth.ErrRejected)
	}
	identityURL, _ := url.Parse(p.identityURL)
	query = identityURL.Query()
	query.Set("client_id", p.cfg.ClientID)
	identityURL.RawQuery = query.Encode()
	identityRaw, err := postForm(ctx, p.cfg.client(), identityURL.String(),
		url.Values{"access_token": {token.AccessToken}}, "")
	if err != nil {
		return auth.Identity{}, err
	}
	var profile struct {
		Subject   string `json:"user_id"`
		Sub       string `json:"sub"`
		Email     string `json:"email"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Name      string `json:"name"`
	}
	if err := decode(identityRaw, &profile); err != nil {
		return auth.Identity{}, err
	}
	if profile.Subject == "" {
		profile.Subject = profile.Sub
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = strings.TrimSpace(profile.FirstName + " " + profile.LastName)
	}
	identity := auth.Identity{
		Subject: profile.Subject,
		Email:   profile.Email,
		Name:    name,
	}
	if err := validateIdentity(identity.Subject, identity.Email); err != nil {
		return auth.Identity{}, err
	}
	return identity, nil
}
