package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/mail"
	"github.com/vernal96/go-cms-kernel/security"
)

type Repository struct{ connector *connectorpostgres.Connector }

func NewRepository(connector *connectorpostgres.Connector) (*Repository, error) {
	if connector == nil || connector.Pool() == nil {
		return nil, errors.New("mail postgres connector is nil")
	}
	return &Repository{connector: connector}, nil
}

const templateColumns = `
id,site_id,code,name,enabled,from_address,to_addresses,cc_addresses,bcc_addresses,
reply_to,subject,content_type,text_body,html_body,attachments,variables,created_at,updated_at,created_by,updated_by`

func (r *Repository) ListTemplates(ctx context.Context, siteID site.ID, query mail.PageQuery) (mail.TemplatePage, error) {
	return r.listTemplates(ctx, siteID, query, false)
}

func (r *Repository) ListEnabledTemplates(ctx context.Context, siteID site.ID, query mail.PageQuery) (mail.TemplatePage, error) {
	return r.listTemplates(ctx, siteID, query, true)
}

func (r *Repository) listTemplates(ctx context.Context, siteID site.ID, query mail.PageQuery, enabledOnly bool) (mail.TemplatePage, error) {
	enabledClause := ""
	if enabledOnly {
		enabledClause = " AND enabled"
	}
	rows, err := r.connector.Pool().Query(ctx, `SELECT `+templateColumns+`,count(*) OVER()
FROM mail.templates WHERE site_id=$1`+enabledClause+` ORDER BY name,id LIMIT $2 OFFSET $3;`, siteID, query.PerPage, (query.Page-1)*query.PerPage)
	if err != nil {
		return mail.TemplatePage{}, fmt.Errorf("list mail templates: %w", err)
	}
	defer rows.Close()
	result := mail.TemplatePage{Items: []mail.Template{}}
	for rows.Next() {
		item, total, err := scanTemplateWithTotal(rows)
		if err != nil {
			return mail.TemplatePage{}, err
		}
		result.Items, result.Total = append(result.Items, item), total
	}
	return result, rows.Err()
}

func (r *Repository) TemplateByID(ctx context.Context, siteID site.ID, id mail.TemplateID) (mail.Template, error) {
	item, err := scanTemplate(r.connector.Pool().QueryRow(ctx, `SELECT `+templateColumns+` FROM mail.templates WHERE site_id=$1 AND id=$2;`, siteID, id))
	return item, mapNotFound(err)
}

func (r *Repository) TemplateByCode(ctx context.Context, siteID site.ID, code string) (mail.Template, error) {
	item, err := scanTemplate(r.connector.Pool().QueryRow(ctx, `SELECT `+templateColumns+` FROM mail.templates WHERE site_id=$1 AND code=$2;`, siteID, code))
	return item, mapNotFound(err)
}

func (r *Repository) CreateTemplate(ctx context.Context, item mail.Template) (mail.Template, error) {
	values, err := templateJSON(item)
	if err != nil {
		return mail.Template{}, err
	}
	created, err := scanTemplate(r.connector.Pool().QueryRow(ctx, `
INSERT INTO mail.templates(site_id,code,name,enabled,from_address,to_addresses,cc_addresses,bcc_addresses,reply_to,subject,content_type,text_body,html_body,attachments,variables,created_by,updated_by)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)
RETURNING `+templateColumns+`;`, item.SiteID, item.Code, item.Name, item.Enabled, values.from, values.to, values.cc, values.bcc, values.replyTo, item.Subject, item.ContentType, item.TextBody, item.HTMLBody, values.attachments, values.variables, item.CreatedBy))
	return created, mapWriteError(err)
}

