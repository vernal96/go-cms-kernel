package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
	"github.com/vernal96/go-cms-kernel/security/jwt"
)

func TestSessionsRevokeCurrentAndInvalidateAllOnPasswordChange(t *testing.T) {
	connector, db, ctx := openOutboxIntegrationDatabase(t)
	_, _ = connector.Pool().Exec(ctx, "DELETE FROM core.users WHERE login='session-probe'")
	created, err := db.Users().Create(ctx, nil, user.Record{User: user.User{Login: "session-probe", Email: "session-probe@example.test", Name: "Probe", ColorScheme: user.ColorSchemeSystem, AccentColor: user.AccentColorBlue}, PasswordHash: "old-hash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Pool().Exec(ctx, "DELETE FROM core.users WHERE id=$1", created.ID)
	sessions := db.Users().(security.SessionStore)
	tokens, err := jwt.New(jwt.Config{SigningKey: strings.Repeat("s", 32), Issuer: "test", Audience: "test", AccessTTL: time.Hour}, jwt.WithSessions(sessions))
	if err != nil {
		t.Fatal(err)
	}
	actor := security.AuthenticatedUser(created.ID, created.SessionVersion)
	first, err := tokens.IssueAccessToken(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tokens.IssueAccessToken(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	if first.Value == second.Value {
		t.Fatal("distinct sessions share token")
	}
	if err := tokens.RevokeAccessToken(ctx, first.Value); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.VerifyAccessToken(ctx, first.Value); !errors.Is(err, security.ErrInvalidAccessToken) {
		t.Fatalf("logout failed: %v", err)
	}
	if _, err := tokens.VerifyAccessToken(ctx, second.Value); err != nil {
		t.Fatal("logout revoked another session", err)
	}
	if _, err := db.Users().ChangePassword(ctx, nil, created.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.VerifyAccessToken(ctx, second.Value); !errors.Is(err, security.ErrInvalidAccessToken) {
		t.Fatalf("password revocation failed: %v", err)
	}
	if _, err := tokens.IssueAccessToken(ctx, actor); !errors.Is(err, security.ErrInvalidAccessToken) {
		t.Fatalf("stale authentication issued a session: %v", err)
	}
	if _, err := db.Users().RecordLogin(ctx, created.ID, "old-hash", nil); !errors.Is(err, user.ErrInvalidCredentials) {
		t.Fatalf("password change lost login race: %v", err)
	}
}
