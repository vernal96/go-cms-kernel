package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/forms"
)

func scanResult(row rowScanner) (forms.Result, error) {
	var item forms.Result
	err := row.Scan(&item.ID, &item.SiteID, &item.FormID, &item.FormCode, &item.FormName, &item.StatusID, &item.StatusCode, &item.StatusName, &item.StatusColor, &item.UserID, &item.UserAgent, &item.ClientAddress, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func (r *Repository) CreateResult(ctx context.Context, record forms.SubmissionRecord) (_ forms.ResultDetail, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return forms.ResultDetail{}, err
	}
	defer rollback(ctx, tx, &resultErr)
	var createdID forms.ResultID
	err = tx.QueryRow(ctx, `INSERT INTO forms.results(site_id,form_id,form_code,form_name,status_id,user_id,user_agent,client_address)
SELECT $1,f.id,$3,$4,s.id,$6,$7,$8 FROM forms.forms f JOIN forms.statuses s ON s.form_id=f.id AND s.id=$5 WHERE f.site_id=$1 AND f.id=$2 AND f.enabled
RETURNING id;`, record.Result.SiteID, record.Result.FormID, record.Result.FormCode, record.Result.FormName, record.Result.StatusID, record.Result.UserID, record.Result.UserAgent, record.Result.ClientAddress).Scan(&createdID)
	if err != nil {
		return forms.ResultDetail{}, mapWriteError(err)
	}
	created, err := scanResult(tx.QueryRow(ctx, `SELECT `+resultColumns+` FROM forms.results r JOIN forms.statuses s ON s.id=r.status_id WHERE r.id=$1;`, createdID))
	if err != nil {
		return forms.ResultDetail{}, err
	}
	values := make([]forms.ResultValue, len(record.Values))
	valueReferences := make([]field.Reference, 0)
	for index, item := range record.Values {
		item.ResultID = created.ID
		values[index], err = insertResultValue(ctx, tx, item)
		if err != nil {
			return forms.ResultDetail{}, err
		}
		valueReferences = append(valueReferences, resultValueReferences(values[index])...)
	}
	if err := mediaoccurrence.Replace(ctx, tx, media.FileOccurrence{OwnerKind: "forms.result", OwnerID: int64(created.ID), SiteID: int64(record.Result.SiteID), Container: "result_values"}, valueReferences); err != nil {
		return forms.ResultDetail{}, err
	}
	uploads := make([]forms.ResultUpload, len(record.Uploads))
	for index, item := range record.Uploads {
		item.ResultID = created.ID
		uploads[index], err = insertResultUpload(ctx, tx, item)
		if err != nil {
			return forms.ResultDetail{}, err
		}
	}
	actions, err := matchingActionsTx(ctx, tx, created.FormID, forms.Trigger{Type: forms.TriggerSubmitted})
	if err != nil {
		return forms.ResultDetail{}, err
	}
	executions := make([]forms.ActionExecution, len(actions))
	for index, action := range actions {
		executions[index], err = insertExecutionAndOutbox(ctx, tx, created, action, forms.Trigger{Type: forms.TriggerSubmitted})
		if err != nil {
			return forms.ResultDetail{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return forms.ResultDetail{}, err
	}
	return forms.ResultDetail{Result: created, Values: values, Uploads: uploads, Executions: executions}, nil
}

func resultValueColumns(item forms.ResultValue) (stringValue *string, integerValue *int64, floatValue *float64, booleanValue *bool, timestampValue *time.Time, referenceValue *int64, jsonValue []byte, err error) {
	switch item.StorageKind {
	case field.StorageString:
		value, ok := item.Value.(string)
		if !ok {
			err = forms.ErrInvalid
		} else {
			stringValue = &value
		}
	case field.StorageInteger:
		value, ok := item.Value.(int64)
		if !ok {
			err = forms.ErrInvalid
		} else {
			integerValue = &value
		}
	case field.StorageFloat:
		value, ok := item.Value.(float64)
		if !ok {
			err = forms.ErrInvalid
		} else {
			floatValue = &value
		}
	case field.StorageBoolean:
		value, ok := item.Value.(bool)
		if !ok {
			err = forms.ErrInvalid
		} else {
			booleanValue = &value
		}
	case field.StorageTimestamp:
		value, ok := item.Value.(time.Time)
		if !ok {
			err = forms.ErrInvalid
		} else {
			timestampValue = &value
		}
	case field.StorageReference:
		value, ok := item.Value.(int64)
		if !ok {
			err = forms.ErrInvalid
		} else {
			referenceValue = &value
		}
	case field.StorageJSON:
		jsonValue, err = json.Marshal(item.Value)
	default:
		err = forms.ErrInvalid
	}
	return
}

func insertResultValue(ctx context.Context, tx pgx.Tx, item forms.ResultValue) (forms.ResultValue, error) {
	stringValue, integerValue, floatValue, booleanValue, timestampValue, referenceValue, jsonValue, err := resultValueColumns(item)
	if err != nil {
		return forms.ResultValue{}, err
	}
	snapshots, err := json.Marshal(item.FileReferences)
	if err != nil {
		return forms.ResultValue{}, err
	}
	if item.FileReferences == nil {
		snapshots = []byte(`[]`)
	}
	var created forms.ResultValue
	var raw []byte
	var stringOut *string
	var integerOut, referenceOut *int64
	var floatOut *float64
	var boolOut *bool
	var timeOut *time.Time
	err = tx.QueryRow(ctx, `INSERT INTO forms.result_values(result_id,field_id,field_code,field_label,result_label,field_type,storage_kind,position,is_multi,string_value,integer_value,float_value,boolean_value,timestamp_value,reference_value,json_value,file_references) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING id,result_id,field_id,field_code,field_label,result_label,field_type,storage_kind,position,is_multi,string_value,integer_value,float_value,boolean_value,timestamp_value,reference_value,json_value;`, item.ResultID, item.FieldID, item.FieldCode, item.FieldLabel, item.ResultLabel, item.FieldType, item.StorageKind, item.Position, item.Multiple, stringValue, integerValue, floatValue, booleanValue, timestampValue, referenceValue, jsonValue, snapshots).Scan(&created.ID, &created.ResultID, &created.FieldID, &created.FieldCode, &created.FieldLabel, &created.ResultLabel, &created.FieldType, &created.StorageKind, &created.Position, &created.Multiple, &stringOut, &integerOut, &floatOut, &boolOut, &timeOut, &referenceOut, &raw)
	if err != nil {
		return forms.ResultValue{}, mapWriteError(err)
	}
	created.Value, err = decodedStoredValue(created.StorageKind, stringOut, integerOut, floatOut, boolOut, timeOut, referenceOut, raw)
	created.ReferenceTarget, created.References = item.ReferenceTarget, item.References
	created.FileReferences = item.FileReferences
	return created, err
}

func resultValueReferences(item forms.ResultValue) []field.Reference {
	var references []field.Reference
	if item.StorageKind == field.StorageReference && (item.ReferenceTarget == field.ReferenceFile || item.ReferenceTarget == field.ReferenceMedia || item.FieldType == field.TypeFile || item.FieldType == field.TypeMedia) {
		if id, ok := item.Value.(int64); ok && id > 0 {
			references = append(references, field.Reference{Target: field.ReferenceMedia, ID: id})
		}
	}
	for _, reference := range item.References {
		if reference.Target != field.ReferenceFile && reference.Target != field.ReferenceMedia {
			continue
		}
		reference.Target = field.ReferenceMedia
		reference.Path = append([]string{fmt.Sprint(item.ID), fmt.Sprint(item.Position)}, reference.Path...)
		references = append(references, reference)
	}
	for index := range references {
		if len(references[index].Path) == 0 {
			references[index].Path = []string{fmt.Sprint(item.ID), fmt.Sprint(item.Position)}
		} else if len(references[index].Path) == 1 {
			references[index].Path = append(references[index].Path, fmt.Sprint(item.Position))
		} else if references[index].Path[0] != fmt.Sprint(item.ID) {
			references[index].Path = append([]string{fmt.Sprint(item.ID), fmt.Sprint(item.Position)}, references[index].Path...)
		}
	}
	return references
}

func decodedStoredValue(kind field.StorageKind, stringValue *string, integerValue *int64, floatValue *float64, booleanValue *bool, timestampValue *time.Time, referenceValue *int64, raw []byte) (any, error) {
	switch kind {
	case field.StorageString:
		if stringValue != nil {
			return *stringValue, nil
		}
	case field.StorageInteger:
		if integerValue != nil {
			return *integerValue, nil
		}
	case field.StorageFloat:
		if floatValue != nil {
			return *floatValue, nil
		}
	case field.StorageBoolean:
		if booleanValue != nil {
			return *booleanValue, nil
		}
	case field.StorageTimestamp:
		if timestampValue != nil {
			return *timestampValue, nil
		}
	case field.StorageReference:
		if referenceValue != nil {
			return *referenceValue, nil
		}
	case field.StorageJSON:
		var value any
		if len(raw) > 0 && json.Unmarshal(raw, &value) == nil {
			return value, nil
		}
	}
	return nil, errors.New("stored Forms result value is invalid")
}

func insertResultUpload(ctx context.Context, tx pgx.Tx, item forms.ResultUpload) (forms.ResultUpload, error) {
	var created forms.ResultUpload
	err := tx.QueryRow(ctx, `INSERT INTO forms.result_uploads(result_id,field_id,field_code,position,filename,mime_type,size,checksum,spool_reference) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id,result_id,field_id,field_code,position,filename,mime_type,size,checksum,spool_reference,spool_deleted_at;`, item.ResultID, item.FieldID, item.FieldCode, item.Position, item.Filename, item.MIMEType, item.Size, item.Checksum, item.SpoolReference).Scan(&created.ID, &created.ResultID, &created.FieldID, &created.FieldCode, &created.Position, &created.Filename, &created.MIMEType, &created.Size, &created.Checksum, &created.SpoolReference, &created.SpoolDeletedAt)
	return created, mapWriteError(err)
}

func matchingActionsTx(ctx context.Context, tx pgx.Tx, formID forms.FormID, trigger forms.Trigger) ([]forms.Action, error) {
	query := `SELECT ` + actionColumns + ` FROM forms.actions WHERE form_id=$1 AND enabled AND trigger->>'type'=$2`
	args := []any{formID, trigger.Type}
	if trigger.Type == forms.TriggerStatusChanged {
		query += ` AND (coalesce(trigger->>'from_status','')='' OR trigger->>'from_status'=$3) AND (coalesce(trigger->>'to_status','')='' OR trigger->>'to_status'=$4)`
		args = append(args, trigger.From, trigger.To)
	}
	query += ` ORDER BY position,id FOR SHARE;`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []forms.Action{}
	for rows.Next() {
		item, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func insertExecutionAndOutbox(ctx context.Context, tx pgx.Tx, result forms.Result, action forms.Action, trigger forms.Trigger) (forms.ActionExecution, error) {
	triggerJSON, err := json.Marshal(trigger)
	if err != nil {
		return forms.ActionExecution{}, err
	}
	var execution forms.ActionExecution
	var scannedTrigger []byte
	err = tx.QueryRow(ctx, `INSERT INTO forms.action_executions(site_id,result_id,action_id,action_code,action_name,action_type,trigger,config,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending') RETURNING `+executionColumns+`;`, result.SiteID, result.ID, action.ID, action.Code, action.Name, action.ActionType, triggerJSON, action.Config).Scan(&execution.ID, &execution.SiteID, &execution.ResultID, &execution.ActionID, &execution.ActionCode, &execution.ActionName, &execution.ActionType, &scannedTrigger, &execution.Config, &execution.Status, &execution.AttemptCount, &execution.SafeError, &execution.ExternalReference, &execution.StartedAt, &execution.FinishedAt, &execution.CreatedAt, &execution.UpdatedAt)
	if err != nil {
		return forms.ActionExecution{}, err
	}
	if err := json.Unmarshal(scannedTrigger, &execution.Trigger); err != nil {
		return forms.ActionExecution{}, err
	}
	envelope, err := job.NewScoped(forms.ExecuteActionJobName, 1, fmt.Sprint(result.SiteID), struct {
		ExecutionID forms.ActionExecutionID `json:"action_execution_id"`
	}{execution.ID})
	if err != nil {
		return forms.ActionExecution{}, err
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return forms.ActionExecution{}, err
	}
	headers, err := json.Marshal(map[string][]byte{"content-type": []byte("application/json"), "x-cms-message-id": []byte(envelope.ID), "x-cms-job-name": []byte(forms.ExecuteActionJobName)})
	if err != nil {
		return forms.ActionExecution{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO core.outbox_messages(message_id,topic,message_key,body,headers) VALUES($1,$2,$3,$4,$5);`, envelope.ID, job.Topic(forms.ExecuteActionJobName), []byte(envelope.ID), body, headers)
	return execution, err
}

func (r *Repository) ListResults(ctx context.Context, siteID site.ID, query forms.ResultQuery, fieldCodes []string) (forms.ResultSummaryPage, error) {
	rows, err := r.connector.Pool().Query(ctx, `SELECT `+resultColumns+`,count(*) OVER() FROM forms.results r JOIN forms.statuses s ON s.id=r.status_id WHERE r.site_id=$1 AND ($2::bigint=0 OR r.form_id=$2) AND ($3::bigint=0 OR r.status_id=$3) AND ($4::timestamptz IS NULL OR r.created_at >= $4) AND ($5::timestamptz IS NULL OR r.created_at <= $5) ORDER BY r.created_at DESC,r.id DESC LIMIT $6 OFFSET $7;`, siteID, query.FormID, query.StatusID, query.DateFrom, query.DateTo, query.PerPage, (query.Page-1)*query.PerPage)
	if err != nil {
		return forms.ResultSummaryPage{}, err
	}
	defer rows.Close()
	result := forms.ResultSummaryPage{Items: []forms.ResultSummary{}}
	ids := []forms.ResultID{}
	for rows.Next() {
		var item forms.ResultSummary
		var total int
		if err := rows.Scan(&item.ID, &item.SiteID, &item.FormID, &item.FormCode, &item.FormName, &item.StatusID, &item.StatusCode, &item.StatusName, &item.StatusColor, &item.UserID, &item.UserAgent, &item.ClientAddress, &item.CreatedAt, &item.UpdatedAt, &total); err != nil {
			return forms.ResultSummaryPage{}, err
		}
		item.Values = map[string]any{}
		result.Items, result.Total, ids = append(result.Items, item), total, append(ids, item.ID)
	}
	if err := rows.Err(); err != nil || len(ids) == 0 || len(fieldCodes) == 0 {
		return result, err
	}
	valueRows, err := r.connector.Pool().Query(ctx, `SELECT id,result_id,field_id,field_code,field_label,result_label,field_type,storage_kind,position,is_multi,string_value,integer_value,float_value,boolean_value,timestamp_value,reference_value,json_value FROM forms.result_values WHERE result_id=ANY($1) AND field_code=ANY($2) ORDER BY result_id,field_code,position;`, ids, fieldCodes)
	if err != nil {
		return forms.ResultSummaryPage{}, err
	}
	defer valueRows.Close()
	byID := make(map[forms.ResultID]*forms.ResultSummary, len(result.Items))
	for index := range result.Items {
		byID[result.Items[index].ID] = &result.Items[index]
	}
	grouped := make(map[forms.ResultID]map[string][]forms.ResultValue)
	for valueRows.Next() {
		item, err := scanResultValue(valueRows)
		if err != nil {
			return forms.ResultSummaryPage{}, err
		}
		if grouped[item.ResultID] == nil {
			grouped[item.ResultID] = make(map[string][]forms.ResultValue)
		}
		grouped[item.ResultID][item.FieldCode] = append(grouped[item.ResultID][item.FieldCode], item)
	}
	for resultID, fields := range grouped {
		for code, values := range fields {
			byID[resultID].Values[code] = forms.ResultFieldValue(values)
		}
	}
	return result, valueRows.Err()
}

func scanResultValue(row rowScanner) (forms.ResultValue, error) {
	var item forms.ResultValue
	var stringValue *string
	var integerValue, referenceValue *int64
	var floatValue *float64
	var booleanValue *bool
	var timestampValue *time.Time
	var raw []byte
	err := row.Scan(&item.ID, &item.ResultID, &item.FieldID, &item.FieldCode, &item.FieldLabel, &item.ResultLabel, &item.FieldType, &item.StorageKind, &item.Position, &item.Multiple, &stringValue, &integerValue, &floatValue, &booleanValue, &timestampValue, &referenceValue, &raw)
	if err != nil {
		return forms.ResultValue{}, err
	}
	item.Value, err = decodedStoredValue(item.StorageKind, stringValue, integerValue, floatValue, booleanValue, timestampValue, referenceValue, raw)
	return item, err
}

func listResultValues(ctx context.Context, q querier, resultID forms.ResultID) ([]forms.ResultValue, error) {
	rows, err := q.Query(ctx, `SELECT id,result_id,field_id,field_code,field_label,result_label,field_type,storage_kind,position,is_multi,string_value,integer_value,float_value,boolean_value,timestamp_value,reference_value,json_value FROM forms.result_values WHERE result_id=$1 ORDER BY id;`, resultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []forms.ResultValue{}
	for rows.Next() {
		item, err := scanResultValue(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func scanResultUpload(row rowScanner) (forms.ResultUpload, error) {
	var item forms.ResultUpload
	err := row.Scan(&item.ID, &item.ResultID, &item.FieldID, &item.FieldCode, &item.Position, &item.Filename, &item.MIMEType, &item.Size, &item.Checksum, &item.SpoolReference, &item.SpoolDeletedAt)
	return item, err
}

func listResultUploads(ctx context.Context, q querier, resultID forms.ResultID) ([]forms.ResultUpload, error) {
	rows, err := q.Query(ctx, `SELECT id,result_id,field_id,field_code,position,filename,mime_type,size,checksum,spool_reference,spool_deleted_at FROM forms.result_uploads WHERE result_id=$1 ORDER BY field_code,position,id;`, resultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []forms.ResultUpload{}
	for rows.Next() {
		item, err := scanResultUpload(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func scanExecution(row rowScanner) (forms.ActionExecution, error) {
	var item forms.ActionExecution
	var trigger []byte
	err := row.Scan(&item.ID, &item.SiteID, &item.ResultID, &item.ActionID, &item.ActionCode, &item.ActionName, &item.ActionType, &trigger, &item.Config, &item.Status, &item.AttemptCount, &item.SafeError, &item.ExternalReference, &item.StartedAt, &item.FinishedAt, &item.CreatedAt, &item.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(trigger, &item.Trigger)
	}
	return item, err
}

func listExecutions(ctx context.Context, q querier, resultID forms.ResultID) ([]forms.ActionExecution, error) {
	rows, err := q.Query(ctx, `SELECT `+executionColumns+` FROM forms.action_executions WHERE result_id=$1 ORDER BY created_at,id;`, resultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []forms.ActionExecution{}
	for rows.Next() {
		item, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func resultDetail(ctx context.Context, q querier, siteID site.ID, id forms.ResultID) (forms.ResultDetail, error) {
	item, err := scanResult(q.QueryRow(ctx, `SELECT `+resultColumns+` FROM forms.results r JOIN forms.statuses s ON s.id=r.status_id WHERE r.site_id=$1 AND r.id=$2;`, siteID, id))
	if err != nil {
		return forms.ResultDetail{}, mapNotFound(err)
	}
	values, err := listResultValues(ctx, q, id)
	if err != nil {
		return forms.ResultDetail{}, err
	}
	uploads, err := listResultUploads(ctx, q, id)
	if err != nil {
		return forms.ResultDetail{}, err
	}
	executions, err := listExecutions(ctx, q, id)
	if err != nil {
		return forms.ResultDetail{}, err
	}
	return forms.ResultDetail{Result: item, Values: values, Uploads: uploads, Executions: executions}, nil
}

func (r *Repository) ResultDetail(ctx context.Context, siteID site.ID, id forms.ResultID) (forms.ResultDetail, error) {
	return resultDetail(ctx, r.connector.Pool(), siteID, id)
}

func (r *Repository) ChangeResultStatus(ctx context.Context, input forms.ResultStatusChange) (_ forms.ResultDetail, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return forms.ResultDetail{}, err
	}
	defer rollback(ctx, tx, &resultErr)
	var formID forms.FormID
	var fromCode, toCode string
	err = tx.QueryRow(ctx, `SELECT r.form_id,current.code,target.code FROM forms.results r JOIN forms.statuses current ON current.id=r.status_id JOIN forms.statuses target ON target.id=$4 AND target.form_id=r.form_id WHERE r.site_id=$1 AND r.id=$2 AND r.status_id=$3 FOR UPDATE OF r;`, input.SiteID, input.ResultID, input.FromStatusID, input.ToStatusID).Scan(&formID, &fromCode, &toCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return forms.ResultDetail{}, forms.ErrConflict
	}
	if err != nil {
		return forms.ResultDetail{}, err
	}
	command, err := tx.Exec(ctx, `UPDATE forms.results SET status_id=$3,updated_at=clock_timestamp() WHERE site_id=$1 AND id=$2;`, input.SiteID, input.ResultID, input.ToStatusID)
	if err != nil || command.RowsAffected() != 1 {
		return forms.ResultDetail{}, errors.Join(err, forms.ErrConflict)
	}
	updated, err := scanResult(tx.QueryRow(ctx, `SELECT `+resultColumns+` FROM forms.results r JOIN forms.statuses s ON s.id=r.status_id WHERE r.site_id=$1 AND r.id=$2;`, input.SiteID, input.ResultID))
	if err != nil {
		return forms.ResultDetail{}, err
	}
	trigger := forms.Trigger{Type: forms.TriggerStatusChanged, From: fromCode, To: toCode}
	actions, err := matchingActionsTx(ctx, tx, formID, trigger)
	if err != nil {
		return forms.ResultDetail{}, err
	}
	for _, action := range actions {
		if _, err := insertExecutionAndOutbox(ctx, tx, updated, action, trigger); err != nil {
			return forms.ResultDetail{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return forms.ResultDetail{}, err
	}
	return resultDetail(ctx, r.connector.Pool(), input.SiteID, input.ResultID)
}

func (r *Repository) DeleteResult(ctx context.Context, siteID site.ID, id forms.ResultID) (_ []string, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(ctx, tx, &resultErr)
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM forms.action_executions WHERE result_id=$1 AND site_id=$2 AND status IN ('pending','running','retryable'));`, id, siteID).Scan(&active); err != nil {
		return nil, err
	}
	if active {
		return nil, forms.ErrActiveExecutions
	}
	rows, err := tx.Query(ctx, `SELECT spool_reference FROM forms.result_uploads WHERE result_id=$1 AND spool_reference IS NOT NULL AND spool_deleted_at IS NULL;`, id)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.media_field_occurrences WHERE site_id=$1 AND owner_kind='forms.result' AND owner_id=$2 AND container='result_values';`, siteID, id); err != nil {
		return nil, err
	}
	command, err := tx.Exec(ctx, `DELETE FROM forms.results WHERE site_id=$1 AND id=$2;`, siteID, id)
	if err != nil {
		return nil, err
	}
	if command.RowsAffected() == 0 {
		return nil, forms.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return keys, nil
}
