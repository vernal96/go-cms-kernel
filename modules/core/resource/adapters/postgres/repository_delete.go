package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) SoftDelete(
	ctx context.Context,
	actorID *security.UserID,
	id resource.ID,
) (resultErr error) {
	if ctx == nil {
		return errors.New("soft delete resource context is nil")
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin resource soft delete: %w", err)
	}
	defer func() {
		if resultErr != nil {
			_ = tx.Rollback(context.Background())
		}
	}()
	if _, err := tx.Exec(ctx, `LOCK TABLE core.resources IN SHARE ROW EXCLUSIVE MODE;`); err != nil {
		return fmt.Errorf("lock resources for soft delete: %w", err)
	}
	hookStates, err := r.subtreeStates(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if err := prepareLifecycle(ctx, hookStates, true, false); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
WITH RECURSIVE tree AS (
    SELECT id FROM core.resources WHERE id = $1
    UNION ALL
    SELECT child.id
    FROM core.resources child
    JOIN tree parent ON child.parent_id = parent.id
)
UPDATE core.resources item
SET deleted_at = now(), deleted_by = $2, updated_at = now(), updated_by = $2
WHERE item.id IN (SELECT id FROM tree);`, id, actorID)
	if err != nil {
		return translateError(err)
	}
	if command.RowsAffected() == 0 {
		return resource.ErrNotFound
	}
	if err := r.finishLifecycle(ctx, tx, hookStates, true, actorID, false); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return translateError(err)
	}
	return nil
}

func (r *Repository) Restore(
	ctx context.Context,
	actorID *security.UserID,
	id resource.ID,
	withDescendants bool,
) (resultErr error) {
	if ctx == nil {
		return errors.New("restore resource context is nil")
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin resource restore: %w", err)
	}
	defer func() {
		if resultErr != nil {
			_ = tx.Rollback(context.Background())
		}
	}()
	item, err := routeResourceByID(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := lockRouteNamespace(ctx, tx, item.SiteID); err != nil {
		return err
	}
	paths, err := prospectiveTreePaths(ctx, tx, item.ID, item.Path)
	if err != nil {
		return err
	}
	if !withDescendants && item.Path != nil {
		paths = []string{*item.Path}
	}
	if err := ensureTreePathsAvailable(ctx, tx, item.SiteID, paths, &item); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE core.resources IN SHARE ROW EXCLUSIVE MODE;`); err != nil {
		return fmt.Errorf("lock resources for restore: %w", err)
	}
	var parentDeleted bool
	if err := tx.QueryRow(ctx, `
SELECT COALESCE(parent.deleted_at IS NOT NULL, false)
FROM core.resources item
LEFT JOIN core.resources parent ON parent.id = item.parent_id
WHERE item.id = $1;`, id).Scan(&parentDeleted); errors.Is(err, pgx.ErrNoRows) {
		return resource.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("check resource restore parent: %w", err)
	}
	if parentDeleted {
		return resource.ErrInvalidTree
	}
	hookStates, err := r.subtreeStates(ctx, tx, id, withDescendants)
	if err != nil {
		return err
	}
	if err := prepareLifecycle(ctx, hookStates, false, false); err != nil {
		return err
	}
	if withDescendants {
		if _, err := tx.Exec(ctx, `
WITH RECURSIVE tree AS (
    SELECT id FROM core.resources WHERE id = $1
    UNION ALL
    SELECT child.id FROM core.resources child
    JOIN tree parent ON child.parent_id = parent.id
)
UPDATE core.resources item
SET deleted_at = NULL, deleted_by = NULL, updated_at = now(), updated_by = $2
WHERE item.id IN (SELECT id FROM tree);`, id, actorID); err != nil {
			return translateError(err)
		}
	} else if command, err := tx.Exec(ctx, `
UPDATE core.resources
SET deleted_at = NULL, deleted_by = NULL, updated_at = now(), updated_by = $2
WHERE id = $1;`, id, actorID); err != nil {
		return translateError(err)
	} else if command.RowsAffected() == 0 {
		return resource.ErrNotFound
	}
	if err := r.finishLifecycle(ctx, tx, hookStates, false, actorID, false); err != nil {
		return err
	}
	if err := validateMirrorNamespaces(ctx, tx, false); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return translateError(err)
	}
	return nil
}

