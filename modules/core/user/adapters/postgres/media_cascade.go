package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
)

// ClearMediaReferences keeps avatar changes and user hook deliveries in the
// enclosing file deletion transaction, including vetoes before storage changes.
func ClearMediaReferences(ctx context.Context, tx pgx.Tx, sourceName string, mediaIDs []int64, actorID *security.UserID) error {
	rows, err := tx.Query(ctx, `SELECT id FROM core.users WHERE avatar_media_id=ANY($1::bigint[]) ORDER BY id FOR UPDATE`, mediaIDs)
	if err != nil {
		return err
	}
	var ids []user.ID
	for rows.Next() {
		var id user.ID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		record, before, err := ReadHookState(ctx, tx, id)
		if err != nil {
			return err
		}
		record.AvatarMediaID = nil
		if _, _, err := user.PrepareMutation(ctx, &before, record, before.GroupIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE core.users SET avatar_media_id=NULL, updated_at=clock_timestamp(), updated_by=$2 WHERE id=$1`, id, actorID); err != nil {
			return err
		}
		if err := AppendMutation(ctx, tx, sourceName, &before, id); err != nil {
			return err
		}
	}
	return nil
}
