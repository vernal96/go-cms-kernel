// Package mediaoccurrence is the shared PostgreSQL boundary for saved Media
// occurrences. Domain code never receives SQL transactions or connector types.
package mediaoccurrence

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
)

type transactionKey struct{}
type transaction struct {
	pool *pgxpool.Pool
	tx   pgx.Tx
}

func WithTransaction(ctx context.Context, pool *pgxpool.Pool, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, transactionKey{}, transaction{pool, tx})
}

func Transaction(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	t, ok := ctx.Value(transactionKey{}).(transaction)
	if !ok || t.pool != pool || t.tx == nil {
		return nil, errors.New("media occurrence owner requires the same PostgreSQL unit of work")
	}
	return t.tx, nil
}

func Replace(ctx context.Context, tx pgx.Tx, owner media.FileOccurrence, refs []field.Reference) error {
	ids := make([]media.ID, 0, len(refs))
	for _, ref := range refs {
		if ref.Target == field.ReferenceMedia {
			ids = append(ids, media.ID(ref.ID))
		}
	}
	if err := medialock.Lock(ctx, tx, ids...); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.media_field_occurrences WHERE owner_kind=$1 AND owner_id=$2 AND container=$3`, owner.OwnerKind, owner.OwnerID, owner.Container); err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.Target != field.ReferenceMedia {
			continue
		}
		if err := EnsureAvailable(ctx, tx, media.ID(ref.ID), owner); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.media_field_occurrences(owner_kind,owner_id,site_id,container,value_path,media_id,reference_target) VALUES($1,$2,$3,$4,$5,$6,$7)`, owner.OwnerKind, owner.OwnerID, owner.SiteID, owner.Container, append([]string{}, ref.Path...), ref.ID, ref.Target); err != nil {
			return err
		}
	}
	return nil
}

// EnsureAvailable enforces a distinct Media for each saved selection. The
// caller holds medialock for id. excluded identifies an unchanged scalar image,
// or the secondary owner index being rebuilt; canonical field rows are never excluded.
func EnsureAvailable(ctx context.Context, tx pgx.Tx, id media.ID, excluded media.FileOccurrence) error {
	var attached bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM core.resource_media_references WHERE media_id=$1
 UNION ALL SELECT 1 FROM core.media_field_occurrences WHERE media_id=$1
 UNION ALL SELECT 1 FROM core.file_field_references WHERE media_id=$1 AND NOT (owner_kind=$2 AND owner_id=$3 AND (($2='site' AND $4='settings') OR ($2='resource' AND $4='fields')))
 UNION ALL SELECT 1 FROM core.resources WHERE image_media_id=$1 AND NOT ($2='resource' AND id=$3 AND $4='image')
 UNION ALL SELECT 1 FROM core.library_items WHERE image_media_id=$1 AND NOT ($2='resource' AND id=$3 AND $4='image')
 UNION ALL SELECT 1 FROM core.users WHERE avatar_media_id=$1 AND NOT ($2='user' AND id=$3 AND $4='avatar')
)`, id, excluded.OwnerKind, excluded.OwnerID, excluded.Container).Scan(&attached)
	if err != nil {
		return err
	}
	if attached {
		return media.ErrAlreadyAttached
	}
	return nil
}
