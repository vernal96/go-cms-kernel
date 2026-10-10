package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

type rowScanner interface {
	Scan(...any) error
}

type rowQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (r *Repository) CreateWidget(
	ctx context.Context,
	actorID *security.UserID,
	resourceID resource.ID,
	expectedVersion int64,
	binding widget.Binding,
	recordRevision bool,
) (widget.Binding, error) {
	if ctx == nil || resourceID <= 0 {
		return widget.Binding{}, errors.New("resource widget create input is invalid")
	}
	rawParams, err := encodeWidgetParams(binding)
	if err != nil {
		return widget.Binding{}, err
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return widget.Binding{}, translateError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	hookBefore, hookErr := r.eventState(ctx, tx, resourceID)
	if hookErr != nil {
		return widget.Binding{}, hookErr
	}

	version, err := lockWidgetResource(ctx, tx, resourceID, expectedVersion)
	if err != nil {
		return widget.Binding{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.resource_widgets WHERE resource_id = $1 AND area = $2;`, resourceID, binding.Area).Scan(&count); err != nil {
		return widget.Binding{}, translateError(err)
	}
	if binding.Position != count {
		return widget.Binding{}, fmt.Errorf("resource %d widget position %d does not append to %q at %d", resourceID, binding.Position, binding.Area, count)
	}
	created, err := scanWidget(tx.QueryRow(ctx, `
INSERT INTO core.resource_widgets
    (resource_id, widget_code, area, position, view, columns, margin_top, margin_bottom, enabled, params, param_bindings)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11::jsonb)
RETURNING id, widget_code, area, position, view, columns, margin_top, margin_bottom, enabled, params, param_bindings;
`, resourceID, binding.Code, binding.Area, binding.Position, binding.Presentation.View,
		binding.Presentation.Columns, binding.Presentation.MarginTop, binding.Presentation.MarginBottom,
		binding.Presentation.Enabled, string(rawParams), nonNilParamBindings(binding.ParamBindings)))
	if err != nil {
		return widget.Binding{}, translateError(err)
	}
	if err := touchWidgetResource(ctx, tx, resourceID); err != nil {
		return widget.Binding{}, err
	}
	created.References = binding.References
	if err := r.replaceWidgetOccurrence(ctx, tx, resourceID, created); err != nil {
		return widget.Binding{}, err
	}
	if err := r.prepareWidgetDraft(ctx, tx, hookBefore); err != nil {
		return widget.Binding{}, err
	}
	if recordRevision {
		if err := r.appendCurrentRevision(ctx, tx, resourceID, version, actorID); err != nil {
			return widget.Binding{}, err
		}
	}
	if err := r.appendWidgetResourceEvent(ctx, tx, resourceID, version, actorID); err != nil {
		return widget.Binding{}, err
	}
	hookBindings := []resource.Resource{{ID: resourceID}}
	if err := loadResourceWidgets(ctx, tx, hookBindings); err != nil {
		return widget.Binding{}, err
	}
	for _, binding := range hookBindings[0].Widgets {
		if binding.ID == created.ID {
			created = binding
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return widget.Binding{}, translateError(err)
	}
	return created, nil
}

func (r *Repository) UpdateWidget(
	ctx context.Context,
	actorID *security.UserID,
	resourceID resource.ID,
	expectedVersion int64,
	binding widget.Binding,
	recordRevision bool,
) (widget.Binding, error) {
	if ctx == nil || resourceID <= 0 || binding.ID <= 0 {
		return widget.Binding{}, errors.New("resource widget update input is invalid")
	}
	rawParams, err := encodeWidgetParams(binding)
	if err != nil {
		return widget.Binding{}, err
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return widget.Binding{}, translateError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	hookBefore, hookErr := r.eventState(ctx, tx, resourceID)
	if hookErr != nil {
		return widget.Binding{}, hookErr
	}

	version, err := lockWidgetResource(ctx, tx, resourceID, expectedVersion)
	if err != nil {
		return widget.Binding{}, err
	}
	updated, err := scanWidget(tx.QueryRow(ctx, `
UPDATE core.resource_widgets
SET widget_code = $3, area = $4, position = $5, view = $6, columns = $7,
    margin_top = $8, margin_bottom = $9, enabled = $10, params = $11::jsonb, param_bindings = $12::jsonb
WHERE resource_id = $1 AND id = $2
RETURNING id, widget_code, area, position, view, columns, margin_top, margin_bottom, enabled, params, param_bindings;
`, resourceID, binding.ID, binding.Code, binding.Area, binding.Position,
		binding.Presentation.View, binding.Presentation.Columns, binding.Presentation.MarginTop,
		binding.Presentation.MarginBottom, binding.Presentation.Enabled, string(rawParams), nonNilParamBindings(binding.ParamBindings)))
	if err != nil {
		return widget.Binding{}, translateError(err)
	}
	if err := touchWidgetResource(ctx, tx, resourceID); err != nil {
		return widget.Binding{}, err
	}
	updated.References = binding.References
	if err := r.replaceWidgetOccurrence(ctx, tx, resourceID, updated); err != nil {
		return widget.Binding{}, err
	}
	if err := r.prepareWidgetDraft(ctx, tx, hookBefore); err != nil {
		return widget.Binding{}, err
	}
	if recordRevision {
		if err := r.appendCurrentRevision(ctx, tx, resourceID, version, actorID); err != nil {
			return widget.Binding{}, err
		}
	}
	if err := r.appendWidgetResourceEvent(ctx, tx, resourceID, version, actorID); err != nil {
		return widget.Binding{}, err
	}
	hookBindings := []resource.Resource{{ID: resourceID}}
	if err := loadResourceWidgets(ctx, tx, hookBindings); err != nil {
		return widget.Binding{}, err
	}
	for _, binding := range hookBindings[0].Widgets {
		if binding.ID == updated.ID {
			updated = binding
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return widget.Binding{}, translateError(err)
	}
	return updated, nil
}

func (r *Repository) DeleteWidget(
	ctx context.Context,
	actorID *security.UserID,
	resourceID resource.ID,
	expectedVersion int64,
	bindingID widget.BindingID,
	recordRevision bool,
) error {
	if ctx == nil || resourceID <= 0 || bindingID <= 0 {
		return errors.New("resource widget delete input is invalid")
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return translateError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	hookBefore, hookErr := r.eventState(ctx, tx, resourceID)
	if hookErr != nil {
		return hookErr
	}

	version, err := lockWidgetResource(ctx, tx, resourceID, expectedVersion)
	if err != nil {
		return err
	}
	var area widget.AreaCode
	var position int
	if err := tx.QueryRow(ctx, `SELECT area, position FROM core.resource_widgets WHERE resource_id = $1 AND id = $2 FOR UPDATE;`, resourceID, bindingID).Scan(&area, &position); err != nil {
		return translateError(err)
	}
	if err := deleteWidgetOccurrence(ctx, tx, resourceID, bindingID); err != nil {
		return translateError(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.resource_widgets WHERE resource_id = $1 AND id = $2;`, resourceID, bindingID); err != nil {
		return translateError(err)
	}
	var offset int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(position), -1) + 1 FROM core.resource_widgets WHERE resource_id = $1 AND area = $2;`, resourceID, area).Scan(&offset); err != nil {
		return translateError(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.resource_widgets SET position = position + $4 WHERE resource_id = $1 AND area = $2 AND position > $3;`, resourceID, area, position, offset); err != nil {
		return translateError(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.resource_widgets SET position = position - $4::integer - 1 WHERE resource_id = $1 AND area = $2 AND position > $3::integer + $4::integer;`, resourceID, area, position, offset); err != nil {
		return translateError(err)
	}
	if err := touchWidgetResource(ctx, tx, resourceID); err != nil {
		return err
	}
	if err := r.prepareWidgetDraft(ctx, tx, hookBefore); err != nil {
		return err
	}
	if recordRevision {
		if err := r.appendCurrentRevision(ctx, tx, resourceID, version, actorID); err != nil {
			return err
		}
	}
	if err := r.appendWidgetResourceEvent(ctx, tx, resourceID, version, actorID); err != nil {
		return err
	}
	return translateError(tx.Commit(ctx))
}

func (r *Repository) ReorderWidgets(
	ctx context.Context,
	actorID *security.UserID,
	resourceID resource.ID,
	expectedVersion int64,
	order []widget.Order,
	recordRevision bool,
) ([]widget.Binding, error) {
	if ctx == nil || resourceID <= 0 {
		return nil, errors.New("resource widget reorder input is invalid")
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	hookBefore, hookErr := r.eventState(ctx, tx, resourceID)
	if hookErr != nil {
		return nil, hookErr
	}

	version, err := lockWidgetResource(ctx, tx, resourceID, expectedVersion)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM core.resource_widgets WHERE resource_id = $1 FOR UPDATE;`, resourceID)
	if err != nil {
		return nil, translateError(err)
	}
	known := make(map[widget.BindingID]struct{})
	for rows.Next() {
		var id widget.BindingID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, translateError(err)
		}
		known[id] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, translateError(err)
	}
	if len(known) != len(order) {
		return nil, errors.New("resource widget order is incomplete")
	}
	positions := make(map[widget.AreaCode]int)
	seen := make(map[widget.BindingID]struct{}, len(order))
	for _, item := range order {
		if _, exists := known[item.ID]; !exists {
			return nil, fmt.Errorf("resource widget %d is unavailable", item.ID)
		}
		if _, exists := seen[item.ID]; exists {
			return nil, fmt.Errorf("resource widget %d is duplicated in order", item.ID)
		}
		seen[item.ID] = struct{}{}
		if !widget.ValidArea(item.Area) {
			return nil, fmt.Errorf("resource widget %d has invalid area %q", item.ID, item.Area)
		}
		if item.Position != positions[item.Area] {
			return nil, fmt.Errorf(
				"resource widget %d in %q has position %d instead of %d",
				item.ID,
				item.Area,
				item.Position,
				positions[item.Area],
			)
		}
		positions[item.Area]++
	}
	var offset int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(position), -1) + 1 FROM core.resource_widgets WHERE resource_id = $1;`, resourceID).Scan(&offset); err != nil {
		return nil, translateError(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.resource_widgets SET position = position + $2 WHERE resource_id = $1;`, resourceID, offset); err != nil {
		return nil, translateError(err)
	}
	for _, item := range order {
		command, err := tx.Exec(ctx, `UPDATE core.resource_widgets SET area = $3, position = $4 WHERE resource_id = $1 AND id = $2;`, resourceID, item.ID, item.Area, item.Position)
		if err != nil {
			return nil, translateError(err)
		}
		if command.RowsAffected() != 1 {
			return nil, resource.ErrNotFound
		}
	}
	loaded := []resource.Resource{{ID: resourceID}}
	if err := loadResourceWidgets(ctx, tx, loaded); err != nil {
		return nil, err
	}
	if err := touchWidgetResource(ctx, tx, resourceID); err != nil {
		return nil, err
	}
	if err := r.prepareWidgetDraft(ctx, tx, hookBefore); err != nil {
		return nil, err
	}
	if recordRevision {
		if err := r.appendCurrentRevision(ctx, tx, resourceID, version, actorID); err != nil {
			return nil, err
		}
	}
	if err := loadResourceWidgets(ctx, tx, loaded); err != nil {
		return nil, err
	}
	if err := r.appendWidgetResourceEvent(ctx, tx, resourceID, version, actorID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, translateError(err)
	}
	return loaded[0].Widgets, nil
}

