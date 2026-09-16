package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/forms"
	"github.com/vernal96/go-cms-kernel/security"
)

type Repository struct{ connector *connectorpostgres.Connector }

func NewRepository(connector *connectorpostgres.Connector) (*Repository, error) {
	if connector == nil || connector.Pool() == nil {
		return nil, errors.New("Forms PostgreSQL connector is nil")
	}
	return &Repository{connector: connector}, nil
}

type rowScanner interface{ Scan(...any) error }
type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

const formColumns = `id,site_id,code,name,description,enabled,created_at,updated_at,created_by,updated_by`
const fieldColumns = `id,form_id,code,type,label,required,rules,options,editor,visible_when,result_label,show_in_results,show_on_site,result_position,created_at,updated_at`
const elementColumns = `id,form_id,code,type,config,created_at,updated_at`
const layoutColumns = `id,form_id,parent_id,kind,field_id,element_id,container_type,position,config`
const statusColumns = `id,form_id,code,name,color,position,is_default,created_at,updated_at`
const actionColumns = `id,form_id,code,name,enabled,trigger,action_type,config,position,created_at,updated_at`
const resultColumns = `r.id,r.site_id,r.form_id,r.form_code,r.form_name,r.status_id,s.code,s.name,s.color,r.user_id,r.user_agent,r.client_address,r.created_at,r.updated_at`
const executionColumns = `id,site_id,result_id,action_id,action_code,action_name,action_type,trigger,config,status,attempt_count,safe_error,external_reference,started_at,finished_at,created_at,updated_at`

func scanForm(row rowScanner) (forms.Form, error) {
	var item forms.Form
	err := row.Scan(&item.ID, &item.SiteID, &item.Code, &item.Name, &item.Description, &item.Enabled, &item.CreatedAt, &item.UpdatedAt, &item.CreatedBy, &item.UpdatedBy)
	return item, err
}

func (r *Repository) ListForms(ctx context.Context, siteID site.ID, query forms.PageQuery) (forms.FormSummaryPage, error) {
	rows, err := r.connector.Pool().Query(ctx, `SELECT `+formColumns+`,count(*) OVER() FROM forms.forms WHERE site_id=$1 AND ($2='' OR code ILIKE '%'||$2||'%' OR name ILIKE '%'||$2||'%') ORDER BY name,id LIMIT $3 OFFSET $4;`, siteID, query.Search, query.PerPage, (query.Page-1)*query.PerPage)
	if err != nil {
		return forms.FormSummaryPage{}, err
	}
	defer rows.Close()
	result := forms.FormSummaryPage{Items: []forms.Form{}}
	for rows.Next() {
		var item forms.Form
		var total int
		if err := rows.Scan(&item.ID, &item.SiteID, &item.Code, &item.Name, &item.Description, &item.Enabled, &item.CreatedAt, &item.UpdatedAt, &item.CreatedBy, &item.UpdatedBy, &total); err != nil {
			return forms.FormSummaryPage{}, err
		}
		result.Items, result.Total = append(result.Items, item), total
	}
	return result, rows.Err()
}

func (r *Repository) FormByID(ctx context.Context, siteID site.ID, id forms.FormID) (forms.Form, error) {
	item, err := scanForm(r.connector.Pool().QueryRow(ctx, `SELECT `+formColumns+` FROM forms.forms WHERE site_id=$1 AND id=$2;`, siteID, id))
	return item, mapNotFound(err)
}
func (r *Repository) FormByCode(ctx context.Context, siteID site.ID, code string, enabledOnly bool) (forms.Form, error) {
	clause := ""
	if enabledOnly {
		clause = " AND enabled"
	}
	item, err := scanForm(r.connector.Pool().QueryRow(ctx, `SELECT `+formColumns+` FROM forms.forms WHERE site_id=$1 AND code=$2`+clause+`;`, siteID, code))
	return item, mapNotFound(err)
}
func (r *Repository) FormDetail(ctx context.Context, siteID site.ID, id forms.FormID) (forms.FormDetail, error) {
	return formDetail(ctx, r.connector.Pool(), siteID, id, "", false)
}
func (r *Repository) FormDetailByCode(ctx context.Context, siteID site.ID, code string, enabledOnly bool) (forms.FormDetail, error) {
	return formDetail(ctx, r.connector.Pool(), siteID, 0, code, enabledOnly)
}

func formDetail(ctx context.Context, q querier, siteID site.ID, id forms.FormID, code string, enabledOnly bool) (forms.FormDetail, error) {
	where := "site_id=$1 AND id=$2"
	argument := any(id)
	if code != "" {
		where, argument = "site_id=$1 AND code=$2", code
	}
	if enabledOnly {
		where += " AND enabled"
	}
	item, err := scanForm(q.QueryRow(ctx, `SELECT `+formColumns+` FROM forms.forms WHERE `+where+`;`, siteID, argument))
	if err != nil {
		return forms.FormDetail{}, mapNotFound(err)
	}
	fields, err := listFields(ctx, q, item.ID)
	if err != nil {
		return forms.FormDetail{}, err
	}
	elements, err := listElements(ctx, q, item.ID)
	if err != nil {
		return forms.FormDetail{}, err
	}
	layout, err := listLayout(ctx, q, item.ID)
	if err != nil {
		return forms.FormDetail{}, err
	}
	statuses, err := listStatuses(ctx, q, item.ID)
	if err != nil {
		return forms.FormDetail{}, err
	}
	actions, err := listActions(ctx, q, item.ID)
	if err != nil {
		return forms.FormDetail{}, err
	}
	return forms.FormDetail{Form: item, Fields: fields, Elements: elements, Layout: layout, Statuses: statuses, Actions: actions}, nil
}

