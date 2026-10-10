package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) ApplyFileDeletionSettings(ctx context.Context, actor *security.UserID, item site.Site) (site.Site, error) {
	tx, err := mediaoccurrence.Transaction(ctx, r.connector.Pool())
	if err != nil {
		return site.Site{}, err
	}
	raw, err := json.Marshal(item.Settings)
	if err != nil {
		return site.Site{}, err
	}
	result, err := scanOne(tx.QueryRow(ctx, `UPDATE core.sites SET settings=$2,updated_at=clock_timestamp(),updated_by=$3,runtime_version=runtime_version+1 WHERE id=$1 AND runtime_version=$4 RETURNING `+siteColumns, item.ID, raw, actor, item.Version))
	if err != nil {
		if errors.Is(err, site.ErrNotFound) {
			return site.Site{}, media.ErrFileDeleteConflict
		}
		return site.Site{}, err
	}
	if err := replaceFileReferences(ctx, tx, "site", int64(item.ID), item.FileReferences); err != nil {
		return site.Site{}, err
	}
	if err := mediaoccurrence.Replace(ctx, tx, media.FileOccurrence{OwnerKind: "site", OwnerID: int64(item.ID), SiteID: int64(item.ID), Container: "settings"}, item.MediaOccurrences); err != nil {
		return site.Site{}, err
	}
	return result, nil
}
