// Package mediaoccurrence is the shared PostgreSQL boundary for saved Media
// occurrences. Domain code never receives SQL transactions or connector types.
package mediaoccurrence

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	if _, err := tx.Exec(ctx, `DELETE FROM core.media_field_occurrences WHERE owner_kind=$1 AND owner_id=$2 AND container=$3`, owner.OwnerKind, owner.OwnerID, owner.Container); err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.Target != field.ReferenceFile && ref.Target != field.ReferenceMedia {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.media_field_occurrences(owner_kind,owner_id,site_id,container,value_path,media_id,reference_target) VALUES($1,$2,$3,$4,$5,$6,$7)`, owner.OwnerKind, owner.OwnerID, owner.SiteID, owner.Container, append([]string{}, ref.Path...), ref.ID, ref.Target); err != nil {
			return err
		}
	}
	return nil
}