func (r *Repository) Delete(
	ctx context.Context,
	id resource.ID,
) (_ error) {
	if ctx == nil {
		return errors.New("delete resource context is nil")
	}

	transaction, err := r.connector.Pool().BeginTx(
		ctx,
		pgx.TxOptions{},
	)
	if err != nil {
		return fmt.Errorf("begin resource delete: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = transaction.Rollback(context.Background())
	}()
	var deletedSiteID site.ID
	var deletedParentID *int64
	if err := transaction.QueryRow(ctx, `SELECT site_id, parent_id FROM core.resources WHERE id = $1;`, id).Scan(
		&deletedSiteID, &deletedParentID,
	); errors.Is(err, pgx.ErrNoRows) {
		return resource.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("read resource delete position: %w", err)
	}
	if err := lockRouteNamespace(ctx, transaction, deletedSiteID); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `LOCK TABLE core.resources IN SHARE ROW EXCLUSIVE MODE;`); err != nil {
		return fmt.Errorf("lock resources for permanent delete: %w", err)
	}

	var referenced bool
	if err := transaction.QueryRow(ctx, `WITH RECURSIVE tree AS (
 SELECT id FROM core.resources WHERE id=$1 UNION ALL
 SELECT child.id FROM core.resources child JOIN tree ON child.parent_id=tree.id
 ) SELECT EXISTS(SELECT 1 FROM core.resources mirror JOIN tree ON tree.id=mirror.source_library_id)`, id).Scan(&referenced); err != nil {
		return err
	}
	if referenced {
		return resource.ErrReferenced
	}

	hookSiblings, err := r.relatedStates(ctx, transaction, id, deletedSiteID, resourceIDFromInt64(deletedParentID), resourceIDFromInt64(deletedParentID), false, true)
	if err != nil {
		return err
	}
	hookStates, err := r.subtreeStates(ctx, transaction, id, true)
	if err != nil {
		return err
	}
	for _, state := range hookStates {
		if err := r.appendStateEvent(ctx, transaction, resource.EventDeleted, state, nil); err != nil {
			return err
		}
	}
	owners := make([]resource.ID, 0, len(hookStates))
	for _, state := range hookStates {
		owners = append(owners, state.ID)
	}
	fieldMedia, err := fieldMediaIDs(ctx, transaction, owners)
	if err != nil {
		return err
	}
	observedMediaIDs, exists, err := treeMediaIDs(
		ctx,
		transaction,
		id,
		false,
	)
	if err != nil {
		return err
	}
	if !exists {
		return resource.ErrNotFound
	}
	libraryMediaIDs, err := treeLibraryItemMediaIDs(ctx, transaction, id, false)
	if err != nil {
		return err
	}
	observedMediaIDs = append(observedMediaIDs, libraryMediaIDs...)
	observedMediaIDs = append(observedMediaIDs, fieldMedia...)
	if err := medialock.Lock(
		ctx,
		transaction,
		observedMediaIDs...,
	); err != nil {
		return err
	}

	actualMediaIDs, exists, err := treeMediaIDs(
		ctx,
		transaction,
		id,
		true,
	)
	if err != nil {
		return err
	}
	if !exists {
		return resource.ErrNotFound
	}
	libraryMediaIDs, err = treeLibraryItemMediaIDs(ctx, transaction, id, true)
	if err != nil {
		return err
	}
	actualMediaIDs = append(actualMediaIDs, libraryMediaIDs...)
	if !mediaIDsContained(actualMediaIDs, observedMediaIDs) {
		return resource.ErrConflict
	}
	if _, err := transaction.Exec(ctx, `
WITH RECURSIVE tree AS
(
    SELECT id FROM core.resources WHERE id = $1
    UNION ALL
    SELECT child.id FROM core.resources AS child
    JOIN tree AS parent ON child.parent_id = parent.id
)
DELETE FROM core.file_field_references
WHERE owner_kind = 'resource' AND owner_id IN (
    SELECT id FROM tree
    UNION ALL
    SELECT route.resource_id
    FROM core.library_item_routes route
    JOIN tree library ON library.id = route.library_id
);
`, id); err != nil {
		return translateDeleteError(err)
	}
	if _, err := transaction.Exec(ctx, `
WITH RECURSIVE tree AS (
    SELECT id FROM core.resources WHERE id = $1
    UNION ALL
    SELECT child.id FROM core.resources child JOIN tree parent ON child.parent_id = parent.id
), owners AS (
    SELECT id FROM tree
    UNION
    SELECT route.resource_id
    FROM core.library_item_routes route
    JOIN tree library ON library.id = route.library_id
)
DELETE FROM core.media_field_occurrences
WHERE owner_kind='resource' AND owner_id IN (SELECT id FROM owners);`, id); err != nil {
		return translateDeleteError(err)
	}
	if _, err := transaction.Exec(ctx, `
WITH RECURSIVE tree AS (
    SELECT id FROM core.resources WHERE id = $1
    UNION ALL
    SELECT child.id FROM core.resources child JOIN tree parent ON child.parent_id = parent.id
)
DELETE FROM core.resource_entities entity
WHERE entity.storage_kind = 'library_item'
  AND entity.id IN (
      SELECT route.resource_id
      FROM core.library_item_routes route
      JOIN tree library ON library.id = route.library_id
  );`, id); err != nil {
		return translateDeleteError(err)
	}

	commandTag, err := transaction.Exec(ctx, `
WITH RECURSIVE tree AS (
    SELECT id FROM core.resources WHERE id = $1
    UNION ALL
    SELECT child.id FROM core.resources child JOIN tree parent ON child.parent_id = parent.id
), deleted AS (
    DELETE FROM core.resources item
    WHERE item.id IN (SELECT id FROM tree)
    RETURNING item.id
)
DELETE FROM core.resource_entities entity
WHERE entity.storage_kind = 'tree' AND entity.id IN (SELECT id FROM deleted);
`, id)
	if err != nil {
		return translateDeleteError(err)
	}
	if commandTag.RowsAffected() == 0 {
		return resource.ErrNotFound
	}
	if _, err := transaction.Exec(ctx, `
WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY sort, id) - 1 AS new_sort
    FROM core.resources
    WHERE site_id = $1 AND parent_id IS NOT DISTINCT FROM $2::bigint
)
UPDATE core.resources item
SET sort = ordered.new_sort
FROM ordered
WHERE item.id = ordered.id AND item.sort <> ordered.new_sort;`, deletedSiteID, deletedParentID); err != nil {
		return translateDeleteError(err)
	}

	for _, mediaID := range actualMediaIDs {
		if _, err := transaction.Exec(ctx, `DELETE FROM core.media WHERE id=$1`, mediaID); err != nil {
			return translateDeleteError(err)
		}
	}
	if err := deleteUnusedMedia(ctx, transaction, fieldMedia); err != nil {
		return err
	}

	if err := r.finishRelated(ctx, transaction, hookSiblings, nil); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return translateDeleteError(err)
	}
	committed = true
	return nil
}

