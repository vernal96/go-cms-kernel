package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
)

func fieldMediaIDs(ctx context.Context, tx pgx.Tx, owners []resource.ID) ([]media.ID, error) {
	rows, err := tx.Query(ctx, `SELECT media_id FROM core.resource_media_references WHERE resource_id=ANY($1::bigint[]) ORDER BY media_id`, owners)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []media.ID
	for rows.Next() {
		var id media.ID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// The caller locks the old and new Media identities before changing ownership.
// Media owns metadata, not the physical source: deleting it must retain Files.
func deleteUnusedMedia(ctx context.Context, tx pgx.Tx, ids []media.ID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `DELETE FROM core.media m WHERE m.id=ANY($1::bigint[])
 AND NOT EXISTS (SELECT 1 FROM core.resource_media_references WHERE media_id=m.id)
 AND NOT EXISTS (SELECT 1 FROM core.resources WHERE image_media_id=m.id)
 AND NOT EXISTS (SELECT 1 FROM core.library_items WHERE image_media_id=m.id)
 AND NOT EXISTS (SELECT 1 FROM core.users WHERE avatar_media_id=m.id)`, ids)
	return translateDeleteError(err)
}
