package postgres

import (
	"context"
	"fmt"

	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
)

func (r *Repository) ReadFileOccurrence(ctx context.Context, ref media.FileOccurrence) (media.FileOccurrenceValues, error) {
	if ref.Container != "settings" || ref.OwnerID != ref.SiteID {
		return media.FileOccurrenceValues{}, fmt.Errorf("%w: invalid site occurrence", media.ErrInvalidReference)
	}
	tx, err := mediaoccurrence.Transaction(ctx, r.connector.Pool())
	if err != nil {
		return media.FileOccurrenceValues{}, err
	}
	item, err := scanOne(tx.QueryRow(ctx, `SELECT `+siteColumns+` FROM core.sites WHERE id=$1`, ref.OwnerID))
	return media.FileOccurrenceValues{Code: string(item.ProfileCode), Values: item.Settings}, err
}
