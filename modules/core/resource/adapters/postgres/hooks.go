package postgres

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

// relatedStates captures only the subtree and sibling lists whose derived path
// or ordering can be changed by this operation. It does not scan other sites.
func (r *Repository) relatedChangeStates(ctx context.Context, tx pgx.Tx, before, after resource.Resource) ([]resource.EventState, error) {
	pathsChanged := !sameOptionalText(before.Path, after.Path) || !reflect.DeepEqual(before.TypeSettings["item_url_pattern"], after.TypeSettings["item_url_pattern"])
	orderChanged := !sameResourceID(before.ParentID, after.ParentID) || before.Sort != after.Sort
	if !pathsChanged && !orderChanged {
		return nil, nil
	}
	return r.relatedStates(ctx, tx, before.ID, before.SiteID, before.ParentID, after.ParentID, pathsChanged, orderChanged)
}

func (r *Repository) relatedStates(ctx context.Context, tx pgx.Tx, id resource.ID, siteID site.ID, oldParent, newParent *resource.ID, pathsChanged, orderChanged bool) ([]resource.EventState, error) {
	rows, err := tx.Query(ctx, `WITH RECURSIVE tree AS (SELECT id FROM core.resources WHERE id=$1 UNION ALL SELECT child.id FROM core.resources child JOIN tree parent ON child.parent_id=parent.id WHERE $5), related AS (SELECT id FROM tree UNION SELECT id FROM core.resources WHERE site_id=$2 AND $6 AND (parent_id IS NOT DISTINCT FROM $3::bigint OR parent_id IS NOT DISTINCT FROM $4::bigint)) SELECT id FROM related WHERE id<>$1 UNION SELECT resource_id FROM core.library_item_routes WHERE $5 AND library_id IN (SELECT id FROM tree) ORDER BY id`, id, siteID, oldParent, newParent, pathsChanged, orderChanged)
	if err != nil {
		return nil, err
	}
	var ids []resource.ID
	for rows.Next() {
		var current resource.ID
		if err := rows.Scan(&current); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, current)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var states []resource.EventState
	for _, current := range ids {
		state, err := r.eventState(ctx, tx, current)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

func (r *Repository) finishRelated(ctx context.Context, tx pgx.Tx, states []resource.EventState, actorID *security.UserID) error {
	for _, before := range states {
		after, err := r.eventState(ctx, tx, before.ID)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(before.Data, after.Data) && reflect.DeepEqual(before.Path, after.Path) && reflect.DeepEqual(before.DeletedAt, after.DeletedAt) {
			continue
		}
		data, err := resource.PrepareMutation(ctx, &before, after)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(data, after.Data) {
			return fmt.Errorf("%w: derived subtree/sibling changes cannot modify unrelated fields", resource.ErrInvalid)
		}
		if err := tx.QueryRow(ctx, `UPDATE core.resource_entities SET version=version+1 WHERE id=$1 RETURNING version`, after.ID).Scan(&after.Version); err != nil {
			return err
		}
		if after.Data.StorageKind == resource.StorageTree {
			item, err := r.treeInTransaction(ctx, tx, after.ID)
			if err != nil {
				return err
			}
			if err := r.appendRevision(ctx, tx, item, resource.RevisionUpdated, nil, actorID); err != nil {
				return err
			}
		}
		if err := r.appendStateEvent(ctx, tx, resource.EventUpdated, after, actorID); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) subtreeStates(ctx context.Context, tx pgx.Tx, id resource.ID, descendants bool) ([]resource.EventState, error) {
	rows, err := tx.Query(ctx, `WITH RECURSIVE tree AS (SELECT id FROM core.resources WHERE id=$1 UNION ALL SELECT c.id FROM core.resources c JOIN tree p ON c.parent_id=p.id WHERE $2) SELECT id FROM tree UNION SELECT route.resource_id FROM core.library_item_routes route WHERE route.library_id IN (SELECT id FROM tree) ORDER BY id`, id, descendants)
	if err != nil {
		return nil, err
	}
	var ids []resource.ID
	for rows.Next() {
		var current resource.ID
		if err := rows.Scan(&current); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, current)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, resource.ErrNotFound
	}
	states := make([]resource.EventState, 0, len(ids))
	for _, current := range ids {
		state, err := r.eventState(ctx, tx, current)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

func prepareLifecycle(ctx context.Context, states []resource.EventState, deleted bool, individual bool) error {
	for _, before := range states {
		next := before

		next.InTrash = deleted
		if before.Data.StorageKind == resource.StorageLibraryItem && !individual {
			next.InTrash = deleted || before.DeletedAt != nil
		} else {
			next.DeletedAt = nil
			if deleted {
				now := time.Now().UTC()
				next.DeletedAt = &now
			}
		}
		if next.InTrash == before.InTrash && reflect.DeepEqual(next.DeletedAt, before.DeletedAt) {
			continue
		}

		if _, err := resource.PrepareMutation(ctx, &before, next); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) finishLifecycle(ctx context.Context, tx pgx.Tx, states []resource.EventState, deleted bool, actorID *security.UserID, individual bool) error {
	for _, before := range states {
		if before.Data.StorageKind == resource.StorageLibraryItem && individual {
			query := `UPDATE core.library_items SET deleted_at=NULL,deleted_by=NULL,updated_at=now(),updated_by=$2 WHERE id=$1`
			if deleted {
				query = `UPDATE core.library_items SET deleted_at=coalesce(deleted_at,now()),deleted_by=coalesce(deleted_by,$2),updated_at=now(),updated_by=$2 WHERE id=$1`
			}
			if _, err := tx.Exec(ctx, query, before.ID, actorID); err != nil {
				return translateError(err)
			}
		}
		state, err := r.eventState(ctx, tx, before.ID)
		if err != nil {
			return err
		}
		if state.InTrash == before.InTrash && reflect.DeepEqual(state.DeletedAt, before.DeletedAt) {
			continue
		}
		if err := tx.QueryRow(ctx, `UPDATE core.resource_entities SET version=version+1 WHERE id=$1 RETURNING version`, before.ID).Scan(&state.Version); err != nil {
			return err
		}
		if err := r.appendStateEvent(ctx, tx, resource.EventUpdated, state, actorID); err != nil {
			return err
		}
	}
	return nil
}

// Finalize a widget draft before history/outbox are appended. Every SQL write
// is still private to the transaction, and a veto rolls back the entire draft.
func (r *Repository) prepareWidgetDraft(ctx context.Context, tx pgx.Tx, before resource.EventState) error {
	state, err := r.eventState(ctx, tx, before.ID)
	if err != nil {
		return err
	}
	next, err := resource.PrepareMutation(ctx, &before, state)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(next, state.Data) {
		return nil
	}
	if len(next.Widgets) != len(state.Data.Widgets) {
		return fmt.Errorf("%w: a widget hook cannot change the operation's widget count", resource.ErrInvalid)
	}
	loaded := []resource.Resource{{ID: before.ID}}
	if err := loadResourceWidgets(ctx, tx, loaded); err != nil {
		return err
	}
	// Keep the identity and placement chosen by the operation. A create/update
	// hook may customize widget parameters/presentation within this resource.
	for i, item := range next.Widgets {
		old := state.Data.Widgets[i]
		if item.Code != old.Code || item.Area != old.Area || item.Position != old.Position {
			return fmt.Errorf("%w: hook changed widget identity or placement", resource.ErrInvalid)
		}
		binding := loaded[0].Widgets[i]
		binding.Params = item.Params
		binding.ParamBindings = widget.CloneParamBindings(item.ParamBindings)
		binding.Presentation = widget.Presentation{View: item.View, Columns: item.Columns, MarginTop: item.MarginTop, MarginBottom: item.MarginBottom, Enabled: item.Enabled}
		if err := resource.ValidateMutationWidget(ctx, state.SiteID, state.Data.Template, &binding); err != nil {
			return err
		}
		raw, err := encodeWidgetParams(binding)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE core.resource_widgets SET view=$3,columns=$4,margin_top=$5,margin_bottom=$6,enabled=$7,params=$8::jsonb,param_bindings=$9::jsonb WHERE resource_id=$1 AND id=$2`, before.ID, binding.ID, binding.Presentation.View, binding.Presentation.Columns, binding.Presentation.MarginTop, binding.Presentation.MarginBottom, binding.Presentation.Enabled, string(raw), nonNilParamBindings(binding.ParamBindings)); err != nil {
			return translateError(err)
		}
	}
	return nil
}

// Library snapshots include their effective route and inherited trash state.
// Read the owning library through the same transaction as the locked item.
func (r *Repository) libraryHookState(ctx context.Context, tx pgx.Tx, item resource.LibraryItem) (resource.EventState, error) {
	state := resource.StateFromLibraryItem(item)
	library, err := routeResourceByID(ctx, tx, item.LibraryID)
	if err != nil {
		return resource.EventState{}, err
	}
	state.InTrash = state.InTrash || library.DeletedAt != nil
	if library.Path != nil {
		path, err := resource.EffectiveLibraryItemURL(library, item)
		if err != nil {
			return resource.EventState{}, err
		}
		state.Path = &path
	}
	return state, nil
}

func (r *Repository) prepareLibraryMutation(ctx context.Context, tx pgx.Tx, before *resource.LibraryItem, candidate resource.LibraryItem) (resource.LibraryItem, error) {
	var previous *resource.EventState
	if before != nil {
		state, err := r.libraryHookState(ctx, tx, *before)
		if err != nil {
			return resource.LibraryItem{}, err
		}
		previous = &state
	}
	state, err := r.libraryHookState(ctx, tx, candidate)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	return resource.PrepareLibraryMutation(ctx, previous, candidate, state)
}
