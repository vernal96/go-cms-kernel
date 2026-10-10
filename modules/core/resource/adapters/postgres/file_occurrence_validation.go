package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
)

func (r *Repository) ReadFileOccurrence(ctx context.Context, ref media.FileOccurrence) (media.FileOccurrenceValues, error) {
	tx, err := mediaoccurrence.Transaction(ctx, r.connector.Pool())
	if err != nil {
		return media.FileOccurrenceValues{}, err
	}
	var result media.FileOccurrenceValues
	if strings.HasPrefix(ref.Container, "fields:") {
		err := tx.QueryRow(ctx, `SELECT template FROM core.resources WHERE id=$1 AND site_id=$2 UNION ALL SELECT template FROM core.library_items WHERE id=$1 AND site_id=$2`, ref.OwnerID, ref.SiteID).Scan(&result.Code)
		if err != nil {
			return result, err
		}
		items := []resource.Resource{{ID: resource.ID(ref.OwnerID)}}
		if err := loadResourceFields(ctx, tx, items); err != nil {
			return result, err
		}
		result.Values = items[0].Fields
		return result, nil
	}
	if !strings.HasPrefix(ref.Container, "widget:") {
		return result, fmt.Errorf("%w: resource occurrence container %q", media.ErrInvalidReference, ref.Container)
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(ref.Container, "widget:"), 10, 64)
	if err != nil {
		return result, err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT w.widget_code,w.params FROM core.resource_widgets w JOIN core.resource_entities e ON e.id=w.resource_id WHERE w.id=$1 AND e.id=$2 AND e.site_id=$3`, id, ref.OwnerID, ref.SiteID).Scan(&result.Code, &raw); err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	err = decoder.Decode(&result.Values)
	return result, err
}
