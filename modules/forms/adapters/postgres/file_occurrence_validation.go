package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
)

func (r *Repository) ReadFileOccurrence(ctx context.Context, ref media.FileOccurrence) (media.FileOccurrenceValues, error) {
	var result media.FileOccurrenceValues
	tx, err := mediaoccurrence.Transaction(ctx, r.connector.Pool())
	if err != nil {
		return result, err
	}
	var raw []byte
	if ref.OwnerKind == "forms.result" && ref.Container == "result_values" && len(ref.Path) >= 2 {
		id, err := strconv.ParseInt(ref.Path[0], 10, 64)
		if err != nil {
			return result, err
		}
		var position int
		if err := tx.QueryRow(ctx, `SELECT v.position,v.file_references FROM forms.result_values v JOIN forms.results r ON r.id=v.result_id WHERE v.id=$1 AND r.id=$2 AND r.site_id=$3`, id, ref.OwnerID, ref.SiteID).Scan(&position, &raw); err != nil {
			return result, err
		}
		if ref.Path[1] != strconv.Itoa(position) {
			return result, media.ErrInvalidReference
		}
		if err := json.Unmarshal(raw, &result.References); err != nil {
			return result, err
		}
		for index := range result.References {
			result.References[index].Path = append([]string{ref.Path[0], ref.Path[1]}, result.References[index].Path...)
			result.References[index].Key = field.ReferenceKey(result.References[index].Path)
		}
		return result, nil
	}
	if ref.OwnerKind != "forms.element" || ref.Container != "config" {
		return result, fmt.Errorf("%w: invalid Forms occurrence", media.ErrInvalidReference)
	}
	if err := tx.QueryRow(ctx, `SELECT e.type,e.config FROM forms.elements e JOIN forms.forms f ON f.id=e.form_id WHERE e.id=$1 AND f.site_id=$2`, ref.OwnerID, ref.SiteID).Scan(&result.Code, &raw); err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	err = decoder.Decode(&result.Values)
	return result, err
}
