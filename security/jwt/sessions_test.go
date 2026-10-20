package jwt

import (
	"context"
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/security"
)

type testSessions struct {
	values      map[string]security.Session
	createErr   error
	validateErr error
}

func newTestSessions() *testSessions {
	return &testSessions{values: make(map[string]security.Session)}
}

func (s *testSessions) CreateSession(_ context.Context, session security.Session) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.values[session.TokenHash] = session
	return nil
}

func (s *testSessions) ValidateSession(_ context.Context, hash string, id security.UserID) error {
	if s.validateErr != nil {
		return s.validateErr
	}
	if session, ok := s.values[hash]; !ok || session.UserID != id {
		return security.ErrInvalidAccessToken
	}
	return nil
}

func (s *testSessions) RevokeSession(_ context.Context, hash string) error {
	delete(s.values, hash)
	return nil
}

func (s *testSessions) PruneSessions(context.Context) error { return nil }

func TestSessionStoreIsRequired(t *testing.T) {
	if _, err := New(testConfig()); err == nil {
		t.Fatal("accepted missing session store")
	}
	if _, err := New(testConfig(), WithSessions(nil)); err == nil {
		t.Fatal("accepted nil session store")
	}
}

func TestSessionsFailClosedAndRevokedTokensAreRejected(t *testing.T) {
	ctx := context.Background()
	store := newTestSessions()
	service, err := New(testConfig(), WithSessions(store))
	if err != nil {
		t.Fatal(err)
	}
	outage := errors.New("database unavailable")
	store.createErr = outage
	if token, err := service.IssueAccessToken(ctx, security.User(42)); !errors.Is(err, outage) || token.Value != "" {
		t.Fatalf("issuance during outage: token=%v err=%v", token.Value != "", err)
	}
	store.createErr = nil
	token, err := service.IssueAccessToken(ctx, security.AuthenticatedUser(42, 7))
	if err != nil {
		t.Fatal(err)
	}
	session := store.values[tokenHash(token.Value)]
	if session.Version != 7 || session.UserID != 42 || !session.ExpiresAt.Equal(token.ExpiresAt.Add(service.clockSkew)) {
		t.Fatalf("session metadata: %#v", session)
	}
	store.validateErr = outage
	if _, err := service.VerifyAccessToken(ctx, token.Value); !errors.Is(err, outage) {
		t.Fatalf("verification during outage: %v", err)
	}
	store.validateErr = nil
	if err := service.RevokeAccessToken(ctx, token.Value); err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifyAccessToken(ctx, token.Value); !errors.Is(err, security.ErrInvalidAccessToken) {
		t.Fatalf("revoked token verification: %v", err)
	}
	if err := service.RevokeAccessToken(nil, token.Value); err == nil {
		t.Fatal("nil revocation context accepted")
	}
}