func (r *Repository) UpdateTemplate(ctx context.Context, item mail.Template) (mail.Template, error) {
	values, err := templateJSON(item)
	if err != nil {
		return mail.Template{}, err
	}
	updated, err := scanTemplate(r.connector.Pool().QueryRow(ctx, `
UPDATE mail.templates SET code=$3,name=$4,enabled=$5,from_address=$6,to_addresses=$7,cc_addresses=$8,bcc_addresses=$9,reply_to=$10,subject=$11,content_type=$12,text_body=$13,html_body=$14,attachments=$15,variables=$16,updated_at=clock_timestamp(),updated_by=$17
WHERE site_id=$1 AND id=$2 RETURNING `+templateColumns+`;`, item.SiteID, item.ID, item.Code, item.Name, item.Enabled, values.from, values.to, values.cc, values.bcc, values.replyTo, item.Subject, item.ContentType, item.TextBody, item.HTMLBody, values.attachments, values.variables, item.UpdatedBy))
	return updated, mapWriteError(err)
}

func (r *Repository) SetTemplateEnabled(ctx context.Context, siteID site.ID, id mail.TemplateID, enabled bool, updatedBy *security.UserID) (mail.Template, error) {
	updated, err := scanTemplate(r.connector.Pool().QueryRow(ctx, `UPDATE mail.templates SET enabled=$3,updated_at=clock_timestamp(),updated_by=$4 WHERE site_id=$1 AND id=$2 RETURNING `+templateColumns+`;`, siteID, id, enabled, updatedBy))
	return updated, mapWriteError(err)
}

func (r *Repository) DeleteTemplate(ctx context.Context, siteID site.ID, id mail.TemplateID) error {
	command, err := r.connector.Pool().Exec(ctx, `DELETE FROM mail.templates WHERE site_id=$1 AND id=$2;`, siteID, id)
	if err != nil {
		return fmt.Errorf("delete mail template: %w", err)
	}
	if command.RowsAffected() == 0 {
		return mail.ErrNotFound
	}
	return nil
}

type templateValues struct{ from, to, cc, bcc, replyTo, attachments, variables []byte }

func templateJSON(item mail.Template) (templateValues, error) {
	var result templateValues
	values := []struct {
		target *[]byte
		value  any
	}{{&result.from, item.From}, {&result.to, item.To}, {&result.cc, item.CC}, {&result.bcc, item.BCC}, {&result.attachments, item.Attachments}}
	if item.ReplyTo != nil {
		values = append(values, struct {
			target *[]byte
			value  any
		}{&result.replyTo, item.ReplyTo})
	}
	for _, value := range values {
		raw, err := encodeJSON(value.value)
		if err != nil {
			return templateValues{}, err
		}
		*value.target = raw
	}
	variables, err := encodeVariables(item.Variables)
	if err != nil {
		return templateValues{}, err
	}
	result.variables = variables
	return result, nil
}

type rowScanner interface{ Scan(...any) error }

func scanTemplate(row rowScanner) (mail.Template, error) {
	var item mail.Template
	var from, to, cc, bcc, replyTo, attachments, variables []byte
	err := row.Scan(&item.ID, &item.SiteID, &item.Code, &item.Name, &item.Enabled, &from, &to, &cc, &bcc, &replyTo, &item.Subject, &item.ContentType, &item.TextBody, &item.HTMLBody, &attachments, &variables, &item.CreatedAt, &item.UpdatedAt, &item.CreatedBy, &item.UpdatedBy)
	if err != nil {
		return mail.Template{}, err
	}
	if err := decodeJSON(from, &item.From); err != nil {
		return mail.Template{}, err
	}
	for _, pair := range []struct {
		raw    []byte
		target any
	}{{to, &item.To}, {cc, &item.CC}, {bcc, &item.BCC}, {attachments, &item.Attachments}} {
		if err := decodeJSON(pair.raw, pair.target); err != nil {
			return mail.Template{}, err
		}
	}
	if len(replyTo) > 0 {
		item.ReplyTo = &mail.AddressTemplate{}
		if err := decodeJSON(replyTo, item.ReplyTo); err != nil {
			return mail.Template{}, err
		}
	}
	item.Variables, err = decodeVariables(variables)
	return item, err
}

