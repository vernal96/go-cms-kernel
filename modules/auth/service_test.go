package auth

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
)

type flowMemory struct {
	mu     sync.Mutex
	values map[string]Flow
}

func (m *flowMemory) Create(_ context.Context, flow Flow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[flow.StateHash] = flow
	return nil
}

func (m *flowMemory) Consume(
	_ context.Context, state, browser string, siteID int64, provider, redirectURI string, now time.Time,
) (Flow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	flow, exists := m.values[state]
	if !exists || flow.BrowserHash != browser || flow.SiteID != siteID ||
		flow.Provider != provider || flow.RedirectURI != redirectURI || !now.Before(flow.ExpiresAt) {
		return Flow{}, ErrState
	}
	delete(m.values, state)
	return flow, nil
}

func (*flowMemory) Prune(context.Context, int64, time.Time) error { return nil }

type recordingAccounts struct {
	calls atomic.Int32
	input user.ExternalInput
}

func (*recordingAccounts) Current(_ context.Context, actor security.Actor) (user.User, error) {
	id, ok := actor.UserID()
	if !ok {
		return user.User{}, security.ErrUnauthenticated
	}
	return user.User{ID: id, SessionVersion: 3}, nil
}

func (a *recordingAccounts) AuthenticateExternal(_ context.Context, input user.ExternalInput) (user.User, error) {
	a.calls.Add(1)
	a.input = input
	return user.User{ID: 7, SessionVersion: 3}, nil
}

type testProvider struct {
	code      string
	namespace string
	identity  Identity
	exchange  ExchangeRequest
	err       error
	calls     atomic.Int32
}

func (p *testProvider) Code() string      { return p.code }
func (*testProvider) Label() string       { return "Test provider" }
func (p *testProvider) Namespace() string { return p.namespace }
func (*testProvider) AuthorizationURL(input AuthorizationRequest) (string, error) {
	values := url.Values{
		"state": {input.State}, "nonce": {input.Nonce}, "redirect_uri": {input.RedirectURI},
		"code_challenge": {input.CodeChallenge},
	}
	return "https://identity.example/authorize?" + values.Encode(), nil
}
func (p *testProvider) Exchange(_ context.Context, input ExchangeRequest) (Identity, error) {
	p.calls.Add(1)
	p.exchange = input
	return p.identity, p.err
}

func newAuthService(t *testing.T, siteID int64, flows *flowMemory) (*Service, *recordingAccounts, *testProvider) {
	t.Helper()
	accounts := &recordingAccounts{}
	provider := &testProvider{
		code: "demo", namespace: "https://identity.example/client-a",
		identity: Identity{Subject: "subject-a", Name: "Person", Email: "person@example.test"},
	}
	service, err := NewService(siteID, "https://site.example", flows, accounts,
		Config{AllowRegistration: true, Providers: []Provider{provider}})
	if err != nil {
		t.Fatal(err)
	}
	return service, accounts, provider
}

