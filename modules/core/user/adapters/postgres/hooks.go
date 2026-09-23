package postgres

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/entityhooks/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
)

// ReadHookState also serves membership mutations owned by the group adapter.
func ReadHookState(ctx context.Context, tx pgx.Tx, id user.ID) (user.Record, user.EventState, error) {
	record, err := scanRecord(tx.QueryRow(ctx, `SELECT id,login,email,password_hash,session_version,name,last_name,middle_name,phone,avatar_media_id,color_scheme,accent_color,last_login_at,created_at,updated_at,blocked_at,created_by,updated_by,blocked_by FROM core.users WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return user.Record{}, user.EventState{}, user.ErrNotFound
	}
	if err != nil {
		return user.Record{}, user.EventState{}, translateError(err)
	}
	rows, err := tx.Query(ctx, `SELECT group_id FROM core.user_groups WHERE user_id=$1 ORDER BY group_id`, id)
	if err != nil {
		return user.Record{}, user.EventState{}, err
	}
	var ids []group.ID
	for rows.Next() {
		var id group.ID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return user.Record{}, user.EventState{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return user.Record{}, user.EventState{}, err
	}
	return record, user.EventSnapshot(record.User, ids), nil
}

func AppendMutation(ctx context.Context, tx pgx.Tx, sourceName string, before *user.EventState, id user.ID) error {
	_, after, err := ReadHookState(ctx, tx, id)
	if err != nil {
		return err
	}
	event, targets, err := user.MutationEvent(ctx, before, after)
	if err != nil {
		return err
	}
	return postgres.NewSource(nil, sourceName, "core").Append(ctx, tx, event, targets, []byte(strconv.FormatInt(int64(id), 10)))
}

func (r *Repository) appendMutation(ctx context.Context, tx pgx.Tx, before *user.EventState, id user.ID) error {
	return AppendMutation(ctx, tx, "core:"+string(r.connector.Code()), before, id)
}