func lockWidgetResource(ctx context.Context, tx pgx.Tx, resourceID resource.ID, expectedVersion int64) (int64, error) {
	var version int64
	if err := tx.QueryRow(ctx, `UPDATE core.resource_entities SET version=version+1 WHERE id=$1 AND version=$2 RETURNING version;`, resourceID, expectedVersion).Scan(&version); errors.Is(err, pgx.ErrNoRows) {
		return 0, resource.ErrConflict
	} else if err != nil {
		return 0, translateError(err)
	}
	return version, nil
}

func touchWidgetResource(ctx context.Context, tx pgx.Tx, resourceID resource.ID) error {
	command, err := tx.Exec(ctx, `UPDATE core.resources SET updated_at = now() WHERE id = $1;`, resourceID)
	if err != nil {
		return translateError(err)
	}
	if command.RowsAffected() == 0 {
		command, err = tx.Exec(ctx, `UPDATE core.library_items SET updated_at = now() WHERE id = $1;`, resourceID)
		if err != nil {
			return translateError(err)
		}
	}
	if command.RowsAffected() == 0 {
		return resource.ErrNotFound
	}
	return nil
}

func encodeWidgetParams(binding widget.Binding) ([]byte, error) {
	params := binding.Params
	if params == nil {
		params = map[string]any{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode params for widget %q: %w", binding.Code, err)
	}
	return raw, nil
}

func scanWidget(scanner rowScanner) (widget.Binding, error) {
	var binding widget.Binding
	var rawParams []byte
	if err := scanner.Scan(
		&binding.ID, &binding.Code, &binding.Area, &binding.Position,
		&binding.Presentation.View, &binding.Presentation.Columns,
		&binding.Presentation.MarginTop, &binding.Presentation.MarginBottom,
		&binding.Presentation.Enabled, &rawParams, &binding.ParamBindings,
	); err != nil {
		return widget.Binding{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(rawParams))
	decoder.UseNumber()
	if err := decoder.Decode(&binding.Params); err != nil {
		return widget.Binding{}, fmt.Errorf("decode params for widget %d: %w", binding.ID, err)
	}
	if binding.Params == nil {
		binding.Params = map[string]any{}
	}
	return binding, nil
}

func loadResourceWidgets(
	ctx context.Context,
	queryer rowQueryer,
	items []resource.Resource,
) error {
	return loadSelectedResourceWidgets(ctx, queryer, items, nil)
}

func loadSelectedResourceWidgets(ctx context.Context, queryer rowQueryer, items []resource.Resource, selected []widget.BindingID) error {
	if len(items) == 0 {
		return nil
	}

	indexes := make(map[resource.ID]int, len(items))
	ids := make([]int64, len(items))
	for index := range items {
		indexes[items[index].ID] = index
		ids[index] = int64(items[index].ID)
		items[index].Widgets = nil
	}

	rows, err := queryer.Query(ctx, `
SELECT resource_id, id, widget_code, area, position, view, columns,
       margin_top, margin_bottom, enabled, params, param_bindings
FROM core.resource_widgets
WHERE resource_id = ANY($1::bigint[]) AND ($2::bigint[] IS NULL OR id = ANY($2::bigint[]))
ORDER BY resource_id, area, position, id;
`, ids, selected)
	if err != nil {
		return fmt.Errorf("query resource widgets: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			resourceID resource.ID
			binding    widget.Binding
			rawParams  []byte
		)
		if err := rows.Scan(
			&resourceID,
			&binding.ID,
			&binding.Code,
			&binding.Area,
			&binding.Position,
			&binding.Presentation.View,
			&binding.Presentation.Columns,
			&binding.Presentation.MarginTop,
			&binding.Presentation.MarginBottom,
			&binding.Presentation.Enabled,
			&rawParams,
			&binding.ParamBindings,
		); err != nil {
			return fmt.Errorf("scan resource widget: %w", err)
		}

		index, exists := indexes[resourceID]
		if !exists {
			return fmt.Errorf(
				"resource widget references unexpected resource %d",
				resourceID,
			)
		}
		params := make(map[string]any)
		decoder := json.NewDecoder(bytes.NewReader(rawParams))
		decoder.UseNumber()
		if err := decoder.Decode(&params); err != nil {
			return fmt.Errorf(
				"decode params for resource %d widget %q: %w",
				resourceID,
				binding.Code,
				err,
			)
		}

		binding.Params = params
		items[index].Widgets = append(items[index].Widgets, binding)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate resource widgets: %w", err)
	}
	rows.Close()
	return loadWidgetOccurrences(ctx, queryer, items)
}

func nonNilParamBindings(bindings widget.ParamBindings) widget.ParamBindings {
	if bindings == nil {
		return widget.ParamBindings{}
	}
	return bindings
}
