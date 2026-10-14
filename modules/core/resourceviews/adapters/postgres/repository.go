package postgres

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourceviews"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

type Repository struct{ connector *connectorpostgres.Connector }

func NewRepository(connector *connectorpostgres.Connector) (*Repository, error) {
	if connector == nil {
		return nil, errors.New("resource views PostgreSQL connector is nil")
	}
	return &Repository{connector: connector}, nil
}

func (r *Repository) ApplyBatch(ctx context.Context, batch resourceviews.Batch) error {
	if batch.ID == "" || batch.SiteID <= 0 || len(batch.Counts) == 0 {
		return errors.New("invalid resource views batch")
	}
	ids := make([]resource.ID, 0, len(batch.Counts))
	for id, count := range batch.Counts {
		if id <= 0 || count <= 0 {
			return errors.New("invalid resource views increment")
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	command, err := tx.Exec(ctx, `INSERT INTO resource_views.batches (batch_id, site_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, batch.ID, batch.SiteID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	resourceIDs := make([]int64, len(ids))
	increments := make([]int64, len(ids))
	for index, id := range ids {
		resourceIDs[index] = int64(id)
		increments[index] = batch.Counts[id]
	}
	_, err = tx.Exec(ctx, `INSERT INTO resource_views.counts (site_id, resource_id, total)
 SELECT entity.site_id, incoming.id, incoming.delta
 FROM unnest($1::bigint[], $2::bigint[]) AS incoming(id, delta)
 JOIN core.resource_entities entity ON entity.id=incoming.id
 ORDER BY incoming.id
 ON CONFLICT (site_id,resource_id) DO UPDATE
 SET total=resource_views.counts.total+EXCLUDED.total`, resourceIDs, increments)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) Total(ctx context.Context, siteID site.ID, id resource.ID) (int64, error) {
	var total int64
	err := r.connector.Pool().QueryRow(ctx, `SELECT total FROM resource_views.counts WHERE site_id=$1 AND resource_id=$2`, siteID, id).Scan(&total)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return total, err
}

var _ resourceviews.Repository = (*Repository)(nil)
