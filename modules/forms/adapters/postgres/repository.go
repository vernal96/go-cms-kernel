package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/forms"
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

const fieldColumns = `id,form_id,code,type,label,required,validators,options,editor,visible_when,result_label,show_in_results,show_on_site,result_position,created_at,updated_at`

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
	var validators, options, visible []byte
	err := row.Scan(&item.ID, &item.FormID, &item.Code, &item.Type, &item.Label, &item.Required, &validators, &options, &item.Editor, &visible, &item.ResultLabel, &item.ShowInResults, &item.ShowOnSite, &item.ResultPosition, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return forms.FormField{}, err
	}
	if err := json.Unmarshal(validators, &item.Validators); err != nil {
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
