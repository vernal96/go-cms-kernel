package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) TransferToSite(
	ctx context.Context,
	actorID *security.UserID,
	id resource.ID,
	sourceSiteID site.ID,
	targetSiteID site.ID,
	expectedVersion int64,
	expectedSourceProfile string,
	expectedTargetProfile string,
) (result resource.SiteTransferResult, resultErr error) {
	if ctx == nil {
		return resource.SiteTransferResult{}, errors.New("resource site transfer context is nil")
	}
	if id <= 0 || sourceSiteID <= 0 || targetSiteID <= 0 || sourceSiteID == targetSiteID {
		return resource.SiteTransferResult{}, resource.ErrInvalidTree
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return resource.SiteTransferResult{}, fmt.Errorf("begin resource site transfer: %w", err)
	}
	defer func() {
		if resultErr == nil {
			return
		}
		if rollbackErr := tx.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()

	lockSites := []site.ID{sourceSiteID, targetSiteID}
	sort.Slice(lockSites, func(left, right int) bool { return lockSites[left] < lockSites[right] })
	for _, siteID := range lockSites {
		if err := lockRouteNamespace(ctx, tx, siteID); err != nil {
			return resource.SiteTransferResult{}, err
		}
		var profileCode string
		if err := tx.QueryRow(ctx, `SELECT profile_code FROM core.sites WHERE id=$1 FOR SHARE;`, siteID).Scan(&profileCode); errors.Is(err, pgx.ErrNoRows) {
			return resource.SiteTransferResult{}, resource.ErrIncompatibleTargetSite
		} else if err != nil {
			return resource.SiteTransferResult{}, translateError(err)
		}
		expectedProfile := expectedSourceProfile
		if siteID == targetSiteID {
			expectedProfile = expectedTargetProfile
		}
		if profileCode != expectedProfile {
			return resource.SiteTransferResult{}, resource.ErrIncompatibleTargetSite
		}
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE core.resources IN SHARE ROW EXCLUSIVE MODE;`); err != nil {
		return resource.SiteTransferResult{}, fmt.Errorf("lock resources for site transfer: %w", err)
	}

	var (
		storedSiteID   site.ID
		storedVersion  int64
		deleted        bool
		storedPath     *string
		oldParentValue *int64
	)
	if err := tx.QueryRow(ctx, `
SELECT item.site_id, entity.version, item.deleted_at IS NOT NULL, item.path, item.parent_id
FROM core.resources item
JOIN core.resource_entities entity ON entity.id=item.id
WHERE item.id=$1
FOR UPDATE OF item,entity;`, id).Scan(&storedSiteID, &storedVersion, &deleted, &storedPath, &oldParentValue); errors.Is(err, pgx.ErrNoRows) {
		return resource.SiteTransferResult{}, resource.ErrNotFound
	} else if err != nil {
		return resource.SiteTransferResult{}, translateError(err)
	}
	if storedSiteID != sourceSiteID {
		return resource.SiteTransferResult{}, resource.ErrNotFound
	}
	if storedVersion != expectedVersion {
		return resource.SiteTransferResult{}, resource.ErrConflict
	}
	if deleted || storedPath != nil && *storedPath == "/" {
		return resource.SiteTransferResult{}, resource.ErrInvalidTree
	}

	rows, err := tx.Query(ctx, `
WITH RECURSIVE tree(id,depth,path,cycle) AS (
    SELECT id,0,ARRAY[id],false FROM core.resources WHERE id=$1 AND site_id=$2
    UNION ALL
    SELECT child.id,parent.depth+1,parent.path||child.id,child.id=ANY(parent.path)
    FROM core.resources child
    JOIN tree parent ON child.parent_id=parent.id
    WHERE child.site_id=$2 AND NOT parent.cycle
)
SELECT id,cycle FROM tree ORDER BY depth,id;`, id, sourceSiteID)
	if err != nil {
		return resource.SiteTransferResult{}, translateError(err)
	}
	ids := make([]resource.ID, 0)
	for rows.Next() {
		var current resource.ID
		var cycle bool
		if err := rows.Scan(&current, &cycle); err != nil {
			rows.Close()
			return resource.SiteTransferResult{}, translateError(err)
		}
		if cycle {
			rows.Close()
			return resource.SiteTransferResult{}, resource.ErrInvalidTree
		}
		ids = append(ids, current)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return resource.SiteTransferResult{}, translateError(err)
	}
	rows.Close()
	if len(ids) == 0 {
		return resource.SiteTransferResult{}, resource.ErrNotFound
	}
	idValues := make([]int64, len(ids))
	for index, current := range ids {
		idValues[index] = int64(current)
	}

	var crossesBoundary bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM core.resources item
    WHERE item.target_resource_id = ANY($1::bigint[])
      AND NOT (item.id = ANY($1::bigint[]))
    UNION ALL
    SELECT 1 FROM core.resources item
    WHERE item.id = ANY($1::bigint[])
      AND item.target_resource_id IS NOT NULL
      AND NOT (item.target_resource_id = ANY($1::bigint[]))
);`, idValues).Scan(&crossesBoundary); err != nil {
		return resource.SiteTransferResult{}, translateError(err)
	}
	if crossesBoundary {
		return resource.SiteTransferResult{}, resource.ErrCrossSiteReference
	}

	items := make([]resource.Resource, len(ids))
	paths := make(map[resource.ID]*string, len(ids))
	for index, currentID := range ids {
		item, err := routeResourceByID(ctx, tx, currentID)
		if err != nil {
			return resource.SiteTransferResult{}, err
		}
		item.SiteID = targetSiteID
		if currentID == id {
			item.ParentID = nil
		}
		if item.Path != nil {
			var next string
			if item.ParentID == nil {
				if strings.TrimSpace(item.Slug) == "" {
					return resource.SiteTransferResult{}, resource.ErrInvalidTree
				}
				next = "/" + item.Slug
			} else {
				parentPath, exists := paths[*item.ParentID]
				if !exists || parentPath == nil {
					return resource.SiteTransferResult{}, resource.ErrInvalidTree
				}
				if *parentPath == "/" {
					next = "/" + item.Slug
				} else {
					next = *parentPath + "/" + item.Slug
				}
			}
			item.Path = &next
		}
		paths[item.ID] = item.Path
		items[index] = item
	}

	prospectivePaths := make([]string, 0, len(items))
	for _, item := range items {
		if item.Path == nil {
			continue
		}
		var conflict bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core.resources WHERE site_id=$1 AND path=$2);`, targetSiteID, *item.Path).Scan(&conflict); err != nil {
			return resource.SiteTransferResult{}, translateError(err)
		}
		if conflict {
			return resource.SiteTransferResult{}, resource.ErrRouteConflict
		}
		prospectivePaths = append(prospectivePaths, *item.Path)
	}
	if err := ensureTreePathsAvailable(ctx, tx, targetSiteID, prospectivePaths, nil); err != nil {
		return resource.SiteTransferResult{}, err
	}
	if err := ensureTransferredLibraryRoutesAvailable(ctx, tx, targetSiteID, items); err != nil {
		return resource.SiteTransferResult{}, err
	}

	hookStates, err := r.subtreeStates(ctx, tx, id, true)
	if err != nil {
		return resource.SiteTransferResult{}, err
	}
	hookSiblings, err := r.relatedStates(ctx, tx, id, sourceSiteID, resourceIDFromInt64(oldParentValue), resourceIDFromInt64(oldParentValue), false, true)
	if err != nil {
		return resource.SiteTransferResult{}, err
	}
	var targetSort int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(sort)+1,0) FROM core.resources WHERE site_id=$1 AND parent_id IS NULL;`, targetSiteID).Scan(&targetSort); err != nil {
		return resource.SiteTransferResult{}, translateError(err)
	}
	versionCommand, err := tx.Exec(ctx, `UPDATE core.resource_entities SET version=version+1 WHERE id=$1 AND site_id=$2 AND version=$3;`, id, sourceSiteID, expectedVersion)
	if err != nil {
		return resource.SiteTransferResult{}, translateError(err)
	}
	if versionCommand.RowsAffected() != 1 {
		return resource.SiteTransferResult{}, resource.ErrConflict
	}
	siblings, err := siblingIDs(ctx, tx, sourceSiteID, resourceIDFromInt64(oldParentValue), id)
	if err != nil {
		return resource.SiteTransferResult{}, err
	}
	if err := updateSiblingPositions(ctx, tx, siblings, 0); err != nil {
		return resource.SiteTransferResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE core.resources SET parent_id=NULL,path=NULL,sort=$2 WHERE id=$1;`, id, targetSort); err != nil {
		return resource.SiteTransferResult{}, translateError(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.resources SET path=NULL WHERE id=ANY($1::bigint[]);`, idValues); err != nil {
		return resource.SiteTransferResult{}, translateError(err)
	}

	command, err := tx.Exec(ctx, `
WITH RECURSIVE tree AS (
    SELECT id,type FROM core.resources WHERE id=$1 AND site_id=$2
    UNION ALL
    SELECT child.id,child.type FROM core.resources child JOIN tree parent ON child.parent_id=parent.id
    WHERE child.site_id=$2
), owned_entities AS (
    SELECT id FROM tree
    UNION
    SELECT item.id FROM core.library_items item JOIN tree library ON library.id=item.library_id AND library.type='library'
)
UPDATE core.resource_entities entity
SET site_id=$3
WHERE entity.site_id=$2 AND entity.id IN (SELECT id FROM owned_entities);`, id, sourceSiteID, targetSiteID)
	if err != nil {
		return resource.SiteTransferResult{}, translateError(err)
	}
	if command.RowsAffected() < int64(len(ids)) {
		return resource.SiteTransferResult{}, resource.ErrConflict
	}
	for _, item := range items {
		if _, err := tx.Exec(ctx, `UPDATE core.resources SET path=$2,updated_at=now(),updated_by=$3 WHERE id=$1;`, item.ID, item.Path, actorID); err != nil {
			return resource.SiteTransferResult{}, translateError(err)
		}
	}

	updated, err := routeResourceByID(ctx, tx, id)
	if err != nil {
		return resource.SiteTransferResult{}, err
	}
	loaded := []resource.Resource{updated}
	if err := loadResourceWidgets(ctx, tx, loaded); err != nil {
		return resource.SiteTransferResult{}, err
	}
	if err := loadResourceFields(ctx, tx, loaded); err != nil {
		return resource.SiteTransferResult{}, err
	}
	updated = loaded[0]
	if err := tx.QueryRow(ctx, `SELECT version FROM core.resource_entities WHERE id=$1;`, id).Scan(&updated.Version); err != nil {
		return resource.SiteTransferResult{}, translateError(err)
	}
	if err := r.appendRevision(ctx, tx, updated, resource.RevisionUpdated, nil, actorID); err != nil {
		return resource.SiteTransferResult{}, err
	}
	for _, before := range hookStates {
		if before.ID != id {
			if _, err := tx.Exec(ctx, `UPDATE core.resource_entities SET version=version+1 WHERE id=$1`, before.ID); err != nil {
				return resource.SiteTransferResult{}, err
			}
		}
		state, err := r.eventState(ctx, tx, before.ID)
		if err != nil {
			return resource.SiteTransferResult{}, err
		}
		if _, err := resource.PrepareMutation(ctx, &before, state); err != nil {
			return resource.SiteTransferResult{}, err
		}
		if err := r.appendStateEvent(ctx, tx, resource.EventUpdated, state, actorID); err != nil {
			return resource.SiteTransferResult{}, err
		}
	}
	if err := r.finishRelated(ctx, tx, hookSiblings, actorID); err != nil {
		return resource.SiteTransferResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return resource.SiteTransferResult{}, translateError(err)
	}
	return resource.SiteTransferResult{Resource: updated, ResourceIDs: ids}, nil
}

