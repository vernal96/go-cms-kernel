package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/forms"
	"github.com/vernal96/go-cms-kernel/security"
)

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
	validators, err := json.Marshal(item.Validators)
	if err != nil {
		return forms.FormField{}, err
	}
	options, err := encodeFieldOptions(item)
	if err != nil {
		return forms.FormField{}, err
	}
	visible, err := nullableJSON(item.VisibleWhen)
	if err != nil {
		return forms.FormField{}, err
	}
	created, err := scanField(tx.QueryRow(ctx, `INSERT INTO forms.fields(form_id,code,type,label,required,validators,options,editor,visible_when,result_label,show_in_results,show_on_site,result_position) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING `+fieldColumns+`;`, item.FormID, item.Code, item.Type, item.Label, item.Required, validators, options, item.Editor, visible, item.ResultLabel, item.ShowInResults, item.ShowOnSite, item.ResultPosition))
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
	validators, err := json.Marshal(item.Validators)
	if err != nil {
		return forms.FormField{}, err
	}
	options, err := encodeFieldOptions(item)
	if err != nil {
		return forms.FormField{}, err
	}
	visible, err := nullableJSON(item.VisibleWhen)
	if err != nil {
		return forms.FormField{}, err
	}
	updated, err := scanField(r.connector.Pool().QueryRow(ctx, `UPDATE forms.fields SET code=$4,type=$5,label=$6,required=$7,validators=$8,options=$9,editor=$10,visible_when=$11,result_label=$12,show_in_results=$13,show_on_site=$14,result_position=$15,updated_at=clock_timestamp() WHERE id=$2 AND form_id=$3 AND EXISTS(SELECT 1 FROM forms.forms WHERE id=$3 AND site_id=$1) RETURNING `+fieldColumns+`;`, siteID, item.ID, item.FormID, item.Code, item.Type, item.Label, item.Required, validators, options, item.Editor, visible, item.ResultLabel, item.ShowInResults, item.ShowOnSite, item.ResultPosition))
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
