package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) UpdateSettings(ctx context.Context, actor *security.UserID, id media.ID, values map[string]any, expected time.Time) (media.Media, error) {
	raw, err := json.Marshal(values)
	if err != nil {
		return media.Media{}, err
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return media.Media{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := medialock.Lock(ctx, tx, id); err != nil {
		return media.Media{}, err
	}
	var updated time.Time
	err = tx.QueryRow(ctx, `SELECT updated_at FROM core.media WHERE id=$1 FOR UPDATE`, id).Scan(&updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return media.Media{}, media.ErrNotFound
	}
	if err != nil {
		return media.Media{}, err
	}
	if expected.IsZero() || !updated.Equal(expected) {
		return media.Media{}, media.ErrSettingsConflict
	}
	item, err := scanMedia(tx.QueryRow(ctx, `UPDATE core.media SET params=jsonb_set(params, '{settings}', $2::jsonb), updated_by=$3, updated_at=clock_timestamp() WHERE id=$1 RETURNING id,file_id,title,params,created_at,updated_at,created_by,updated_by`, id, string(raw), actor))
	if err != nil {
		return media.Media{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return media.Media{}, err
	}
	return item, nil
}