func ensureTransferredLibraryRoutesAvailable(
	ctx context.Context,
	tx pgx.Tx,
	targetSiteID site.ID,
	items []resource.Resource,
) error {
	libraryIDs := make([]int64, 0)
	libraryPaths := make([]string, 0)
	libraryPatterns := make([]string, 0)
	for _, item := range items {
		if item.Type != resourcetype.Library || item.Path == nil {
			continue
		}
		pattern, _ := item.TypeSettings["item_url_pattern"].(string)
		if pattern == "" {
			pattern = resourcetype.DefaultItemURLPattern
		}
		libraryIDs = append(libraryIDs, int64(item.ID))
		libraryPaths = append(libraryPaths, *item.Path)
		libraryPatterns = append(libraryPatterns, pattern)
	}
	if len(libraryIDs) == 0 {
		return nil
	}

	var conflict bool
	if err := tx.QueryRow(ctx, `
WITH moved_libraries AS (
    SELECT * FROM unnest($1::bigint[], $2::text[], $3::text[])
        AS value(library_id, path, pattern)
), moved_routes AS (
    SELECT item.id,
           rtrim(library.path, '/') ||
           replace(replace(replace(replace(replace(
               library.pattern,
               '{id}', item.id::text),
               '{slug}', item.slug),
               '{year}', to_char(coalesce(item.published_at,item.created_at) AT TIME ZONE 'UTC','YYYY')),
               '{month}', to_char(coalesce(item.published_at,item.created_at) AT TIME ZONE 'UTC','MM')),
               '{day}', to_char(coalesce(item.published_at,item.created_at) AT TIME ZONE 'UTC','DD')) AS path
    FROM moved_libraries library
    JOIN core.library_items item ON item.library_id=library.library_id
), target_routes AS (
    SELECT item.id,
           rtrim(library.path, '/') ||
           replace(replace(replace(replace(replace(
               coalesce(nullif(library.type_settings->>'item_url_pattern',''),'/{slug}'),
               '{id}', item.id::text),
               '{slug}', item.slug),
               '{year}', to_char(coalesce(item.published_at,item.created_at) AT TIME ZONE 'UTC','YYYY')),
               '{month}', to_char(coalesce(item.published_at,item.created_at) AT TIME ZONE 'UTC','MM')),
               '{day}', to_char(coalesce(item.published_at,item.created_at) AT TIME ZONE 'UTC','DD')) AS path
    FROM core.resources library
    JOIN core.library_items item ON item.library_id=library.id AND item.site_id=library.site_id
    WHERE library.site_id=$4 AND library.type='library' AND library.path IS NOT NULL
)
SELECT EXISTS (
    SELECT 1 FROM moved_routes moved
    JOIN core.resources tree ON tree.site_id=$4 AND tree.path=moved.path
    UNION ALL
    SELECT 1 FROM moved_routes moved JOIN target_routes target ON target.path=moved.path
);`, libraryIDs, libraryPaths, libraryPatterns, targetSiteID).Scan(&conflict); err != nil {
		return translateError(err)
	}
	if conflict {
		return resource.ErrRouteConflict
	}
	return nil
}

var _ resource.SiteTransferRepository = (*Repository)(nil)
