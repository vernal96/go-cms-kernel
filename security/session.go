package security

import (
	"context"
	"time"
)

// Sessions are authoritative authentication state, never a cache.
type Session struct {
	TokenHash string
	UserID    UserID
	Version   int64
	ExpiresAt time.Time
}
type SessionStore interface {
	CreateSession(context.Context, Session) error
	ValidateSession(context.Context, string, UserID) error
	RevokeSession(context.Context, string) error
	PruneSessions(context.Context) error
}
type AccessTokenRevoker interface {
	RevokeAccessToken(context.Context, string) error
}

func AuthenticatedUser(id UserID, version int64) Actor {
	a := User(id)
	a.sessionVersion = version
	return a
}
func (a Actor) SessionVersion() int64 { return a.sessionVersion }
