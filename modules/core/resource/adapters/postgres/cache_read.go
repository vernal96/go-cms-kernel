package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func (r *Repository) LookupRoute(ctx context.Context, siteID site.ID, path string) (resource.RouteTarget, error) {
	if _, err := resource.NormalizeLookupPath(path); err != nil {
		return resource.RouteTarget{}, resource.ErrNotFound
	}
	target := resource.RouteTarget{SiteID: siteID, Kind: resource.StorageTree}
	err := r.connector.Pool().QueryRow(ctx, `SELECT id FROM core.resources WHERE site_id=$1 AND path=$2`, siteID, path).Scan(&target.ID)
	if err == nil {
		return target, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return resource.RouteTarget{}, err
	}
	item, library, err := r.lookupLibraryItemRoute(ctx, siteID, path)
	if err != nil {
		return resource.RouteTarget{}, err
	}
	return resource.RouteTarget{SiteID: siteID, ID: item.ID, Kind: resource.StorageLibraryItem, LibraryID: library.ID}, nil
}

func (r *Repository) WidgetsByID(ctx context.Context, owner resource.ID, ids []widget.BindingID) ([]widget.Binding, error) {
	if len(ids) == 0 {
		return []widget.Binding{}, nil
	}
	items := []resource.Resource{{ID: owner}}
	if err := loadSelectedResourceWidgets(ctx, r.connector.Pool(), items, ids); err != nil {
		return nil, err
	}
	return items[0].Widgets, nil
}

var _ resource.CacheReadRepository = (*Repository)(nil)
