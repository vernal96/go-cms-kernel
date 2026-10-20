package providers

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/auth"
)

const (
	mailruAuthorizeURL = "https://connect.mail.ru/oauth/authorize"
	mailruTokenURL     = "https://connect.mail.ru/oauth/token"
	mailruIdentityURL  = "https://www.appsmail.ru/platform/api"
)

// MailRuConfig contains the Mail.ru OAuth application and optional HTTP test seams.
type MailRuConfig struct {
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
	TokenURL     string
	IdentityURL  string
}

// MailRu implements Mail.ru's OAuth code flow and signed users.getInfo call.
type MailRu struct {
	cfg         config
	tokenURL    string
	identityURL string
}

func NewMailRu(cfg MailRuConfig) (*MailRu, error) {
	base := config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, HTTPClient: cfg.HTTPClient}
	if err := base.validate(); err != nil {
		return nil, err
	}
	if base.ClientSecret == "" {
		return nil, errors.New("Mail.ru client secret is required")
	}
	tokenURL := cfg.TokenURL
	if tokenURL == "" {
		tokenURL = mailruTokenURL
	}
	identityURL := cfg.IdentityURL
	if identityURL == "" {
		identityURL = mailruIdentityURL
	}
	if err := validateEndpoint(tokenURL); err != nil {
		return nil, err
	}
	if err := validateEndpoint(identityURL); err != nil {
		return nil, err
	}
	return &MailRu{cfg: base, tokenURL: tokenURL, identityURL: identityURL}, nil
}

func (p *MailRu) Code() string      { return "mailru" }
func (p *MailRu) Label() string     { return "Mail.ru" }
func (p *MailRu) Namespace() string { return "mailru:" + p.cfg.ClientID }

func (p *MailRu) AuthorizationURL(request auth.AuthorizationRequest) (string, error) {
	return authorizationURL(mailruAuthorizeURL, p.cfg.ClientID,
		request.RedirectURI, request.State, "", "",
		url.Values{"scope": {"userinfo"}})
}

func (p *MailRu) Exchange(
	ctx context.Context,
	request auth.ExchangeRequest,
) (auth.Identity, error) {
	if err := requiredExchange(request.Code, request.RedirectURI); err != nil {
		return auth.Identity{}, err
	}
	form := url.Values{
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {request.Code},
		"redirect_uri":  {request.RedirectURI},
	}
	tokenRaw, err := postForm(ctx, p.cfg.client(), p.tokenURL, form, "")
	if err != nil {
		return auth.Identity{}, err
	}
	var token struct {
		AccessToken string `json:"access_token"`
		VID         string `json:"x_mailru_vid"`
	}
	if err := decode(tokenRaw, &token); err != nil {
		return auth.Identity{}, err
	}
	if token.AccessToken == "" || token.VID == "" {
		return auth.Identity{}, errors.New("Mail.ru token response is invalid")
	}
	parameters := url.Values{
		"app_id":      {p.cfg.ClientID},
		"method":      {"users.getInfo"},
		"secure":      {"1"},
		"session_key": {token.AccessToken},
	}
	parameters.Set("sig", mailruSignature(parameters, p.cfg.ClientSecret))
	identityEndpoint, _ := url.Parse(p.identityURL)
	identityEndpoint.RawQuery = parameters.Encode()
	identityRaw, err := getJSON(ctx, p.cfg.client(), identityEndpoint.String(), "")
	if err != nil {
		return auth.Identity{}, err
	}
	var profiles []struct {
		UID       string `json:"uid"`
		Email     string `json:"email"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	if err := decode(identityRaw, &profiles); err != nil {
		return auth.Identity{}, err
	}
	if len(profiles) == 0 || (profiles[0].UID != "" && profiles[0].UID != token.VID) {
		return auth.Identity{}, fmt.Errorf("%w: Mail.ru subject mismatch", auth.ErrRejected)
	}
	name := strings.TrimSpace(profiles[0].FirstName + " " + profiles[0].LastName)
	identity := auth.Identity{Subject: token.VID, Email: profiles[0].Email, Name: name}
	if err := validateIdentity(identity.Subject, identity.Email); err != nil {
		return auth.Identity{}, err
	}
	return identity, nil
}

func mailruSignature(parameters url.Values, secret string) string {
	keys := make([]string, 0, len(parameters))
	for key := range parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var canonical strings.Builder
	for _, key := range keys {
		canonical.WriteString(key)
		canonical.WriteByte('=')
		canonical.WriteString(parameters.Get(key))
	}
	digest := md5.Sum([]byte(canonical.String() + secret))
	return hex.EncodeToString(digest[:])
}
