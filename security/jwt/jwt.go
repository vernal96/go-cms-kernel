package jwt

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/vernal96/go-cms-kernel/security"
)

const (
	minimumSigningKeyBytes = 32
	maximumClockSkew       = 5 * time.Minute
)

var subjectPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

type Config struct {
	SigningKey string        `envconfig:"SIGNING_KEY"`
	Issuer     string        `envconfig:"ISSUER" default:"go-cms"`
	Audience   string        `envconfig:"AUDIENCE" default:"go-cms-api"`
	AccessTTL  time.Duration `envconfig:"ACCESS_TTL" default:"48h"`
	ClockSkew  time.Duration `envconfig:"CLOCK_SKEW" default:"30s"`
}

type Option func(*Service) error

type Service struct {
	sessions   security.SessionStore
	signingKey []byte
	issuer     string
	audience   string
	accessTTL  time.Duration
	clockSkew  time.Duration
	now        func() time.Time
}

type accessClaims struct {
	jwtlib.RegisteredClaims
}

func New(config Config, options ...Option) (*Service, error) {
	if len([]byte(config.SigningKey)) < minimumSigningKeyBytes {
		return nil, fmt.Errorf(
			"JWT signing key must contain at least %d bytes",
			minimumSigningKeyBytes,
		)
	}
	if config.Issuer == "" || strings.TrimSpace(config.Issuer) != config.Issuer {
		return nil, errors.New("JWT issuer is invalid")
	}
	if config.Audience == "" ||
		strings.TrimSpace(config.Audience) != config.Audience {
		return nil, errors.New("JWT audience is invalid")
	}
	if config.AccessTTL <= 0 {
		return nil, errors.New("JWT access TTL must be positive")
	}
	if config.ClockSkew < 0 || config.ClockSkew > maximumClockSkew {
		return nil, fmt.Errorf(
			"JWT clock skew must be between zero and %s",
			maximumClockSkew,
		)
	}
	if config.ClockSkew >= config.AccessTTL {
		return nil, errors.New("JWT clock skew must be shorter than access TTL")
	}

	service := &Service{
		signingKey: append([]byte(nil), []byte(config.SigningKey)...),
		issuer:     config.Issuer,
		audience:   config.Audience,
		accessTTL:  config.AccessTTL,
		clockSkew:  config.ClockSkew,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("JWT option at index %d is nil", index)
		}
		if err := option(service); err != nil {
			return nil, fmt.Errorf("apply JWT option at index %d: %w", index, err)
		}
	}
	return service, nil
}

func WithClock(now func() time.Time) Option {
	return func(service *Service) error {
		if now == nil {
			return errors.New("JWT clock is nil")
		}
		service.now = now
		return nil
	}
}

func (s *Service) IssueAccessToken(
	ctx context.Context,
	actor security.Actor,
) (security.AccessToken, error) {
	if err := validateContext(ctx); err != nil {
		return security.AccessToken{}, err
	}
	userID, exists := actor.UserID()
	if !exists {
		return security.AccessToken{}, security.ErrUnauthenticated
	}

	issuedAt := s.now().UTC()
	expiresAt := issuedAt.Add(s.accessTTL)
	claims := accessClaims{RegisteredClaims: jwtlib.RegisteredClaims{
		ID:        rand.Text(),
		Issuer:    s.issuer,
		Subject:   strconv.FormatInt(int64(userID), 10),
		Audience:  jwtlib.ClaimStrings{s.audience},
		ExpiresAt: jwtlib.NewNumericDate(expiresAt),
		NotBefore: jwtlib.NewNumericDate(issuedAt),
		IssuedAt:  jwtlib.NewNumericDate(issuedAt),
	}}
	token := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims)
	value, err := token.SignedString(s.signingKey)
	if err != nil {
		return security.AccessToken{}, fmt.Errorf("sign JWT access token: %w", err)
	}
	if s.sessions != nil {
		if err := s.sessions.CreateSession(ctx, security.Session{TokenHash: tokenHash(value), UserID: userID, Version: actor.SessionVersion(), ExpiresAt: expiresAt.Add(s.clockSkew)}); err != nil {
			return security.AccessToken{}, err
		}
	}
	return security.AccessToken{
		Value:     value,
		ExpiresAt: expiresAt,
	}, nil
}

func (s *Service) VerifyAccessToken(
	ctx context.Context,
	value string,
) (security.Actor, error) {
	if err := validateContext(ctx); err != nil {
		return security.Actor{}, err
	}
	if value == "" || strings.TrimSpace(value) != value {
		return security.Actor{}, security.ErrInvalidAccessToken
	}

	claims := &accessClaims{}
	token, err := jwtlib.ParseWithClaims(
		value,
		claims,
		func(token *jwtlib.Token) (any, error) {
			if token.Method != jwtlib.SigningMethodHS256 {
				return nil, security.ErrInvalidAccessToken
			}
			return s.signingKey, nil
		},
		jwtlib.WithValidMethods([]string{jwtlib.SigningMethodHS256.Alg()}),
		jwtlib.WithIssuer(s.issuer),
		jwtlib.WithAudience(s.audience),
		jwtlib.WithExpirationRequired(),
		jwtlib.WithIssuedAt(),
		jwtlib.WithLeeway(s.clockSkew),
		jwtlib.WithTimeFunc(func() time.Time {
			return s.now().UTC()
		}),
	)
	if err != nil || token == nil || !token.Valid {
		return security.Actor{}, security.ErrInvalidAccessToken
	}
	if claims.ExpiresAt == nil ||
		claims.NotBefore == nil ||
		claims.IssuedAt == nil ||
		!subjectPattern.MatchString(claims.Subject) {
		return security.Actor{}, security.ErrInvalidAccessToken
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || id <= 0 {
		return security.Actor{}, security.ErrInvalidAccessToken
	}
	if s.sessions != nil {
		if err := s.sessions.ValidateSession(ctx, tokenHash(value), security.UserID(id)); err != nil {
			return security.Actor{}, err
		}
	}
	return security.User(security.UserID(id)), nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("JWT context is nil")
	}
	return ctx.Err()
}

var _ security.AccessTokens = (*Service)(nil)
