package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
)

const externalUserColumns = `id, login, email, password_hash, session_version, name,
last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
last_login_at, created_at, updated_at, blocked_at, created_by, updated_by, blocked_by`

func (r *Repository) AuthenticateExternal(
	ctx context.Context,
	input user.ExternalInput,
	candidate user.Record,
) (user.Record, error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return user.Record{}, err
	}
	defer tx.Rollback(ctx)
	// Serialize first login and linking for this identity across replicas.
	identityKey := input.Provider + ":" + input.Namespace + ":" + input.Subject
	_, err = tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, identityKey,
	)
	if err != nil {
		return user.Record{}, err
	}
	var accountID user.ID
	err = tx.QueryRow(ctx, `SELECT user_id FROM core.external_identities
WHERE provider=$1 AND namespace=$2 AND subject=$3`,
		input.Provider, input.Namespace, input.Subject,
	).Scan(&accountID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return user.Record{}, err
	}
	exists := err == nil
	if input.LinkUserID != nil {
		if exists && accountID != *input.LinkUserID {
			return user.Record{}, user.ErrConflict
		}
		accountID = *input.LinkUserID
	} else if !exists && !input.AllowRegistration {
		return user.Record{}, user.ErrRegistrationDisabled
	}
	var record user.Record
	if accountID != 0 {
		record, err = scanRecord(tx.QueryRow(ctx,
			`SELECT `+externalUserColumns+` FROM core.users WHERE id=$1 FOR UPDATE`, accountID,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return user.Record{}, security.ErrUnauthenticated
		}
		if err != nil {
			return user.Record{}, err
		}
		if record.BlockedAt != nil || input.LinkUserID != nil &&
			record.SessionVersion != input.LinkSessionVersion {
			return user.Record{}, security.ErrUnauthenticated
		}
	} else {
		candidate, groups, prepareErr := user.PrepareMutation(ctx, nil, candidate, nil)
		if prepareErr != nil {
			return user.Record{}, prepareErr
		}
		// Public provider registration never grants administrative membership.
		if len(groups) != 0 || candidate.PasswordHash != "" || candidate.AvatarMediaID != nil {
			return user.Record{}, user.ErrInvalidReference
		}
		record, err = scanRecord(tx.QueryRow(ctx, `INSERT INTO core.users
(login,email,name,color_scheme,accent_color)
VALUES($1,NULLIF($2,''),$3,$4,$5) RETURNING `+externalUserColumns,
			candidate.Login, candidate.Email, candidate.Name,
			candidate.ColorScheme, candidate.AccentColor,
		))
		if err != nil {
			// A colliding email is a conflict, never proof of account ownership.
			return user.Record{}, translateError(err)
		}
		if err := r.appendMutation(ctx, tx, nil, record.ID); err != nil {
			return user.Record{}, err
		}
	}
	if !exists {
		_, err = tx.Exec(ctx, `INSERT INTO core.external_identities
(provider,namespace,subject,user_id) VALUES($1,$2,$3,$4)`,
			input.Provider, input.Namespace, input.Subject, record.ID,
		)
		if err != nil {
			return user.Record{}, translateError(err)
		}
	}
	record, err = scanRecord(tx.QueryRow(ctx, `UPDATE core.users
SET last_login_at=clock_timestamp() WHERE id=$1 RETURNING `+externalUserColumns, record.ID))
	if err != nil {
		return user.Record{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return user.Record{}, err
	}
	return record, nil
}

var _ user.ExternalIdentityRepository = (*Repository)(nil)