func scanField(row rowScanner) (forms.FormField, error) {
	var item forms.FormField
	var rules, options, visible []byte
	err := row.Scan(&item.ID, &item.FormID, &item.Code, &item.Type, &item.Label, &item.Required, &rules, &options, &item.Editor, &visible, &item.ResultLabel, &item.ShowInResults, &item.ShowOnSite, &item.ResultPosition, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return forms.FormField{}, err
	}
	if err := json.Unmarshal(rules, &item.Rules); err != nil {
		return forms.FormField{}, err
	}
	item.Options, err = decodeFieldOptions(item.Type, options)
	if err != nil {
		return forms.FormField{}, err
	}
	if len(visible) > 0 && string(visible) != "null" {
		item.VisibleWhen = &field.VisibleWhen{}
		if err := json.Unmarshal(visible, item.VisibleWhen); err != nil {
			return forms.FormField{}, err
		}
	}
	return item, nil
}

func listFields(ctx context.Context, q querier, formID forms.FormID) ([]forms.FormField, error) {
	rows, err := q.Query(ctx, `SELECT `+fieldColumns+` FROM forms.fields WHERE form_id=$1 ORDER BY result_position,id;`, formID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []forms.FormField{}
	for rows.Next() {
		item, err := scanField(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func scanElement(row rowScanner) (forms.Element, error) {
	var item forms.Element
	err := row.Scan(&item.ID, &item.FormID, &item.Code, &item.Type, &item.Config, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}
func listElements(ctx context.Context, q querier, formID forms.FormID) ([]forms.Element, error) {
	rows, err := q.Query(ctx, `SELECT `+elementColumns+` FROM forms.elements WHERE form_id=$1 ORDER BY id;`, formID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []forms.Element{}
	for rows.Next() {
		item, err := scanElement(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func scanLayout(row rowScanner) (forms.LayoutNode, error) {
	var item forms.LayoutNode
	err := row.Scan(&item.ID, &item.FormID, &item.ParentID, &item.Kind, &item.FieldID, &item.ElementID, &item.ContainerType, &item.Position, &item.Config)
	return item, err
}
func listLayout(ctx context.Context, q querier, formID forms.FormID) ([]forms.LayoutNode, error) {
	rows, err := q.Query(ctx, `SELECT `+layoutColumns+` FROM forms.layout_nodes WHERE form_id=$1 ORDER BY coalesce(parent_id,0),position,id;`, formID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []forms.LayoutNode{}
	for rows.Next() {
		item, err := scanLayout(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func scanStatus(row rowScanner) (forms.Status, error) {
	var item forms.Status
	err := row.Scan(&item.ID, &item.FormID, &item.Code, &item.Name, &item.Color, &item.Position, &item.IsDefault, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}
func listStatuses(ctx context.Context, q querier, formID forms.FormID) ([]forms.Status, error) {
	rows, err := q.Query(ctx, `SELECT `+statusColumns+` FROM forms.statuses WHERE form_id=$1 ORDER BY position,id;`, formID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []forms.Status{}
	for rows.Next() {
		item, err := scanStatus(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
func scanAction(row rowScanner) (forms.Action, error) {
	var item forms.Action
	var trigger []byte
	err := row.Scan(&item.ID, &item.FormID, &item.Code, &item.Name, &item.Enabled, &trigger, &item.ActionType, &item.Config, &item.Position, &item.CreatedAt, &item.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(trigger, &item.Trigger)
	}
	return item, err
}
func listActions(ctx context.Context, q querier, formID forms.FormID) ([]forms.Action, error) {
	rows, err := q.Query(ctx, `SELECT `+actionColumns+` FROM forms.actions WHERE form_id=$1 ORDER BY position,id;`, formID)
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

func (r *Repository) CreateForm(ctx context.Context, input forms.CreateFormInput) (_ forms.FormDetail, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return forms.FormDetail{}, err
	}
	defer rollback(ctx, tx, &resultErr)
	created, err := scanForm(tx.QueryRow(ctx, `INSERT INTO forms.forms(site_id,code,name,description,enabled,created_by,updated_by) VALUES($1,$2,$3,$4,$5,$6,$6) RETURNING `+formColumns+`;`, input.Form.SiteID, input.Form.Code, input.Form.Name, input.Form.Description, input.Form.Enabled, input.Form.CreatedBy))
	if err != nil {
		return forms.FormDetail{}, mapWriteError(err)
	}
	input.Consent.FormID, input.Captcha.FormID, input.Submit.FormID, input.Status.FormID = created.ID, created.ID, created.ID, created.ID
	consent, err := insertField(ctx, tx, input.Consent)
	if err != nil {
		return forms.FormDetail{}, err
	}
	captcha, err := insertField(ctx, tx, input.Captcha)
	if err != nil {
		return forms.FormDetail{}, err
	}
	submit, err := insertElement(ctx, tx, input.Submit)
	if err != nil {
		return forms.FormDetail{}, err
	}
	status, err := insertStatus(ctx, tx, input.Status)
	if err != nil {
		return forms.FormDetail{}, err
	}
	layout := make([]forms.LayoutNode, 3)
	for index, node := range []forms.LayoutNode{{FormID: created.ID, Kind: forms.LayoutField, FieldID: &consent.ID, Position: 0}, {FormID: created.ID, Kind: forms.LayoutField, FieldID: &captcha.ID, Position: 1}, {FormID: created.ID, Kind: forms.LayoutElement, ElementID: &submit.ID, Position: 2}} {
		layout[index], err = insertLayout(ctx, tx, node)
		if err != nil {
			return forms.FormDetail{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return forms.FormDetail{}, err
	}
	return forms.FormDetail{Form: created, Fields: []forms.FormField{consent, captcha}, Elements: []forms.Element{submit}, Layout: layout, Statuses: []forms.Status{status}, Actions: []forms.Action{}}, nil
}

func insertField(ctx context.Context, tx pgx.Tx, item forms.FormField) (forms.FormField, error) {
	rules, _ := json.Marshal(item.Rules)
	options, err := encodeFieldOptions(item)
	if err != nil {
		return forms.FormField{}, err
	}
	visible, err := nullableJSON(item.VisibleWhen)
	if err != nil {
		return forms.FormField{}, err
	}
	created, err := scanField(tx.QueryRow(ctx, `INSERT INTO forms.fields(form_id,code,type,label,required,rules,options,editor,visible_when,result_label,show_in_results,show_on_site,result_position) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING `+fieldColumns+`;`, item.FormID, item.Code, item.Type, item.Label, item.Required, rules, options, item.Editor, visible, item.ResultLabel, item.ShowInResults, item.ShowOnSite, item.ResultPosition))
	return created, mapWriteError(err)
}
func insertElement(ctx context.Context, tx pgx.Tx, item forms.Element) (forms.Element, error) {
	created, err := scanElement(tx.QueryRow(ctx, `INSERT INTO forms.elements(form_id,code,type,config) VALUES($1,$2,$3,$4) RETURNING `+elementColumns+`;`, item.FormID, item.Code, item.Type, item.Config))
	return created, mapWriteError(err)
}
func insertStatus(ctx context.Context, tx pgx.Tx, item forms.Status) (forms.Status, error) {
	created, err := scanStatus(tx.QueryRow(ctx, `INSERT INTO forms.statuses(form_id,code,name,color,position,is_default) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+statusColumns+`;`, item.FormID, item.Code, item.Name, item.Color, item.Position, item.IsDefault))
	return created, mapWriteError(err)
}
func insertLayout(ctx context.Context, tx pgx.Tx, item forms.LayoutNode) (forms.LayoutNode, error) {
	config := item.Config
	if len(config) == 0 {
		config = []byte(`{}`)
	}
	created, err := scanLayout(tx.QueryRow(ctx, `INSERT INTO forms.layout_nodes(form_id,parent_id,kind,field_id,element_id,container_type,position,config) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+layoutColumns+`;`, item.FormID, item.ParentID, item.Kind, item.FieldID, item.ElementID, item.ContainerType, item.Position, config))
	return created, mapWriteError(err)
}
func nullableJSON(value *field.VisibleWhen) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

func (r *Repository) UpdateForm(ctx context.Context, item forms.Form) (forms.Form, error) {
	updated, err := scanForm(r.connector.Pool().QueryRow(ctx, `UPDATE forms.forms SET code=$3,name=$4,description=$5,enabled=$6,updated_at=clock_timestamp(),updated_by=$7 WHERE site_id=$1 AND id=$2 RETURNING `+formColumns+`;`, item.SiteID, item.ID, item.Code, item.Name, item.Description, item.Enabled, item.UpdatedBy))
	return updated, mapWriteError(err)
}
func (r *Repository) SetFormEnabled(ctx context.Context, siteID site.ID, id forms.FormID, enabled bool, actor *security.UserID) (forms.Form, error) {
	updated, err := scanForm(r.connector.Pool().QueryRow(ctx, `UPDATE forms.forms SET enabled=$3,updated_at=clock_timestamp(),updated_by=$4 WHERE site_id=$1 AND id=$2 RETURNING `+formColumns+`;`, siteID, id, enabled, actor))
	return updated, mapWriteError(err)
}

func (r *Repository) DeleteForm(ctx context.Context, siteID site.ID, id forms.FormID) (_ []string, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(ctx, tx, &resultErr)
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM forms.action_executions e JOIN forms.results r ON r.id=e.result_id WHERE r.site_id=$1 AND r.form_id=$2 AND e.status IN ('pending','running','retryable'));`, siteID, id).Scan(&active); err != nil {
		return nil, err
	}
	if active {
		return nil, forms.ErrActiveExecutions
	}
	keys, err := spoolReferencesForForm(ctx, tx, siteID, id)
	if err != nil {
		return nil, err
	}
	command, err := tx.Exec(ctx, `DELETE FROM forms.forms WHERE site_id=$1 AND id=$2;`, siteID, id)
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

func spoolReferencesForForm(ctx context.Context, q querier, siteID site.ID, formID forms.FormID) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT u.spool_reference FROM forms.result_uploads u JOIN forms.results r ON r.id=u.result_id WHERE r.site_id=$1 AND r.form_id=$2 AND u.spool_reference IS NOT NULL AND u.spool_deleted_at IS NULL;`, siteID, formID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		result = append(result, key)
	}
	return result, rows.Err()
}

func rollback(ctx context.Context, tx pgx.Tx, resultErr *error) {
	rollbackErr := tx.Rollback(context.Background())
	if *resultErr != nil && rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
		*resultErr = errors.Join(*resultErr, rollbackErr)
	}
}

func lockOwnedForm(ctx context.Context, tx pgx.Tx, siteID site.ID, formID forms.FormID) error {
	var value forms.FormID
	err := tx.QueryRow(ctx, `SELECT id FROM forms.forms WHERE site_id=$1 AND id=$2 FOR UPDATE;`, siteID, formID).Scan(&value)
	return mapNotFound(err)
}

func validatePlacement(ctx context.Context, tx pgx.Tx, formID forms.FormID, placement forms.LayoutPlacement) error {
	if placement.ParentID != nil {
		var kind forms.LayoutKind
		if err := tx.QueryRow(ctx, `SELECT kind FROM forms.layout_nodes WHERE id=$1 AND form_id=$2;`, *placement.ParentID, formID).Scan(&kind); err != nil {
			return mapNotFound(err)
		}
		if kind != forms.LayoutContainer {
			return forms.ErrInvalid
		}
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM forms.layout_nodes WHERE form_id=$1 AND parent_id IS NOT DISTINCT FROM $2;`, formID, placement.ParentID).Scan(&count); err != nil {
		return err
	}
	if placement.Position < 0 || placement.Position > count {
		return forms.ErrInvalid
	}
	return nil
}

func shiftSiblingPositions(ctx context.Context, tx pgx.Tx, formID forms.FormID, parentID *forms.LayoutNodeID, from, delta int) error {
	if delta == 0 {
		return nil
	}
	if delta > 0 {
		if _, err := tx.Exec(ctx, `UPDATE forms.layout_nodes SET position=position+1000000 WHERE form_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND position >= $3;`, formID, parentID, from); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE forms.layout_nodes SET position=position-1000000+$4 WHERE form_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND position >= $3+1000000;`, formID, parentID, from, delta)
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE forms.layout_nodes SET position=position+1000000 WHERE form_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND position > $3;`, formID, parentID, from); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE forms.layout_nodes SET position=position-1000000+$4 WHERE form_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND position > $3+1000000;`, formID, parentID, from, delta)
	return err
}

func (r *Repository) CreateField(ctx context.Context, siteID site.ID, formID forms.FormID, item forms.FormField, placement forms.LayoutPlacement) (_ forms.FormField, _ forms.LayoutNode, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return forms.FormField{}, forms.LayoutNode{}, err
	}
	defer rollback(ctx, tx, &resultErr)
	if err := lockOwnedForm(ctx, tx, siteID, formID); err != nil {
		return forms.FormField{}, forms.LayoutNode{}, err
	}
	if err := validatePlacement(ctx, tx, formID, placement); err != nil {
		return forms.FormField{}, forms.LayoutNode{}, err
	}
	if err := shiftSiblingPositions(ctx, tx, formID, placement.ParentID, placement.Position, 1); err != nil {
		return forms.FormField{}, forms.LayoutNode{}, err
	}
	item.FormID = formID
	created, err := insertField(ctx, tx, item)
	if err != nil {
		return forms.FormField{}, forms.LayoutNode{}, err
	}
	node, err := insertLayout(ctx, tx, forms.LayoutNode{FormID: formID, Kind: forms.LayoutField, FieldID: &created.ID, ParentID: placement.ParentID, Position: placement.Position})
	if err != nil {
		return forms.FormField{}, forms.LayoutNode{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return forms.FormField{}, forms.LayoutNode{}, err
	}
	return created, node, nil
}

func (r *Repository) UpdateField(ctx context.Context, siteID site.ID, item forms.FormField) (forms.FormField, error) {
	rules, _ := json.Marshal(item.Rules)
	options, err := encodeFieldOptions(item)
	if err != nil {
		return forms.FormField{}, err
	}
	visible, err := nullableJSON(item.VisibleWhen)
	if err != nil {
		return forms.FormField{}, err
	}
	updated, err := scanField(r.connector.Pool().QueryRow(ctx, `UPDATE forms.fields SET code=$4,type=$5,label=$6,required=$7,rules=$8,options=$9,editor=$10,visible_when=$11,result_label=$12,show_in_results=$13,show_on_site=$14,result_position=$15,updated_at=clock_timestamp() WHERE id=$2 AND form_id=$3 AND EXISTS(SELECT 1 FROM forms.forms WHERE id=$3 AND site_id=$1) RETURNING `+fieldColumns+`;`, siteID, item.ID, item.FormID, item.Code, item.Type, item.Label, item.Required, rules, options, item.Editor, visible, item.ResultLabel, item.ShowInResults, item.ShowOnSite, item.ResultPosition))
	return updated, mapWriteError(err)
}

func (r *Repository) DeleteField(ctx context.Context, siteID site.ID, formID forms.FormID, id forms.FieldID) (_ error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	var resultErr error
	defer rollback(ctx, tx, &resultErr)
	if err := lockOwnedForm(ctx, tx, siteID, formID); err != nil {
		resultErr = err
		return resultErr
	}
	var parent *forms.LayoutNodeID
	var position int
	if err := tx.QueryRow(ctx, `SELECT parent_id,position FROM forms.layout_nodes WHERE form_id=$1 AND field_id=$2;`, formID, id).Scan(&parent, &position); err != nil {
		resultErr = mapNotFound(err)
		return resultErr
	}
	command, err := tx.Exec(ctx, `DELETE FROM forms.fields WHERE form_id=$1 AND id=$2;`, formID, id)
	if err != nil {
		resultErr = mapWriteError(err)
		return resultErr
	}
	if command.RowsAffected() == 0 {
		resultErr = forms.ErrNotFound
		return resultErr
	}
	if err := shiftSiblingPositions(ctx, tx, formID, parent, position, -1); err != nil {
		resultErr = err
		return resultErr
	}
	resultErr = tx.Commit(ctx)
	return resultErr
}

func (r *Repository) CreateElement(ctx context.Context, siteID site.ID, formID forms.FormID, item forms.Element, placement forms.LayoutPlacement) (_ forms.Element, _ forms.LayoutNode, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return forms.Element{}, forms.LayoutNode{}, err
	}
	defer rollback(ctx, tx, &resultErr)
	if err := lockOwnedForm(ctx, tx, siteID, formID); err != nil {
		return forms.Element{}, forms.LayoutNode{}, err
	}
	if err := validatePlacement(ctx, tx, formID, placement); err != nil {
		return forms.Element{}, forms.LayoutNode{}, err
	}
	if err := shiftSiblingPositions(ctx, tx, formID, placement.ParentID, placement.Position, 1); err != nil {
		return forms.Element{}, forms.LayoutNode{}, err
	}
	item.FormID = formID
	created, err := insertElement(ctx, tx, item)
	if err != nil {
		return forms.Element{}, forms.LayoutNode{}, err
	}
	node, err := insertLayout(ctx, tx, forms.LayoutNode{FormID: formID, Kind: forms.LayoutElement, ElementID: &created.ID, ParentID: placement.ParentID, Position: placement.Position})
	if err != nil {
		return forms.Element{}, forms.LayoutNode{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return forms.Element{}, forms.LayoutNode{}, err
	}
	return created, node, nil
}

func (r *Repository) UpdateElement(ctx context.Context, siteID site.ID, item forms.Element) (forms.Element, error) {
	updated, err := scanElement(r.connector.Pool().QueryRow(ctx, `UPDATE forms.elements SET code=$4,type=$5,config=$6,updated_at=clock_timestamp() WHERE id=$2 AND form_id=$3 AND EXISTS(SELECT 1 FROM forms.forms WHERE id=$3 AND site_id=$1) RETURNING `+elementColumns+`;`, siteID, item.ID, item.FormID, item.Code, item.Type, item.Config))
	return updated, mapWriteError(err)
}

func (r *Repository) DeleteElement(ctx context.Context, siteID site.ID, formID forms.FormID, id forms.ElementID) (_ error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	var resultErr error
	defer rollback(ctx, tx, &resultErr)
	if err := lockOwnedForm(ctx, tx, siteID, formID); err != nil {
		resultErr = err
		return resultErr
	}
	var parent *forms.LayoutNodeID
	var position int
	if err := tx.QueryRow(ctx, `SELECT parent_id,position FROM forms.layout_nodes WHERE form_id=$1 AND element_id=$2;`, formID, id).Scan(&parent, &position); err != nil {
		resultErr = mapNotFound(err)
		return resultErr
	}
	command, err := tx.Exec(ctx, `DELETE FROM forms.elements WHERE form_id=$1 AND id=$2;`, formID, id)
	if err != nil {
		resultErr = mapWriteError(err)
		return resultErr
	}
	if command.RowsAffected() == 0 {
		resultErr = forms.ErrNotFound
		return resultErr
	}
	if err := shiftSiblingPositions(ctx, tx, formID, parent, position, -1); err != nil {
		resultErr = err
		return resultErr
	}
	resultErr = tx.Commit(ctx)
	return resultErr
}

func (r *Repository) CreateContainer(ctx context.Context, siteID site.ID, formID forms.FormID, item forms.LayoutNode) (_ forms.LayoutNode, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return forms.LayoutNode{}, err
	}
	defer rollback(ctx, tx, &resultErr)
	if err := lockOwnedForm(ctx, tx, siteID, formID); err != nil {
		return forms.LayoutNode{}, err
	}
	if err := validatePlacement(ctx, tx, formID, forms.LayoutPlacement{ParentID: item.ParentID, Position: item.Position}); err != nil {
		return forms.LayoutNode{}, err
	}
	if err := shiftSiblingPositions(ctx, tx, formID, item.ParentID, item.Position, 1); err != nil {
		return forms.LayoutNode{}, err
	}
	item.FormID = formID
	created, err := insertLayout(ctx, tx, item)
	if err != nil {
		return forms.LayoutNode{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return forms.LayoutNode{}, err
	}
	return created, nil
}

// DeleteContainer unwraps its direct children before deleting the container,
// so the parent foreign key never cascades into retained content.
func (r *Repository) DeleteContainer(ctx context.Context, siteID site.ID, formID forms.FormID, id forms.LayoutNodeID) (resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx, &resultErr)
	if err := lockOwnedForm(ctx, tx, siteID, formID); err != nil {
		return err
	}
	nodes, err := listLayout(ctx, tx, formID)
	if err != nil {
		return err
	}
	var target *forms.LayoutNode
	for i := range nodes {
		if nodes[i].ID == id {
			target = &nodes[i]
			break
		}
	}
	if target == nil {
		return forms.ErrNotFound
	}
	if target.Kind != forms.LayoutContainer {
		return forms.ErrInvalid
	}
	sameParent := func(a, b *forms.LayoutNodeID) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
	var siblings, children []forms.LayoutNode
	for _, node := range nodes {
		if sameParent(node.ParentID, target.ParentID) {
			siblings = append(siblings, node)
		}
		if node.ParentID != nil && *node.ParentID == id {
			children = append(children, node)
		}
	}
	sort.Slice(siblings, func(i, j int) bool { return siblings[i].Position < siblings[j].Position })
	sort.Slice(children, func(i, j int) bool { return children[i].Position < children[j].Position })
	var desired []forms.LayoutNode
	for _, node := range siblings {
		if node.ID == id {
			desired = append(desired, children...)
		} else {
			desired = append(desired, node)
		}
	}
	// Vacate the destination positions, including the deleted container's position.
	if _, err := tx.Exec(ctx, `WITH moved AS (SELECT id,row_number() OVER (ORDER BY id) AS n FROM forms.layout_nodes WHERE form_id=$1)
 UPDATE forms.layout_nodes SET position=1000000000+moved.n FROM moved WHERE layout_nodes.id=moved.id;`, formID); err != nil {
		return err
	}
	for position, node := range desired {
		if _, err := tx.Exec(ctx, `UPDATE forms.layout_nodes SET parent_id=$3,position=$4 WHERE form_id=$1 AND id=$2;`, formID, node.ID, target.ParentID, position); err != nil {
			return mapWriteError(err)
		}
	}
	// Restore the positions of all other branches, including grandchildren.
	for _, node := range nodes {
		if node.ID == id || sameParent(node.ParentID, target.ParentID) || node.ParentID != nil && *node.ParentID == id {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE forms.layout_nodes SET position=$3 WHERE form_id=$1 AND id=$2;`, formID, node.ID, node.Position); err != nil {
			return mapWriteError(err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM forms.layout_nodes WHERE form_id=$1 AND id=$2;`, formID, id); err != nil {
		return mapWriteError(err)
	}
	return tx.Commit(ctx)
}

func (r *Repository) ReplaceLayout(ctx context.Context, siteID site.ID, formID forms.FormID, items []forms.LayoutNode) (_ []forms.LayoutNode, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(ctx, tx, &resultErr)
	if err := lockOwnedForm(ctx, tx, siteID, formID); err != nil {
		return nil, err
	}
	locked, err := tx.Query(ctx, `SELECT id FROM forms.layout_nodes WHERE form_id=$1 FOR UPDATE;`, formID)
	if err != nil {
		return nil, err
	}
	count := 0
	for locked.Next() {
		count++
	}
	lockErr := locked.Err()
	locked.Close()
	if lockErr != nil {
		return nil, lockErr
	}
	if count != len(items) {
		return nil, forms.ErrConflict
	}
	if _, err := tx.Exec(ctx, `WITH moved AS (
SELECT id,row_number() OVER (ORDER BY id) AS temporary_position
FROM forms.layout_nodes WHERE form_id=$1
)
UPDATE forms.layout_nodes n
SET parent_id=NULL,position=1000000000+moved.temporary_position
FROM moved WHERE n.id=moved.id;`, formID); err != nil {
		return nil, err
	}
	for _, item := range items {
		command, err := tx.Exec(ctx, `UPDATE forms.layout_nodes SET parent_id=$3,position=$4,config=$5 WHERE form_id=$1 AND id=$2;`, formID, item.ID, item.ParentID, item.Position, defaultJSON(item.Config))
		if err != nil {
			return nil, mapWriteError(err)
		}
		if command.RowsAffected() != 1 {
			return nil, forms.ErrConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return listLayout(ctx, r.connector.Pool(), formID)
}

func defaultJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}

func (r *Repository) CreateStatus(ctx context.Context, siteID site.ID, formID forms.FormID, item forms.Status) (_ forms.Status, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return forms.Status{}, err
	}
	defer rollback(ctx, tx, &resultErr)
	if err := lockOwnedForm(ctx, tx, siteID, formID); err != nil {
		return forms.Status{}, err
	}
	if item.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE forms.statuses SET is_default=FALSE,updated_at=clock_timestamp() WHERE form_id=$1 AND is_default;`, formID); err != nil {
			return forms.Status{}, err
		}
	}
	item.FormID = formID
	created, err := insertStatus(ctx, tx, item)
	if err != nil {
		return forms.Status{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return forms.Status{}, err
	}
	return created, nil
}

func (r *Repository) UpdateStatus(ctx context.Context, siteID site.ID, item forms.Status) (_ forms.Status, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return forms.Status{}, err
	}
	defer rollback(ctx, tx, &resultErr)
	if err := lockOwnedForm(ctx, tx, siteID, item.FormID); err != nil {
		return forms.Status{}, err
	}
	if item.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE forms.statuses SET is_default=FALSE,updated_at=clock_timestamp() WHERE form_id=$1 AND id<>$2 AND is_default;`, item.FormID, item.ID); err != nil {
			return forms.Status{}, err
		}
	}
	updated, err := scanStatus(tx.QueryRow(ctx, `UPDATE forms.statuses SET code=$3,name=$4,color=$5,position=$6,is_default=$7,updated_at=clock_timestamp() WHERE form_id=$1 AND id=$2 RETURNING `+statusColumns+`;`, item.FormID, item.ID, item.Code, item.Name, item.Color, item.Position, item.IsDefault))
	if err != nil {
		return forms.Status{}, mapWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return forms.Status{}, err
	}
	return updated, nil
}

func (r *Repository) DeleteStatus(ctx context.Context, siteID site.ID, formID forms.FormID, id forms.StatusID) error {
	command, err := r.connector.Pool().Exec(ctx, `DELETE FROM forms.statuses s USING forms.forms f WHERE s.form_id=$2 AND s.id=$3 AND NOT s.is_default AND f.id=s.form_id AND f.site_id=$1;`, siteID, formID, id)
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == pgerrcode.ForeignKeyViolation {
			return forms.ErrConflict
		}
		return mapWriteError(err)
	}
	if command.RowsAffected() == 0 {
		return forms.ErrConflict
	}
	return nil
}

func (r *Repository) CreateAction(ctx context.Context, siteID site.ID, formID forms.FormID, item forms.Action) (forms.Action, error) {
	var exists bool
	if err := r.connector.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM forms.forms WHERE site_id=$1 AND id=$2);`, siteID, formID).Scan(&exists); err != nil {
		return forms.Action{}, err
	}
	if !exists {
		return forms.Action{}, forms.ErrNotFound
	}
	item.FormID = formID
	return insertAction(ctx, r.connector.Pool(), item)
}
func insertAction(ctx context.Context, q querier, item forms.Action) (forms.Action, error) {
	trigger, err := json.Marshal(item.Trigger)
	if err != nil {
		return forms.Action{}, err
	}
	created, err := scanAction(q.QueryRow(ctx, `INSERT INTO forms.actions(form_id,code,name,enabled,trigger,action_type,config,position) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+actionColumns+`;`, item.FormID, item.Code, item.Name, item.Enabled, trigger, item.ActionType, item.Config, item.Position))
	return created, mapWriteError(err)
}
func (r *Repository) UpdateAction(ctx context.Context, siteID site.ID, item forms.Action) (forms.Action, error) {
	trigger, err := json.Marshal(item.Trigger)
	if err != nil {
		return forms.Action{}, err
	}
	updated, err := scanAction(r.connector.Pool().QueryRow(ctx, `UPDATE forms.actions SET code=$4,name=$5,enabled=$6,trigger=$7,action_type=$8,config=$9,position=$10,updated_at=clock_timestamp() WHERE id=$2 AND form_id=$3 AND EXISTS(SELECT 1 FROM forms.forms WHERE id=$3 AND site_id=$1) RETURNING `+actionColumns+`;`, siteID, item.ID, item.FormID, item.Code, item.Name, item.Enabled, trigger, item.ActionType, item.Config, item.Position))
	return updated, mapWriteError(err)
}
func (r *Repository) DeleteAction(ctx context.Context, siteID site.ID, formID forms.FormID, id forms.ActionID) error {
	command, err := r.connector.Pool().Exec(ctx, `DELETE FROM forms.actions a USING forms.forms f WHERE a.form_id=$2 AND a.id=$3 AND f.id=a.form_id AND f.site_id=$1;`, siteID, formID, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return forms.ErrNotFound
	}
	return nil
}

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
	for index, item := range record.Values {
		item.ResultID = created.ID
		values[index], err = insertResultValue(ctx, tx, item)
		if err != nil {
			return forms.ResultDetail{}, err
		}
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
	var created forms.ResultValue
	var raw []byte
	var stringOut *string
	var integerOut, referenceOut *int64
	var floatOut *float64
	var boolOut *bool
	var timeOut *time.Time
	err = tx.QueryRow(ctx, `INSERT INTO forms.result_values(result_id,field_id,field_code,field_label,result_label,field_type,storage_kind,position,is_multi,string_value,integer_value,float_value,boolean_value,timestamp_value,reference_value,json_value) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING id,result_id,field_id,field_code,field_label,result_label,field_type,storage_kind,position,is_multi,string_value,integer_value,float_value,boolean_value,timestamp_value,reference_value,json_value;`, item.ResultID, item.FieldID, item.FieldCode, item.FieldLabel, item.ResultLabel, item.FieldType, item.StorageKind, item.Position, item.Multiple, stringValue, integerValue, floatValue, booleanValue, timestampValue, referenceValue, jsonValue).Scan(&created.ID, &created.ResultID, &created.FieldID, &created.FieldCode, &created.FieldLabel, &created.ResultLabel, &created.FieldType, &created.StorageKind, &created.Position, &created.Multiple, &stringOut, &integerOut, &floatOut, &boolOut, &timeOut, &referenceOut, &raw)
	if err != nil {
		return forms.ResultValue{}, mapWriteError(err)
	}
	created.Value, err = decodedStoredValue(created.StorageKind, stringOut, integerOut, floatOut, boolOut, timeOut, referenceOut, raw)
	return created, err
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

func (r *Repository) ClaimExecution(ctx context.Context, siteID site.ID, id forms.ActionExecutionID, maxAttempts int) (_ forms.ExecutionWork, claimed bool, resultErr error) {
	if maxAttempts < 1 {
		return forms.ExecutionWork{}, false, forms.ErrInvalid
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return forms.ExecutionWork{}, false, err
	}
	defer rollback(ctx, tx, &resultErr)

	var execution forms.ActionExecution
	var trigger []byte
	err = tx.QueryRow(ctx, `UPDATE forms.action_executions
SET status='running',attempt_count=attempt_count+1,safe_error='',started_at=clock_timestamp(),finished_at=NULL,updated_at=clock_timestamp()
WHERE site_id=$1 AND id=$2
  AND (status IN ('pending','retryable') OR (status='running' AND updated_at <= clock_timestamp()-interval '10 minutes'))
  AND attempt_count < $3
RETURNING `+executionColumns+`;`, siteID, id, maxAttempts).Scan(
		&execution.ID, &execution.SiteID, &execution.ResultID, &execution.ActionID,
		&execution.ActionCode, &execution.ActionName, &execution.ActionType, &trigger,
		&execution.Config, &execution.Status, &execution.AttemptCount, &execution.SafeError,
		&execution.ExternalReference, &execution.StartedAt, &execution.FinishedAt,
		&execution.CreatedAt, &execution.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		command, terminalErr := tx.Exec(ctx, `UPDATE forms.action_executions
SET status='failed',safe_error='execution lease expired',finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE site_id=$1 AND id=$2
  AND (status IN ('pending','retryable') OR (status='running' AND updated_at <= clock_timestamp()-interval '10 minutes'))
  AND attempt_count >= $3;`, siteID, id, maxAttempts)
		if terminalErr != nil {
			return forms.ExecutionWork{}, false, terminalErr
		}
		var status forms.ExecutionStatus
		queryErr := tx.QueryRow(ctx, `SELECT status FROM forms.action_executions WHERE site_id=$1 AND id=$2;`, siteID, id).Scan(&status)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return forms.ExecutionWork{}, false, forms.ErrNotFound
		}
		if queryErr != nil {
			return forms.ExecutionWork{}, false, queryErr
		}
		if err := tx.Commit(ctx); err != nil {
			return forms.ExecutionWork{}, false, err
		}
		if command.RowsAffected() == 0 && status == forms.ExecutionRunning {
			return forms.ExecutionWork{}, false, forms.ErrExecutionBusy
		}
		return forms.ExecutionWork{}, false, nil
	}
	if err != nil {
		return forms.ExecutionWork{}, false, err
	}
	if err := json.Unmarshal(trigger, &execution.Trigger); err != nil {
		return forms.ExecutionWork{}, false, err
	}
	result, err := scanResult(tx.QueryRow(ctx, `SELECT `+resultColumns+` FROM forms.results r JOIN forms.statuses s ON s.id=r.status_id WHERE r.site_id=$1 AND r.id=$2;`, siteID, execution.ResultID))
	if err != nil {
		return forms.ExecutionWork{}, false, err
	}
	values, err := listResultValues(ctx, tx, execution.ResultID)
	if err != nil {
		return forms.ExecutionWork{}, false, err
	}
	uploads, err := listResultUploads(ctx, tx, execution.ResultID)
	if err != nil {
		return forms.ExecutionWork{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return forms.ExecutionWork{}, false, err
	}
	return forms.ExecutionWork{Execution: execution, Result: result, Values: values, Uploads: uploads}, true, nil
}

func (r *Repository) FinishExecution(ctx context.Context, id forms.ActionExecutionID, status forms.ExecutionStatus, safeError, externalReference string) error {
	if status != forms.ExecutionSucceeded && status != forms.ExecutionRetryable && status != forms.ExecutionFailed {
		return forms.ErrInvalid
	}
	command, err := r.connector.Pool().Exec(ctx, `UPDATE forms.action_executions
SET status=$2,safe_error=$3,external_reference=$4,
    finished_at=CASE WHEN $2='retryable' THEN NULL ELSE clock_timestamp() END,
    updated_at=clock_timestamp()
WHERE id=$1 AND status='running';`, id, status, safeError, externalReference)
	if err != nil {
		return mapWriteError(err)
	}
	if command.RowsAffected() != 1 {
		return forms.ErrConflict
	}
	return nil
}

func (r *Repository) HasActiveExecutions(ctx context.Context, siteID site.ID) (bool, error) {
	var result bool
	err := r.connector.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM forms.action_executions WHERE site_id=$1 AND status IN ('pending','running','retryable'));`, siteID).Scan(&result)
	return result, err
}

func (r *Repository) ResultHasActiveSubmittedExecutions(ctx context.Context, siteID site.ID, resultID forms.ResultID) (bool, error) {
	var result bool
	err := r.connector.Pool().QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM forms.action_executions
WHERE site_id=$1 AND result_id=$2 AND trigger->>'type'=$3 AND status IN ('pending','running','retryable')
);`, siteID, resultID, forms.TriggerSubmitted).Scan(&result)
	return result, err
}

func (r *Repository) MarkUploadSpoolDeleted(ctx context.Context, siteID site.ID, resultID forms.ResultID, references []string) error {
	if len(references) == 0 {
		return nil
	}
	_, err := r.connector.Pool().Exec(ctx, `UPDATE forms.result_uploads u
SET spool_deleted_at=coalesce(spool_deleted_at,clock_timestamp())
FROM forms.results r
WHERE u.result_id=r.id AND r.site_id=$1 AND r.id=$2 AND u.spool_reference=ANY($3);`, siteID, resultID, references)
	return err
}

func (r *Repository) MarkUploadSpoolReferencesDeleted(ctx context.Context, siteID site.ID, references []string) error {
	if len(references) == 0 {
		return nil
	}
	_, err := r.connector.Pool().Exec(ctx, `UPDATE forms.result_uploads u
SET spool_deleted_at=coalesce(spool_deleted_at,clock_timestamp())
FROM forms.results r
WHERE u.result_id=r.id AND r.site_id=$1 AND u.spool_reference=ANY($2);`, siteID, references)
	return err
}

func (r *Repository) MarkAllUploadSpoolDeleted(ctx context.Context, siteID site.ID) error {
	_, err := r.connector.Pool().Exec(ctx, `UPDATE forms.result_uploads u
SET spool_deleted_at=coalesce(spool_deleted_at,clock_timestamp())
FROM forms.results r
WHERE u.result_id=r.id AND r.site_id=$1 AND u.spool_reference IS NOT NULL AND u.spool_deleted_at IS NULL;`, siteID)
	return err
}

func (r *Repository) ActiveSpoolReferences(ctx context.Context, siteID site.ID, references []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if len(references) == 0 {
		return result, nil
	}
	rows, err := r.connector.Pool().Query(ctx, `SELECT u.spool_reference
FROM forms.result_uploads u
JOIN forms.results r ON r.id=u.result_id
WHERE r.site_id=$1 AND u.spool_reference=ANY($2) AND u.spool_deleted_at IS NULL
  AND EXISTS(SELECT 1 FROM forms.action_executions e WHERE e.result_id=r.id AND e.trigger->>'type'=$3 AND e.status IN ('pending','running','retryable'));`, siteID, references, forms.TriggerSubmitted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var reference string
		if err := rows.Scan(&reference); err != nil {
			return nil, err
		}
		result[reference] = struct{}{}
	}
	return result, rows.Err()
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return forms.ErrNotFound
	}
	return err
}

func mapWriteError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return forms.ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case pgerrcode.UniqueViolation:
			return forms.ErrConflict
		case pgerrcode.ForeignKeyViolation, pgerrcode.CheckViolation, pgerrcode.NotNullViolation:
			return forms.ErrInvalid
		}
	}
	return err
}

var _ forms.Repository = (*Repository)(nil)