func scanTemplateWithTotal(row rowScanner) (mail.Template, int, error) {
	var item mail.Template
	var from, to, cc, bcc, replyTo, attachments, variables []byte
	var total int
	err := row.Scan(&item.ID, &item.SiteID, &item.Code, &item.Name, &item.Enabled, &from, &to, &cc, &bcc, &replyTo, &item.Subject, &item.ContentType, &item.TextBody, &item.HTMLBody, &attachments, &variables, &item.CreatedAt, &item.UpdatedAt, &item.CreatedBy, &item.UpdatedBy, &total)
	if err != nil {
		return mail.Template{}, 0, err
	}
	// Reuse decoding through an in-memory scanner without another query.
	decoded, err := decodeTemplateScanned(item, from, to, cc, bcc, replyTo, attachments, variables)
	return decoded, total, err
}

func decodeTemplateScanned(item mail.Template, from, to, cc, bcc, replyTo, attachments, variables []byte) (mail.Template, error) {
	if err := decodeJSON(from, &item.From); err != nil {
		return mail.Template{}, err
	}
	for _, pair := range []struct {
		raw    []byte
		target any
	}{{to, &item.To}, {cc, &item.CC}, {bcc, &item.BCC}, {attachments, &item.Attachments}} {
		if err := decodeJSON(pair.raw, pair.target); err != nil {
			return mail.Template{}, err
		}
	}
	if len(replyTo) > 0 {
		item.ReplyTo = &mail.AddressTemplate{}
		if err := decodeJSON(replyTo, item.ReplyTo); err != nil {
			return mail.Template{}, err
		}
	}
	var err error
	item.Variables, err = decodeVariables(variables)
	return item, err
}

const messageColumns = `
id,site_id,template_id,template_code,template_name,rfc_message_id,from_address,to_addresses,cc_addresses,bcc_addresses,reply_to,subject,content_type,text_body,html_body,attachments,status,origin,origin_source,origin_event,origin_reference,requested_at,requested_by,requested_by_name,accepted_at,created_at,updated_at`

