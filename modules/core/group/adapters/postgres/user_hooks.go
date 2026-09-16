package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	userpostgres "github.com/vernal96/go-cms-kernel/modules/core/user/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) membershipTransaction(ctx context.Context, execute func(pgx.Tx) error) error {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "LOCK TABLE core.users IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE core.user_groups IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return err
	}
	if err := execute(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) changeUserGroups(ctx context.Context, actorID *security.UserID, userID security.UserID, change func([]group.ID) ([]group.ID, error)) error {
	return r.membershipTransaction(ctx, func(tx pgx.Tx) error {
		_, before, err := userpostgres.ReadHookState(ctx, tx, userID)
		if err != nil {
			return err
		}
		next, err := change(before.GroupIDs)
		if err != nil {
			return err
		}
		return r.prepareMembership(ctx, tx, actorID, userID, next)
	})
}

func (r *Repository) prepareMembership(ctx context.Context, tx pgx.Tx, actorID *security.UserID, userID security.UserID, groups []group.ID) error {
	current, before, err := userpostgres.ReadHookState(ctx, tx, userID)
	if err != nil {
		return err
	}
	_, next, err := user.PrepareMutation(ctx, &before, current, groups)
	if err != nil {
		return err
	}
	if err := r.replaceUserGroupsTx(ctx, tx, actorID, userID, next); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE core.users SET updated_at=now(),updated_by=$2 WHERE id=$1`, userID, actorID); err != nil {
		return err
	}
	return userpostgres.AppendMutation(ctx, tx, "core:"+string(r.connector.Code()), &before, userID)
}
