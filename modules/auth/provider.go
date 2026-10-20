// Package auth implements optional, site-scoped external authentication.
package auth

import (
	"context"
	"errors"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
)

var (
	ErrInvalid  = errors.New("invalid authentication request")
	ErrState    = errors.New("authentication state is invalid or expired")
	ErrProvider = errors.New("authentication provider is unavailable")
	ErrRejected = errors.New("external authentication was rejected")
)

// Provider verifies the external authority before returning an identity.
// Namespace distinguishes subjects belonging to different issuers/client bindings.
// Implementations must support concurrent requests and honor context cancellation.
type Provider interface {
	Code() string
	Label() string
	Namespace() string
	AuthorizationURL(AuthorizationRequest) (string, error)
	Exchange(context.Context, ExchangeRequest) (Identity, error)
}

type AuthorizationRequest struct {
	State         string
	Nonce         string
	CodeChallenge string
	RedirectURI   string
}

type ExchangeRequest struct {
	Code         string
	State        string
	DeviceID     string
	Nonce        string
	CodeVerifier string
	RedirectURI  string
}

type Identity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// ProviderRegistrar is the runtime contribution target owned by Auth.
type ProviderRegistrar interface {
	RegisterProvider(Provider) error
}

// Flow is authoritative, short-lived state, not a disposable cache entry.
// BrowserHash contains no browser secret; CodeVerifier must never be logged.
type Flow struct {
	StateHash          string
	BrowserHash        string
	SiteID             int64
	Provider           string
	Namespace          string
	RedirectURI        string
	Nonce              string
	CodeVerifier       string
	LinkUserID         *user.ID
	LinkSessionVersion int64
	ExpiresAt          time.Time
}

type FlowRepository interface {
	Create(context.Context, Flow) error
	// Consume atomically removes a matching, unexpired flow. A mismatch must
	// not consume another browser's, site's or callback origin's flow.
	Consume(context.Context, string, string, int64, string, string, time.Time) (Flow, error)
	Prune(context.Context, int64, time.Time) error
}

type Accounts interface {
	Current(context.Context, security.Actor) (user.User, error)
	AuthenticateExternal(context.Context, user.ExternalInput) (user.User, error)
}
