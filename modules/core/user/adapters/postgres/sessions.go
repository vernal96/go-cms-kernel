package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) CreateSession(ctx context.Context, s security.Session) error {
	// Lock the user while inserting so password change cannot leave a session
	// issued against an obsolete credential version.
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var version int64
	err = tx.QueryRow(ctx, "SELECT session_version FROM core.users WHERE id=$1 AND blocked_at IS NULL FOR UPDATE", s.UserID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && version != s.Version {
		return security.ErrInvalidAccessToken
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO core.auth_sessions(token_hash,user_id,session_version,expires_at) VALUES($1,$2,$3,$4)", s.TokenHash, s.UserID, s.Version, s.ExpiresAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) ValidateSession(ctx context.Context, hash string, id security.UserID) error {
	var valid bool
	err := r.connector.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core.auth_sessions s JOIN core.users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.user_id=$2 AND s.session_version=u.session_version AND s.expires_at>now() AND u.blocked_at IS NULL)`, hash, id).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return security.ErrInvalidAccessToken
	}
	return nil
}
func (r *Repository) RevokeSession(ctx context.Context, hash string) error {
	_, err := r.connector.Pool().Exec(ctx, "DELETE FROM core.auth_sessions WHERE token_hash=$1", hash)
	return err
}
func (r *Repository) PruneSessions(ctx context.Context) error {
	_, err := r.connector.Pool().Exec(ctx, "DELETE FROM core.auth_sessions WHERE expires_at<=now()")
	return err
}