func (r *Repository) CreateMessageAndJob(ctx context.Context, item mail.Message, busMessage eventbus.Message) (_ mail.Message, resultErr error) {
	values, err := messageJSON(item)
	if err != nil {
		return mail.Message{}, err
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return mail.Message{}, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(context.Background()); resultErr != nil && rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()
	created, err := scanMessage(tx.QueryRow(ctx, `
INSERT INTO mail.messages(site_id,template_id,template_code,template_name,rfc_message_id,from_address,to_addresses,cc_addresses,bcc_addresses,reply_to,subject,content_type,text_body,html_body,attachments,status,origin,origin_source,origin_event,origin_reference,requested_at,requested_by,requested_by_name)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,'queued',$16,$17,$18,$19,$20,$21,$22)
RETURNING `+messageColumns+`;`, item.SiteID, item.TemplateID, item.TemplateCode, item.TemplateName, item.RFCMessageID, values.from, values.to, values.cc, values.bcc, values.replyTo, item.Subject, item.ContentType, item.TextBody, item.HTMLBody, values.attachments, item.Origin, item.OriginSource, item.OriginEvent, item.OriginReference, item.RequestedAt, item.RequestedBy, item.RequestedByName))
	if err != nil {
		return mail.Message{}, mapWriteError(err)
	}
	var envelope job.Envelope
	if err := json.Unmarshal(busMessage.Body, &envelope); err != nil {
		return mail.Message{}, fmt.Errorf("decode queued mail job: %w", err)
	}
	payload, err := json.Marshal(struct {
		MessageID mail.MessageID `json:"message_id"`
	}{created.ID})
	if err != nil {
		return mail.Message{}, err
	}
	envelope.Payload = payload
	body, err := json.Marshal(envelope)
	if err != nil {
		return mail.Message{}, err
	}
	headers, err := json.Marshal(busMessage.Headers)
	if err != nil {
		return mail.Message{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO core.outbox_messages(message_id,topic,message_key,body,headers) VALUES($1,$2,$3,$4,$5);`, envelope.ID, busMessage.Topic, busMessage.Key, body, headers)
	if err != nil {
		return mail.Message{}, fmt.Errorf("insert mail outbox job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mail.Message{}, err
	}
	return created, nil
}

type messageValues struct{ from, to, cc, bcc, replyTo, attachments []byte }

func messageJSON(item mail.Message) (messageValues, error) {
	var result messageValues
	values := []struct {
		target *[]byte
		value  any
	}{{&result.from, item.From}, {&result.to, item.To}, {&result.cc, item.CC}, {&result.bcc, item.BCC}}
	if item.ReplyTo != nil {
		values = append(values, struct {
			target *[]byte
			value  any
		}{&result.replyTo, item.ReplyTo})
	}
	for _, value := range values {
		raw, err := encodeJSON(value.value)
		if err != nil {
			return messageValues{}, err
		}
		*value.target = raw
	}
	attachments, err := encodeMessageAttachments(item.Attachments)
	if err != nil {
		return messageValues{}, err
	}
	result.attachments = attachments
	return result, nil
}

func (r *Repository) ListMessages(ctx context.Context, siteID site.ID, query mail.MessageQuery) (mail.MessageSummaryPage, error) {
	rows, err := r.connector.Pool().Query(ctx, `SELECT id,template_code,template_name,subject,to_addresses,cc_addresses,bcc_addresses,status,origin,origin_source,origin_event,origin_reference,requested_at,requested_by,requested_by_name,accepted_at,count(*) OVER(),
(SELECT count(*) FROM mail.delivery_attempts a WHERE a.message_id=mail.messages.id),
(SELECT jsonb_build_object('id',a.id,'message_id',a.message_id,'attempt_number',a.attempt_number,'driver',a.driver,'started_at',a.started_at,'finished_at',a.finished_at,'status',a.status,'remote_message_id',a.remote_message_id,'response_code',a.response_code,'safe_error',a.safe_error,'created_at',a.created_at) FROM mail.delivery_attempts a WHERE a.message_id=mail.messages.id ORDER BY a.attempt_number DESC LIMIT 1)
FROM mail.messages WHERE site_id=$1
AND ($2='' OR status=$2)
AND ($3='' OR template_code=$3)
AND ($4::timestamptz IS NULL OR requested_at >= $4)
AND ($5::timestamptz IS NULL OR requested_at <= $5)
AND ($6='' OR EXISTS (SELECT 1 FROM jsonb_array_elements(to_addresses || cc_addresses || bcc_addresses) recipient WHERE recipient->>'email' ILIKE '%' || $6 || '%'))
ORDER BY created_at DESC,id DESC LIMIT $7 OFFSET $8;`, siteID, query.Status, query.TemplateCode, query.DateFrom, query.DateTo, query.Recipient, query.PerPage, (query.Page-1)*query.PerPage)
	if err != nil {
		return mail.MessageSummaryPage{}, err
	}
	defer rows.Close()
	result := mail.MessageSummaryPage{Items: []mail.MessageSummary{}}
	for rows.Next() {
		item, total, err := scanMessageSummary(rows)
		if err != nil {
			return mail.MessageSummaryPage{}, err
		}
		result.Items, result.Total = append(result.Items, item), total
	}
	return result, rows.Err()
}

func scanMessageSummary(row rowScanner) (mail.MessageSummary, int, error) {
	var item mail.MessageSummary
	var to, cc, bcc, latestAttempt []byte
	var total int
	err := row.Scan(&item.ID, &item.TemplateCode, &item.TemplateName, &item.Subject, &to, &cc, &bcc, &item.Status, &item.Origin, &item.OriginSource, &item.OriginEvent, &item.OriginReference, &item.RequestedAt, &item.RequestedBy, &item.RequestedByName, &item.AcceptedAt, &total, &item.AttemptCount, &latestAttempt)
	if err != nil {
		return mail.MessageSummary{}, 0, err
	}
	for _, raw := range [][]byte{to, cc, bcc} {
		var addresses []mail.Address
		if err := decodeJSON(raw, &addresses); err != nil {
			return mail.MessageSummary{}, 0, err
		}
		for _, address := range addresses {
			item.Recipients = append(item.Recipients, address.Email)
		}
	}
	if item.Recipients == nil {
		item.Recipients = []string{}
	}
	if len(latestAttempt) > 0 {
		item.LatestAttempt = &mail.DeliveryAttempt{}
		if err := json.Unmarshal(latestAttempt, item.LatestAttempt); err != nil {
			return mail.MessageSummary{}, 0, err
		}
	}
	return item, total, nil
}

func (r *Repository) MessageDetail(ctx context.Context, siteID site.ID, id mail.MessageID) (mail.MessageDetail, error) {
	message, err := scanMessage(r.connector.Pool().QueryRow(ctx, `SELECT `+messageColumns+` FROM mail.messages WHERE site_id=$1 AND id=$2;`, siteID, id))
	if err != nil {
		return mail.MessageDetail{}, mapNotFound(err)
	}
	rows, err := r.connector.Pool().Query(ctx, `SELECT id,message_id,attempt_number,driver,started_at,finished_at,status,remote_message_id,response_code,safe_error,created_at FROM mail.delivery_attempts WHERE message_id=$1 ORDER BY attempt_number DESC;`, id)
	if err != nil {
		return mail.MessageDetail{}, err
	}
	defer rows.Close()
	attempts := []mail.DeliveryAttempt{}
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return mail.MessageDetail{}, err
		}
		attempts = append(attempts, attempt)
	}
	message.AttemptCount = len(attempts)
	if len(attempts) > 0 {
		latest := attempts[0]
		message.LatestAttempt = &latest
	}
	return mail.MessageDetail{Message: message, Attempts: attempts}, rows.Err()
}

func (r *Repository) DeleteMessage(ctx context.Context, siteID site.ID, id mail.MessageID) error {
	command, err := r.connector.Pool().Exec(ctx, `DELETE FROM mail.messages WHERE site_id=$1 AND id=$2 AND status IN ('accepted','failed');`, siteID, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		var exists bool
		if queryErr := r.connector.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mail.messages WHERE site_id=$1 AND id=$2);`, siteID, id).Scan(&exists); queryErr != nil {
			return queryErr
		}
		if exists {
			return mail.ErrConflict
		}
		return mail.ErrNotFound
	}
	return nil
}

func (r *Repository) ClaimMessage(ctx context.Context, siteID site.ID, id mail.MessageID, maxAttempts int) (_ mail.Message, _ mail.DeliveryAttempt, _ bool, resultErr error) {
	if maxAttempts < 1 {
		return mail.Message{}, mail.DeliveryAttempt{}, false, errors.New("mail maximum attempts is invalid")
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return mail.Message{}, mail.DeliveryAttempt{}, false, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(context.Background()); resultErr != nil && rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()
	message, err := scanMessage(tx.QueryRow(ctx, `UPDATE mail.messages SET status='sending',updated_at=clock_timestamp() WHERE site_id=$1 AND id=$2 AND (status IN ('queued','retryable') OR (status='sending' AND updated_at <= clock_timestamp()-interval '10 minutes')) AND (SELECT count(*) FROM mail.delivery_attempts WHERE message_id=$2) < $3 RETURNING `+messageColumns+`;`, siteID, id, maxAttempts))
	if errors.Is(err, pgx.ErrNoRows) {
		command, terminalErr := tx.Exec(ctx, `UPDATE mail.messages SET status='failed',updated_at=clock_timestamp() WHERE site_id=$1 AND id=$2 AND (status IN ('queued','retryable') OR (status='sending' AND updated_at <= clock_timestamp()-interval '10 minutes')) AND (SELECT count(*) FROM mail.delivery_attempts WHERE message_id=$2) >= $3;`, siteID, id, maxAttempts)
		if terminalErr != nil {
			return mail.Message{}, mail.DeliveryAttempt{}, false, terminalErr
		}
		if command.RowsAffected() > 0 {
			if _, terminalErr = tx.Exec(ctx, `UPDATE mail.delivery_attempts SET finished_at=clock_timestamp(),status='failed',response_code='lease_expired',safe_error='delivery lease expired' WHERE message_id=$1 AND status='sending';`, id); terminalErr != nil {
				return mail.Message{}, mail.DeliveryAttempt{}, false, terminalErr
			}
		}
		var exists bool
		if queryErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mail.messages WHERE site_id=$1 AND id=$2);`, siteID, id).Scan(&exists); queryErr != nil {
			return mail.Message{}, mail.DeliveryAttempt{}, false, queryErr
		}
		if !exists {
			return mail.Message{}, mail.DeliveryAttempt{}, false, mail.ErrNotFound
		}
		if err := tx.Commit(ctx); err != nil {
			return mail.Message{}, mail.DeliveryAttempt{}, false, err
		}
		return mail.Message{}, mail.DeliveryAttempt{}, false, nil
	}
	if err != nil {
		return mail.Message{}, mail.DeliveryAttempt{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE mail.delivery_attempts SET finished_at=clock_timestamp(),status='failed',response_code='lease_expired',safe_error='delivery lease expired' WHERE message_id=$1 AND status='sending';`, id); err != nil {
		return mail.Message{}, mail.DeliveryAttempt{}, false, err
	}
	var attempt mail.DeliveryAttempt
	err = tx.QueryRow(ctx, `INSERT INTO mail.delivery_attempts(message_id,attempt_number,status) SELECT $1,coalesce(max(attempt_number),0)+1,'sending' FROM mail.delivery_attempts WHERE message_id=$1 RETURNING id,message_id,attempt_number,driver,started_at,finished_at,status,remote_message_id,response_code,safe_error,created_at;`, id).Scan(&attempt.ID, &attempt.MessageID, &attempt.AttemptNumber, &attempt.Driver, &attempt.StartedAt, &attempt.FinishedAt, &attempt.Status, &attempt.RemoteMessageID, &attempt.ResponseCode, &attempt.SafeError, &attempt.CreatedAt)
	if err != nil {
		return mail.Message{}, mail.DeliveryAttempt{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return mail.Message{}, mail.DeliveryAttempt{}, false, err
	}
	return message, attempt, true, nil
}

func (r *Repository) FinishAttempt(ctx context.Context, id mail.MessageID, number int, result mail.DeliveryResult, failure *mail.DeliveryError, terminal bool) (_ error) {
	status, messageStatus, safeError := mail.AttemptAccepted, mail.StatusAccepted, ""
	if failure != nil {
		messageStatus = mail.StatusFailed
		if failure.Retryable && !terminal {
			messageStatus = mail.StatusRetryable
		}
		status, safeError = mail.AttemptFailed, failure.Error()
		if result.ResponseCode == "" {
			result.ResponseCode = failure.Code
		}
	}
	if len(safeError) > 4096 {
		safeError = safeError[:4096]
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	command, err := tx.Exec(ctx, `UPDATE mail.delivery_attempts SET driver=$3,finished_at=clock_timestamp(),status=$4,remote_message_id=$5,response_code=$6,safe_error=$7 WHERE message_id=$1 AND attempt_number=$2 AND status='sending';`, id, number, result.Driver, status, result.RemoteMessageID, result.ResponseCode, safeError)
	if err != nil || command.RowsAffected() != 1 {
		return errors.Join(err, errors.New("mail delivery attempt is no longer claimable"))
	}
	command, err = tx.Exec(ctx, `UPDATE mail.messages SET status=$2,accepted_at=CASE WHEN $2='accepted' THEN clock_timestamp() ELSE accepted_at END,updated_at=clock_timestamp() WHERE id=$1 AND status='sending';`, id, messageStatus)
	if err != nil || command.RowsAffected() != 1 {
		return errors.Join(err, errors.New("mail message is no longer claimable"))
	}
	return tx.Commit(ctx)
}

func (r *Repository) HasActiveMessages(ctx context.Context, siteID site.ID) (bool, error) {
	if ctx == nil {
		return false, errors.New("mail active-message query context is nil")
	}
	var active bool
	if err := r.connector.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mail.messages WHERE site_id=$1 AND status IN ('queued','sending','retryable') LIMIT 1);`, siteID).Scan(&active); err != nil {
		return false, fmt.Errorf("query active mail messages: %w", err)
	}
	return active, nil
}

func (r *Repository) Cleanup(ctx context.Context, siteID site.ID, retention time.Duration, limit int) (int64, error) {
	if siteID <= 0 || retention < 0 || limit < 1 {
		return 0, errors.New("mail cleanup request is invalid")
	}
	if retention == 0 {
		return 0, nil
	}
	command, err := r.connector.Pool().Exec(ctx, `WITH candidates AS (SELECT id FROM mail.messages WHERE site_id=$1 AND status IN ('accepted','failed') AND updated_at <= clock_timestamp()-($2::bigint*interval '1 microsecond') ORDER BY updated_at,id LIMIT $3) DELETE FROM mail.messages USING candidates WHERE mail.messages.id=candidates.id;`, siteID, int64((retention+time.Microsecond-1)/time.Microsecond), limit)
	if err != nil {
		return 0, err
	}
	return command.RowsAffected(), nil
}

func (r *Repository) ActiveSpoolKeys(ctx context.Context, siteID site.ID, keys []string) (map[string]struct{}, error) {
	if siteID <= 0 {
		return nil, errors.New("mail active spool-key request is invalid")
	}
	result := make(map[string]struct{})
	if len(keys) == 0 {
		return result, nil
	}
	rows, err := r.connector.Pool().Query(ctx, `SELECT DISTINCT attachment->>'spool_key' FROM mail.messages CROSS JOIN LATERAL jsonb_array_elements(attachments) attachment WHERE site_id=$1 AND status IN ('queued','sending','retryable') AND attachment->>'spool_key'=ANY($2);`, siteID, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		result[key] = struct{}{}
	}
	return result, rows.Err()
}

func scanMessage(row rowScanner) (mail.Message, error) {
	var item mail.Message
	var from, to, cc, bcc, replyTo, attachments []byte
	err := row.Scan(&item.ID, &item.SiteID, &item.TemplateID, &item.TemplateCode, &item.TemplateName, &item.RFCMessageID, &from, &to, &cc, &bcc, &replyTo, &item.Subject, &item.ContentType, &item.TextBody, &item.HTMLBody, &attachments, &item.Status, &item.Origin, &item.OriginSource, &item.OriginEvent, &item.OriginReference, &item.RequestedAt, &item.RequestedBy, &item.RequestedByName, &item.AcceptedAt, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return mail.Message{}, err
	}
	return decodeMessageScanned(item, from, to, cc, bcc, replyTo, attachments)
}

func decodeMessageScanned(item mail.Message, from, to, cc, bcc, replyTo, attachments []byte) (mail.Message, error) {
	if err := decodeJSON(from, &item.From); err != nil {
		return mail.Message{}, err
	}
	for _, pair := range []struct {
		raw    []byte
		target any
	}{{to, &item.To}, {cc, &item.CC}, {bcc, &item.BCC}} {
		if err := decodeJSON(pair.raw, pair.target); err != nil {
			return mail.Message{}, err
		}
	}
	decodedAttachments, err := decodeMessageAttachments(attachments)
	if err != nil {
		return mail.Message{}, err
	}
	item.Attachments = decodedAttachments
	if len(replyTo) > 0 {
		item.ReplyTo = &mail.Address{}
		if err := decodeJSON(replyTo, item.ReplyTo); err != nil {
			return mail.Message{}, err
		}
	}
	return item, nil
}

func scanAttempt(row rowScanner) (mail.DeliveryAttempt, error) {
	var item mail.DeliveryAttempt
	err := row.Scan(&item.ID, &item.MessageID, &item.AttemptNumber, &item.Driver, &item.StartedAt, &item.FinishedAt, &item.Status, &item.RemoteMessageID, &item.ResponseCode, &item.SafeError, &item.CreatedAt)
	return item, err
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return mail.ErrNotFound
	}
	return err
}

func mapWriteError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return mail.ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case pgerrcode.UniqueViolation:
			return mail.ErrConflict
		case pgerrcode.ForeignKeyViolation:
			return mail.ErrInvalid
		}
	}
	return err
}

var _ mail.Repository = (*Repository)(nil)
