package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/forms"
)

// ListPublicResults reads publication settings, the count and values from the
// same snapshot. Historical values are matched by field identity, never code.
func (r *Repository) ListPublicResults(ctx context.Context, siteID site.ID, formID forms.FormID, query forms.PageQuery) (result forms.PublicResultsPage, resultErr error) {
	if err := forms.ValidatePublicPage(query); err != nil {
		return result, err
	}
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer rollback(ctx, tx, &resultErr)
	var id forms.FormID
	if err := tx.QueryRow(ctx, "SELECT id FROM forms.forms WHERE site_id=$1 AND id=$2 AND enabled", siteID, formID).Scan(&id); err != nil {
		return result, mapNotFound(err)
	}

	result.Columns, result.Items = []forms.PublicResultColumn{}, []forms.PublicResult{}
	result.Pagination = forms.PublicResultPagination{Page: query.Page, PerPage: query.PerPage}
	fields, err := listFields(ctx, tx, formID)
	if err != nil {
		return result, err
	}
	codes := map[forms.FieldID]string{}
	fieldIDs := []forms.FieldID{}
	for _, item := range fields {
		if !item.ShowOnSite || item.Type == forms.FieldTypeCaptcha || item.Type == forms.FieldTypeUpload {
			continue
		}
		codes[item.ID] = item.Code
		fieldIDs = append(fieldIDs, item.ID)
		result.Columns = append(result.Columns, forms.PublicResultColumn{Code: item.Code, Label: item.EffectiveResultLabel(), Type: item.Type})
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM forms.results WHERE site_id=$1 AND form_id=$2", siteID, formID).Scan(&result.Pagination.Total); err != nil {
		return result, err
	}
	result.Pagination.Pages = result.Pagination.Total / query.PerPage
	if result.Pagination.Total%query.PerPage != 0 {
		result.Pagination.Pages++
	}
	rows, err := tx.Query(ctx, "SELECT id,created_at FROM forms.results WHERE site_id=$1 AND form_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4", siteID, formID, query.PerPage, (query.Page-1)*query.PerPage)
	if err != nil {
		return result, err
	}
	ids := []forms.ResultID{}
	positions := map[forms.ResultID]int{}
	for rows.Next() {
		var id forms.ResultID
		item := forms.PublicResult{Values: map[string]any{}}
		if err := rows.Scan(&id, &item.CreatedAt); err != nil {
			rows.Close()
			return result, err
		}
		positions[id] = len(result.Items)
		ids = append(ids, id)
		result.Items = append(result.Items, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(ids) > 0 && len(fieldIDs) > 0 {
		values, err := tx.Query(ctx, `SELECT id,result_id,field_id,field_code,field_label,result_label,field_type,storage_kind,position,is_multi,string_value,integer_value,float_value,boolean_value,timestamp_value,reference_value,json_value FROM forms.result_values WHERE result_id=ANY($1) AND field_id=ANY($2) ORDER BY result_id,field_id,position,id`, ids, fieldIDs)
		if err != nil {
			return result, err
		}
		grouped := map[forms.ResultID]map[string][]forms.ResultValue{}
		for values.Next() {
			value, err := scanResultValue(values)
			if err != nil {
				values.Close()
				return result, err
			}
			if value.FieldID == nil {
				continue
			}
			code, exists := codes[*value.FieldID]
			if !exists {
				continue
			}
			if grouped[value.ResultID] == nil {
				grouped[value.ResultID] = map[string][]forms.ResultValue{}
			}
			grouped[value.ResultID][code] = append(grouped[value.ResultID][code], value)
		}
		values.Close()
		if err := values.Err(); err != nil {
			return result, err
		}
		for id, fields := range grouped {
			for code, items := range fields {
				result.Items[positions[id]].Values[code] = forms.ResultFieldValue(items)
			}
		}
	}
	return result, tx.Commit(ctx)
}
