package jwt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/vernal96/go-cms-kernel/security"
)

func WithSessions(store security.SessionStore) Option {
	return func(s *Service) error {
		if store == nil {
			return errors.New("session store is nil")
		}
		s.sessions = store
		return nil
	}
}
func tokenHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (s *Service) RevokeAccessToken(ctx context.Context, value string) error {
	if s.sessions == nil {
		return errors.New("session store is not configured")
	}
	return s.sessions.RevokeSession(ctx, tokenHash(value))
}