func reorderSiblings(
	ctx context.Context,
	tx pgx.Tx,
	siteID site.ID,
	movingID resource.ID,
	sourceParentID *resource.ID,
	targetParentID *resource.ID,
	position int,
) (int, error) {
	if position < 0 {
		return 0, resource.ErrInvalidTree
	}
	target, err := siblingIDs(ctx, tx, siteID, targetParentID, movingID)
	if err != nil {
		return 0, err
	}
	if position > len(target) {
		position = len(target)
	}
	if sameResourceID(sourceParentID, targetParentID) {
		ordered := append(target, 0)
		copy(ordered[position+1:], ordered[position:])
		ordered[position] = movingID
		if err := updateSiblingPositions(ctx, tx, ordered, movingID); err != nil {
			return 0, err
		}
		return position, nil
	}
	source, err := siblingIDs(ctx, tx, siteID, sourceParentID, movingID)
	if err != nil {
		return 0, err
	}
	if err := updateSiblingPositions(ctx, tx, source, 0); err != nil {
		return 0, err
	}
	ordered := append(target, 0)
	copy(ordered[position+1:], ordered[position:])
	ordered[position] = movingID
	if err := updateSiblingPositions(ctx, tx, ordered, movingID); err != nil {
		return 0, err
	}
	return position, nil
}

func siblingIDs(
	ctx context.Context,
	tx pgx.Tx,
	siteID site.ID,
	parentID *resource.ID,
	exclude resource.ID,
) ([]resource.ID, error) {
	rows, err := tx.Query(ctx, `
SELECT id
FROM core.resources
WHERE site_id = $1
  AND parent_id IS NOT DISTINCT FROM $2::bigint
  AND id <> $3
ORDER BY sort, id;`, siteID, parentID, exclude)
	if err != nil {
		return nil, fmt.Errorf("list resource siblings: %w", err)
	}
	defer rows.Close()
	result := make([]resource.ID, 0)
	for rows.Next() {
		var id resource.ID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan resource sibling: %w", err)
		}
		result = append(result, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resource siblings: %w", err)
	}
	return result, nil
}

func updateSiblingPositions(ctx context.Context, tx pgx.Tx, ids []resource.ID, skip resource.ID) error {
	for position, id := range ids {
		if id == skip {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE core.resources SET sort = $2 WHERE id = $1;`, id, position); err != nil {
			return fmt.Errorf("update resource sibling position: %w", err)
		}
	}
	return nil
}

func resourceIDFromInt64(value *int64) *resource.ID {
	if value == nil {
		return nil
	}
	result := resource.ID(*value)
	return &result
}

func sameResourceID(left, right *resource.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