func TestFlowBindsBrowserSiteProviderAndConsumesOnce(t *testing.T) {
	flows := &flowMemory{values: make(map[string]Flow)}
	service, accounts, provider := newAuthService(t, 1, flows)
	otherSite, _, _ := newAuthService(t, 2, flows)
	start, err := service.Begin(context.Background(), security.Guest(), "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	location, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("state") != start.State ||
		len(location.Query().Get("code_challenge")) != 43 {
		t.Fatal("authorization request lacks state or PKCE")
	}
	for _, attempt := range []struct {
		service  *Service
		provider string
		browser  string
		err      error
	}{
		{otherSite, "demo", start.BrowserSecret, ErrState},
		{service, "demo", "other-browser", ErrState},
		{service, "missing", start.BrowserSecret, ErrProvider},
	} {
		_, err := attempt.service.Complete(context.Background(), CallbackInput{
			Provider: attempt.provider, State: start.State, BrowserSecret: attempt.browser, Code: "code",
		})
		if !errors.Is(err, attempt.err) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if provider.calls.Load() != 0 {
		t.Fatal("invalid state reached provider")
	}
	account, err := service.Complete(context.Background(), CallbackInput{
		Provider: "demo", State: start.State, BrowserSecret: start.BrowserSecret, Code: "code",
	})
	if err != nil || account.ID != 7 {
		t.Fatalf("complete: %+v %v", account, err)
	}
	if provider.exchange.State != start.State || provider.exchange.Nonce == "" ||
		len(provider.exchange.CodeVerifier) < 43 ||
		provider.exchange.RedirectURI != "https://site.example/api/identity/demo/callback" {
		t.Fatal("exchange lost PKCE, nonce, state or redirect binding")
	}
	if accounts.input.Email != "" {
		t.Fatal("unverified email was accepted")
	}
	_, err = service.Complete(context.Background(), CallbackInput{
		Provider: "demo", State: start.State, BrowserSecret: start.BrowserSecret, Code: "code",
	})
	if !errors.Is(err, ErrState) || provider.calls.Load() != 1 {
		t.Fatal("callback replay succeeded")
	}
}

func TestConcurrentCallbackOnlyOneAuthenticates(t *testing.T) {
	service, accounts, provider := newAuthService(t, 1, &flowMemory{values: make(map[string]Flow)})
	start, err := service.Begin(context.Background(), security.Guest(), "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var workers sync.WaitGroup
	for range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := service.Complete(context.Background(), CallbackInput{
				Provider: "demo", State: start.State, BrowserSecret: start.BrowserSecret, Code: "code",
			})
			if err == nil {
				successes.Add(1)
			}
		}()
	}
	workers.Wait()
	if successes.Load() != 1 || accounts.calls.Load() != 1 || provider.calls.Load() != 1 {
		t.Fatal("concurrent callback authenticated more than once")
	}
}

func TestLinkingRequiresAuthenticatedAccountAndCapturesVersion(t *testing.T) {
	service, accounts, provider := newAuthService(t, 1, &flowMemory{values: make(map[string]Flow)})
	_, err := service.Begin(context.Background(), security.Guest(), "demo", true)
	if !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("guest link: %v", err)
	}
	start, err := service.Begin(context.Background(), security.User(12), "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	provider.identity.EmailVerified = true
	_, err = service.Complete(context.Background(), CallbackInput{
		Provider: "demo", State: start.State, BrowserSecret: start.BrowserSecret, Code: "code",
	})
	if err != nil {
		t.Fatal(err)
	}
	if accounts.input.LinkUserID == nil || *accounts.input.LinkUserID != 12 ||
		accounts.input.LinkSessionVersion != 3 || accounts.input.Email != "person@example.test" {
		t.Fatal("link lost authenticated account, version or verified email")
	}
}

func TestFlowExpiryProviderChangeAndExchangeFailure(t *testing.T) {
	for _, scenario := range []string{"expiry", "provider-change", "exchange-error"} {
		t.Run(scenario, func(t *testing.T) {
			service, accounts, provider := newAuthService(t, 1, &flowMemory{values: make(map[string]Flow)})
			start, err := service.Begin(context.Background(), security.Guest(), "demo", false)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "expiry":
				service.now = func() time.Time { return start.ExpiresAt }
			case "provider-change":
				provider.namespace = "different-client"
			case "exchange-error":
				provider.err = errors.New("provider unavailable")
			}
			_, err = service.Complete(context.Background(), CallbackInput{
				Provider: "demo", State: start.State, BrowserSecret: start.BrowserSecret, Code: "code",
			})
			if err == nil || accounts.calls.Load() != 0 {
				t.Fatal("failed flow authenticated account")
			}
			if scenario == "exchange-error" {
				_, replayErr := service.Complete(context.Background(), CallbackInput{
					Provider: "demo", State: start.State, BrowserSecret: start.BrowserSecret, Code: "code",
				})
				if !errors.Is(replayErr, ErrState) || provider.calls.Load() != 1 {
					t.Fatal("failed exchange replayed a single-use code")
				}
			}
		})
	}
}

func TestProviderRegistryRejectsDuplicatesAndSeals(t *testing.T) {
	service, _, provider := newAuthService(t, 1, &flowMemory{values: make(map[string]Flow)})
	if err := service.RegisterProvider(provider); err == nil {
		t.Fatal("duplicate accepted")
	}
	var absent *testProvider
	if err := service.RegisterProvider(absent); err == nil {
		t.Fatal("typed nil accepted")
	}
	if err := service.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := service.RegisterProvider(&testProvider{code: "other", namespace: "other"}); err == nil {
		t.Fatal("registration after finalization accepted")
	}
}

func TestSameProfileSiteSelectionsAndDisableDuringCallback(t *testing.T) {
	flows := &flowMemory{values: make(map[string]Flow)}
	accounts := &recordingAccounts{}
	provider := &testProvider{code: "demo", namespace: "shared-client", identity: Identity{Subject: "subject"}}
	config := Config{Providers: []Provider{provider}, EnabledProvidersSetting: "auth_providers"}
	first, err := NewService(1, "https://first.test", flows, accounts, config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewService(2, "https://second.test", flows, accounts, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.selectProviders([]any{"demo"}); err != nil {
		t.Fatal(err)
	}
	if err := second.selectProviders(nil); err != nil {
		t.Fatal(err)
	}
	if err := first.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := second.Seal(); err != nil {
		t.Fatal(err)
	}
	if len(first.Providers()) != 1 || len(second.Providers()) != 0 {
		t.Fatal("site selection leaked")
	}
	if _, err := second.Begin(context.Background(), security.Guest(), "demo", false); !errors.Is(err, ErrProvider) {
		t.Fatal("disabled site started a login", err)
	}
	start, err := first.Begin(context.Background(), security.Guest(), "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	// A rebuilt runtime for the same site observes the new persistent selection.
	reloaded, err := NewService(1, "https://first.test", flows, accounts, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.selectProviders([]string{}); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Seal(); err != nil {
		t.Fatal(err)
	}
	_, err = reloaded.Complete(context.Background(), CallbackInput{
		Provider: "demo", State: start.State, BrowserSecret: start.BrowserSecret, Code: "code",
	})
	if !errors.Is(err, ErrProvider) || provider.calls.Load() != 0 {
		t.Fatal("disabled callback authenticated")
	}
	if err := first.selectProviders([]string{}); err == nil {
		t.Fatal("published runtime mutated")
	}
}

func TestSelectionRejectsUnknownProviderAtFinalization(t *testing.T) {
	service, _, _ := newAuthService(t, 1, &flowMemory{values: make(map[string]Flow)})
	if err := service.selectProviders([]string{"unknown"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Seal(); err == nil {
		t.Fatal("unknown selection finalized")
	}
	if err := service.selectProviders([]any{42}); err == nil {
		t.Fatal("non-string selection accepted")
	}
}
