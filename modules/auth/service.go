package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

var (
	providerPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	ErrBusy         = errors.New("authentication capacity exhausted")
)

type Service struct {
	mu           sync.RWMutex
	sealed       bool
	providers    map[string]Provider
	enabled      map[string]struct{}
	siteID       int64
	origin       string
	registration bool
	ttl          time.Duration
	timeout      time.Duration
	flows        FlowRepository
	accounts     Accounts
	slots        chan struct{}
	now          func() time.Time
}

type ProviderInfo struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

type Start struct {
	State            string
	BrowserSecret    string
	AuthorizationURL string
	ExpiresAt        time.Time
}

type CallbackInput struct {
	Provider      string
	State         string
	BrowserSecret string
	Code          string
	DeviceID      string
	// Origin is the validated HTTP request origin. Empty uses the configured origin.
	Origin string
}

func NewService(
	siteID int64,
	origin string,
	flows FlowRepository,
	accounts Accounts,
	config Config,
) (*Service, error) {
	parsed, err := canonicalOrigin(origin)
	if err != nil {
		return nil, err
	}
	if siteID <= 0 || flows == nil || accounts == nil {
		return nil, errors.New("authentication dependencies are invalid")
	}
	config, err = normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	service := &Service{
		siteID: siteID, origin: parsed.String(), flows: flows, accounts: accounts,
		registration: config.AllowRegistration, ttl: config.FlowTTL,
		timeout: config.ExchangeTimeout, providers: make(map[string]Provider),
		slots: make(chan struct{}, config.MaxConcurrentExchanges), now: time.Now,
	}
	if config.EnabledProvidersSetting != "" {
		service.enabled = make(map[string]struct{})
	}
	for _, provider := range config.Providers {
		if err := service.RegisterProvider(provider); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (s *Service) selectProviders(value any) error {
	codes, err := selectedProviders(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return errors.New("authentication providers are sealed")
	}
	s.enabled = make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if !providerPattern.MatchString(code) {
			return errors.New("enabled provider code is invalid")
		}
		s.enabled[code] = struct{}{}
	}
	return nil
}

func (s *Service) RegisterProvider(provider Provider) error {
	if provider == nil || reflect.ValueOf(provider).Kind() == reflect.Pointer &&
		reflect.ValueOf(provider).IsNil() {
		return errors.New("authentication provider is nil")
	}
	if !providerPattern.MatchString(provider.Code()) ||
		strings.TrimSpace(provider.Label()) == "" || provider.Namespace() == "" ||
		len(provider.Namespace()) > 512 {
		return errors.New("authentication provider is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return errors.New("authentication providers are sealed")
	}
	if _, exists := s.providers[provider.Code()]; exists {
		return fmt.Errorf("authentication provider %q is already registered", provider.Code())
	}
	s.providers[provider.Code()] = provider
	return nil
}

func (s *Service) Seal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for code := range s.enabled {
		if _, exists := s.providers[code]; !exists {
			return fmt.Errorf("enabled authentication provider %q is not registered", code)
		}
	}
	s.sealed = true
	return nil
}

func (s *Service) Providers() []ProviderInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]ProviderInfo, 0, len(s.providers))
	for code, provider := range s.providers {
		if s.enabled != nil {
			if _, enabled := s.enabled[code]; !enabled {
				continue
			}
		}
		result = append(result, ProviderInfo{Code: code, Label: provider.Label()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	return result
}

func (s *Service) provider(code string) (Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	provider, exists := s.providers[code]
	if !exists {
		return nil, ErrProvider
	}
	if s.enabled != nil {
		if _, enabled := s.enabled[code]; !enabled {
			return nil, ErrProvider
		}
	}
	return provider, nil
}

func (s *Service) Begin(
	ctx context.Context,
	actor security.Actor,
	code string,
	link bool,
) (Start, error) {
	return s.begin(ctx, actor, code, link, s.origin)
}

func (s *Service) begin(
	ctx context.Context,
	actor security.Actor,
	code string,
	link bool,
	origin string,
) (Start, error) {
	if ctx == nil {
		return Start{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Start{}, err
	}
	provider, err := s.provider(code)
	if err != nil {
		return Start{}, err
	}
	state, browser, nonce, verifier := rand.Text(), rand.Text(), rand.Text(), randomVerifier()
	flow := Flow{
		StateHash: digest(state), BrowserHash: digest(browser), SiteID: s.siteID,
		Provider: code, Namespace: provider.Namespace(),
		RedirectURI: callbackURI(origin, code), Nonce: nonce, CodeVerifier: verifier,
		ExpiresAt: s.now().Add(s.ttl),
	}
	if link {
		account, err := s.accounts.Current(ctx, actor)
		if err != nil {
			return Start{}, err
		}
		flow.LinkUserID = &account.ID
		flow.LinkSessionVersion = account.SessionVersion
	}
	challenge := sha256.Sum256([]byte(verifier))
	location, err := provider.AuthorizationURL(AuthorizationRequest{
		State: state, Nonce: nonce, RedirectURI: flow.RedirectURI,
		CodeChallenge: base64.RawURLEncoding.EncodeToString(challenge[:]),
	})
	if err != nil {
		return Start{}, err
	}
	parsed, err := url.Parse(location)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return Start{}, ErrProvider
	}
	if err := s.flows.Create(ctx, flow); err != nil {
		return Start{}, err
	}
	return Start{
		State: state, BrowserSecret: browser, AuthorizationURL: location,
		ExpiresAt: flow.ExpiresAt,
	}, nil
}

func (s *Service) Complete(
	ctx context.Context,
	input CallbackInput,
) (user.User, error) {
	providerCode, state := input.Provider, input.State
	browser, code := input.BrowserSecret, input.Code
	if ctx == nil {
		return user.User{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return user.User{}, err
	}
	if state == "" || len(state) > 128 || browser == "" || len(browser) > 128 ||
		code == "" || len(code) > 4096 || len(input.DeviceID) > 512 {
		return user.User{}, ErrInvalid
	}
	provider, err := s.provider(providerCode)
	if err != nil {
		return user.User{}, err
	}
	origin := s.origin
	if input.Origin != "" {
		parsed, err := canonicalOrigin(input.Origin)
		if err != nil {
			return user.User{}, ErrState
		}
		origin, err = s.originForHost(parsed.Host)
		if err != nil || origin != parsed.String() {
			return user.User{}, ErrState
		}
	}
	redirectURI := callbackURI(origin, providerCode)
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return user.User{}, ErrBusy
	}
	flow, err := s.flows.Consume(
		ctx, digest(state), digest(browser), s.siteID, providerCode, redirectURI, s.now(),
	)
	if err != nil {
		return user.User{}, err
	}
	if flow.Namespace != provider.Namespace() || flow.RedirectURI != redirectURI {
		return user.User{}, ErrState
	}
	exchangeContext, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	identity, err := provider.Exchange(exchangeContext, ExchangeRequest{
		Code: code, State: state, Nonce: flow.Nonce, CodeVerifier: flow.CodeVerifier,
		RedirectURI: flow.RedirectURI, DeviceID: input.DeviceID,
	})
	if err != nil {
		// Authorization codes are single-use. Restart the login after any error;
		// automatic retries can replay a code already accepted by the provider.
		return user.User{}, fmt.Errorf("external authentication exchange: %w", err)
	}
	email := ""
	if identity.EmailVerified {
		email = identity.Email
	}
	return s.accounts.AuthenticateExternal(ctx, user.ExternalInput{
		Provider: providerCode, Namespace: provider.Namespace(), Subject: identity.Subject,
		Email: email, Name: identity.Name, AllowRegistration: s.registration,
		LinkUserID: flow.LinkUserID, LinkSessionVersion: flow.LinkSessionVersion,
	})
}

func callbackURI(origin, code string) string {
	return origin + httptransport.APIPrefix + "/identity/" + code + "/callback"
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func randomVerifier() string {
	// Two 128-bit Text values exceed PKCE's minimum 43-character verifier.
	return rand.Text() + rand.Text()
}
