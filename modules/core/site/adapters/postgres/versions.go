package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

func (r *Repository) RuntimeVersion(ctx context.Context, id site.ID) (int64, error) {
	var version int64
	err := r.connector.Pool().QueryRow(ctx, "SELECT runtime_version FROM core.sites WHERE id=$1", id).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, site.ErrNotFound
	}
	return version, err
}
func (r *Repository) RuntimeVersions(ctx context.Context) (map[site.ID]int64, error) {
	rows, err := r.connector.Pool().Query(ctx, "SELECT id,runtime_version FROM core.sites")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[site.ID]int64{}
	for rows.Next() {
		var id site.ID
		var version int64
		if err := rows.Scan(&id, &version); err != nil {
			return nil, err
		}
		result[id] = version
	}
	return result, rows.Err()
}
