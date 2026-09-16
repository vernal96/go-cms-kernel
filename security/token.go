package security

import (
	"context"
	"errors"
	"time"
)

var ErrInvalidAccessToken = errors.New("invalid access token")

type AccessToken struct {
	Value     string
	ExpiresAt time.Time
}

type AccessTokenIssuer interface {
	IssueAccessToken(context.Context, Actor) (AccessToken, error)
}

type AccessTokenVerifier interface {
	VerifyAccessToken(context.Context, string) (Actor, error)
}

type AccessTokens interface {
	AccessTokenIssuer
	AccessTokenVerifier
}
