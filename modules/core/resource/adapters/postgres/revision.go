package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

func snapshotFromResource(item resource.Resource) resource.Snapshot {
	return resource.SnapshotFromResource(item)
}
func snapshotFromLibraryItem(item resource.LibraryItem) resource.Snapshot {
	return resource.SnapshotFromLibraryItem(item)
}

func (r *Repository) appendRevision(ctx context.Context, tx pgx.Tx, item resource.Resource, kind resource.RevisionKind, sourceVersion *int64, actorID *security.UserID) error {
	raw, err := json.Marshal(snapshotFromResource(item))
	if err != nil {
		return fmt.Errorf("encode resource revision snapshot: %w", err)
	}
	return insertRevision(ctx, tx, item.ID, item.SiteID, item.Version, kind, sourceVersion, raw, actorID)
}

func (r *Repository) appendLibraryItemRevision(ctx context.Context, tx pgx.Tx, item resource.LibraryItem, kind resource.RevisionKind, sourceVersion *int64, actorID *security.UserID) error {
	raw, err := json.Marshal(snapshotFromLibraryItem(item))
	if err != nil {
		return fmt.Errorf("encode library item revision snapshot: %w", err)
	}
	return insertRevision(ctx, tx, item.ID, item.SiteID, item.Version, kind, sourceVersion, raw, actorID)
}

func insertRevision(ctx context.Context, tx pgx.Tx, resourceID resource.ID, siteID site.ID, version int64, kind resource.RevisionKind, sourceVersion *int64, raw []byte, actorID *security.UserID) error {
	_, err := tx.Exec(ctx, `
INSERT INTO core.resource_revisions
    (resource_id, site_id, version, kind, source_version, snapshot, created_by, created_by_name)
SELECT $1, $2, $3, $4, $5, $6::jsonb, $7,
       coalesce(nullif(btrim(concat_ws(' ', u.name, u.last_name, u.middle_name)), ''), u.login, '')
FROM (SELECT 1) AS one
LEFT JOIN core.users u ON u.id = $7;`, resourceID, siteID, version, kind, sourceVersion, string(raw), actorID)
	if err != nil {
		return fmt.Errorf("insert resource revision: %w", translateError(err))
	}
	return nil
}

func (r *Repository) appendCurrentRevision(ctx context.Context, tx pgx.Tx, resourceID resource.ID, version int64, actorID *security.UserID) error {
	var storage resource.StorageKind
	if err := tx.QueryRow(ctx, `SELECT storage_kind FROM core.resource_entities WHERE id=$1;`, resourceID).Scan(&storage); err != nil {
		return translateError(err)
	}
	if storage == resource.StorageLibraryItem {
		item, err := r.libraryItemByID(ctx, tx, resourceID, false)
		if err != nil {
			return err
		}
		if err := r.loadLibraryItemFields(ctx, tx, &item); err != nil {
			return err
		}
		item.Version = version
		return r.appendLibraryItemRevision(ctx, tx, item, resource.RevisionUpdated, nil, actorID)
	}
	item, err := scanResource(tx.QueryRow(ctx, `
SELECT id, site_id, parent_id, type, template, content_type,
       title, menu_title, slug, path, annotation, content, image_media_id,
       target_resource_id, external_url, is_public, is_searchable, in_menu,
       in_sitemap, sort, published_at, unpublished_at, type_settings, created_at,
       updated_at, created_by, updated_by, deleted_at, deleted_by
FROM core.resources WHERE id=$1;`, resourceID))
	if err != nil {
		return translateError(err)
	}
	items := []resource.Resource{item}
	if err := loadResourceFields(ctx, tx, items); err != nil {
		return err
	}
	if err := loadResourceWidgets(ctx, tx, items); err != nil {
		return err
	}
	items[0].Version = version
	return r.appendRevision(ctx, tx, items[0], resource.RevisionUpdated, nil, actorID)
}

func (r *Repository) ListRevisions(ctx context.Context, siteID site.ID, resourceID resource.ID, page, perPage int) (resource.RevisionPage, error) {
	if ctx == nil || siteID <= 0 || resourceID <= 0 || page < 1 || perPage < 1 || perPage > 100 {
		return resource.RevisionPage{}, resource.ErrInvalid
	}
	result := resource.RevisionPage{Page: page, PerPage: perPage, Items: []resource.Revision{}}
	if err := r.connector.Pool().QueryRow(ctx, `SELECT count(*) FROM core.resource_revisions WHERE site_id=$1 AND resource_id=$2;`, siteID, resourceID).Scan(&result.Total); err != nil {
		return resource.RevisionPage{}, translateError(err)
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT id, resource_id, site_id, version, kind, source_version, created_at, created_by, created_by_name
FROM core.resource_revisions
WHERE site_id=$1 AND resource_id=$2
ORDER BY version DESC
LIMIT $3 OFFSET $4;`, siteID, resourceID, perPage, (page-1)*perPage)
	if err != nil {
		return resource.RevisionPage{}, translateError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var item resource.Revision
		if err := rows.Scan(&item.ID, &item.ResourceID, &item.SiteID, &item.Version, &item.Kind, &item.SourceVersion, &item.CreatedAt, &item.CreatedBy, &item.CreatedByName); err != nil {
			return resource.RevisionPage{}, translateError(err)
		}
		result.Items = append(result.Items, item)
	}
	return result, translateError(rows.Err())
}

func (r *Repository) Revision(ctx context.Context, siteID site.ID, resourceID resource.ID, version int64) (resource.Revision, error) {
	var item resource.Revision
	var raw []byte
	err := r.connector.Pool().QueryRow(ctx, `
SELECT id, resource_id, site_id, version, kind, source_version, snapshot, created_at, created_by, created_by_name
FROM core.resource_revisions WHERE site_id=$1 AND resource_id=$2 AND version=$3;`, siteID, resourceID, version).Scan(
		&item.ID, &item.ResourceID, &item.SiteID, &item.Version, &item.Kind, &item.SourceVersion, &raw, &item.CreatedAt, &item.CreatedBy, &item.CreatedByName)
	if errors.Is(err, pgx.ErrNoRows) {
		return resource.Revision{}, resource.ErrRevisionNotFound
	}
	if err != nil {
		return resource.Revision{}, translateError(err)
	}
	var snapshot resource.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return resource.Revision{}, fmt.Errorf("decode resource revision snapshot: %w", err)
	}
	item.Snapshot = &snapshot
	return item, nil
}

func (r *Repository) PurgeRevisions(ctx context.Context, siteID site.ID, resourceID resource.ID) (int64, error) {
	command, err := r.connector.Pool().Exec(ctx, `DELETE FROM core.resource_revisions WHERE site_id=$1 AND resource_id=$2;`, siteID, resourceID)
	if err != nil {
		return 0, translateError(err)
	}
	return command.RowsAffected(), nil
}

func (r *Repository) CountRevisions(ctx context.Context) (int64, error) {
	var count int64
	if err := r.connector.Pool().QueryRow(ctx, `SELECT count(*) FROM core.resource_revisions;`).Scan(&count); err != nil {
		return 0, translateError(err)
	}
	return count, nil
}

func (r *Repository) PurgeAllRevisions(ctx context.Context) (int64, error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return 0, translateError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var count int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.resource_revisions;`).Scan(&count); err != nil {
		return 0, translateError(err)
	}
	if _, err := tx.Exec(ctx, `TRUNCATE core.resource_revisions;`); err != nil {
		return 0, translateError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, translateError(err)
	}
	return count, nil
}

func (r *Repository) RestoreRevision(ctx context.Context, actorID *security.UserID, current, candidate resource.Resource, sourceVersion int64) (_ resource.Resource, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return resource.Resource{}, translateError(err)
	}
	defer func() {
		if resultErr != nil {
			_ = tx.Rollback(context.Background())
		}
	}()
	if current.ID != candidate.ID || current.SiteID != candidate.SiteID || current.Version <= 0 {
		return resource.Resource{}, resource.ErrInvalidReference
	}
	if err := lockRouteNamespace(ctx, tx, candidate.SiteID); err != nil {
		return resource.Resource{}, err
	}
	lockedForHooks, err := r.treeInTransaction(ctx, tx, current.ID)
	if err != nil {
		return resource.Resource{}, err
	}
	if lockedForHooks.Version != current.Version {
		return resource.Resource{}, resource.ErrConflict
	}
	candidate, err = resource.PrepareResourceMutation(ctx, &lockedForHooks, candidate)
	if err != nil {
		return resource.Resource{}, err
	}
	hookRelated, err := r.relatedChangeStates(ctx, tx, lockedForHooks, candidate)
	if err != nil {
		return resource.Resource{}, err
	}
	paths, err := prospectiveTreePaths(ctx, tx, candidate.ID, candidate.Path)
	if err != nil {
		return resource.Resource{}, err
	}
	if err := ensureTreePathsAvailable(ctx, tx, candidate.SiteID, paths, &candidate); err != nil {
		return resource.Resource{}, err
	}
	if candidate.Type == resourcetype.Library && (!sameOptionalText(current.Path, candidate.Path) || !reflect.DeepEqual(current.TypeSettings, candidate.TypeSettings)) {
		if err := ensureProspectiveLibraryNamespaceAvailable(ctx, tx, candidate); err != nil {
			return resource.Resource{}, err
		}
	}
	if err := lockRevisionMedia(ctx, tx, current.ImageMediaID, candidate.ImageMediaID, candidate.ID); err != nil {
		return resource.Resource{}, err
	}
	if err := tx.QueryRow(ctx, `UPDATE core.resource_entities SET version=version+1 WHERE id=$1 AND site_id=$2 AND version=$3 RETURNING version;`, current.ID, current.SiteID, current.Version).Scan(&candidate.Version); errors.Is(err, pgx.ErrNoRows) {
		return resource.Resource{}, resource.ErrConflict
	} else if err != nil {
		return resource.Resource{}, translateError(err)
	}
	candidate.Sort, err = reorderSiblings(ctx, tx, candidate.SiteID, candidate.ID, current.ParentID, candidate.ParentID, candidate.Sort)
	if err != nil {
		return resource.Resource{}, err
	}
	rawSettings, err := json.Marshal(candidate.TypeSettings)
	if err != nil {
		return resource.Resource{}, err
	}
	command, err := tx.Exec(ctx, `
UPDATE core.resources SET parent_id=$3,type=$4,template=$5,content_type=$6,title=$7,menu_title=$8,
 slug=$9,path=$10,annotation=$11,content=$12,image_media_id=$13,target_resource_id=$14,external_url=$15,
 is_public=$16,is_searchable=$17,in_menu=$18,in_sitemap=$19,sort=$20,published_at=$21,unpublished_at=$22,
 type_settings=$23::jsonb,updated_at=now(),updated_by=$24
WHERE id=$1 AND site_id=$2;`, candidate.ID, candidate.SiteID, candidate.ParentID, candidate.Type, candidate.Template,
		candidate.ContentType, candidate.Title, candidate.MenuTitle, candidate.Slug, candidate.Path, candidate.Annotation,
		candidate.Content, candidate.ImageMediaID, candidate.TargetResourceID, candidate.ExternalURL, candidate.IsPublic,
		candidate.IsSearchable, candidate.InMenu, candidate.InSitemap, candidate.Sort, candidate.PublishedAt,
		candidate.UnpublishedAt, string(rawSettings), actorID)
	if err != nil {
		return resource.Resource{}, translateError(err)
	}
	if command.RowsAffected() != 1 {
		return resource.Resource{}, resource.ErrNotFound
	}
	if err := replaceResourceFields(ctx, tx, candidate.ID, candidate.SiteID, nil, candidate.FieldValues); err != nil {
		return resource.Resource{}, err
	}
	if err := replaceFileReferences(ctx, tx, candidate.ID, candidate.FileReferences); err != nil {
		return resource.Resource{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.resource_widgets WHERE resource_id=$1;`, candidate.ID); err != nil {
		return resource.Resource{}, translateError(err)
	}
	for _, binding := range candidate.Widgets {
		rawParams, encodeErr := json.Marshal(binding.Params)
		if encodeErr != nil {
			return resource.Resource{}, encodeErr
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.resource_widgets
 (resource_id,widget_code,area,position,view,columns,margin_top,margin_bottom,enabled,params,param_bindings)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb);`, candidate.ID, binding.Code, binding.Area,
			binding.Position, binding.Presentation.View, binding.Presentation.Columns, binding.Presentation.MarginTop,
			binding.Presentation.MarginBottom, binding.Presentation.Enabled, string(rawParams), nonNilParamBindings(binding.ParamBindings)); err != nil {
			return resource.Resource{}, translateError(err)
		}
	}
	if _, err := tx.Exec(ctx, `
WITH RECURSIVE tree AS (
 SELECT id,path FROM core.resources WHERE id=$1
 UNION ALL
 SELECT child.id, CASE WHEN child.path IS NULL OR tree.path IS NULL THEN NULL WHEN tree.path='/' THEN '/'||child.slug ELSE tree.path||'/'||child.slug END
 FROM core.resources child JOIN tree ON child.parent_id=tree.id)
UPDATE core.resources item SET path=tree.path,updated_at=now(),updated_by=$2 FROM tree WHERE item.id=tree.id AND item.id<>$1;`, candidate.ID, actorID); err != nil {
		return resource.Resource{}, translateError(err)
	}
	if err := r.appendRevision(ctx, tx, candidate, resource.RevisionRestored, &sourceVersion, actorID); err != nil {
		return resource.Resource{}, err
	}
	if !sameMediaID(current.ImageMediaID, candidate.ImageMediaID) && current.ImageMediaID != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM core.media WHERE id=$1;`, *current.ImageMediaID); err != nil {
			return resource.Resource{}, translateError(err)
		}
	}
	if err := r.finishRelated(ctx, tx, hookRelated, actorID); err != nil {
		return resource.Resource{}, err
	}
	if err := r.appendResourceEvent(ctx, tx, resource.EventUpdated, candidate.ID, candidate.SiteID, resource.StorageTree, candidate.Version, actorID); err != nil {
		return resource.Resource{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return resource.Resource{}, translateError(err)
	}
	return candidate, nil
}

func (r *Repository) RestoreLibraryItemRevision(ctx context.Context, actorID *security.UserID, current, candidate resource.LibraryItem, sourceVersion int64) (_ resource.LibraryItem, resultErr error) {
	return retryLibraryItemTransaction(ctx, func() (resource.LibraryItem, error) {
		return r.restoreLibraryItemRevisionOnce(ctx, actorID, current, candidate, sourceVersion)
	})
}

func (r *Repository) restoreLibraryItemRevisionOnce(ctx context.Context, actorID *security.UserID, current, candidate resource.LibraryItem, sourceVersion int64) (_ resource.LibraryItem, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	defer func() {
		if resultErr != nil {
			_ = tx.Rollback(context.Background())
		}
	}()
	locked, err := r.libraryInTransaction(ctx, tx, current.ID)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if locked.SiteID != current.SiteID || locked.Version != current.Version || current.Version <= 0 {
		return resource.LibraryItem{}, resource.ErrConflict
	}
	candidate, err = r.prepareLibraryMutation(ctx, tx, &locked, candidate)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if err := lockRouteNamespace(ctx, tx, candidate.SiteID); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := lockRevisionMedia(ctx, tx, locked.ImageMediaID, candidate.ImageMediaID, candidate.ID); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := ensureLibraryTarget(ctx, tx, candidate.SiteID, candidate.LibraryID); err != nil {
		return resource.LibraryItem{}, err
	}
	library, err := routeResourceByID(ctx, tx, candidate.LibraryID)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if err := ensureLibraryItemRouteAvailable(ctx, tx, library, candidate); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := tx.QueryRow(ctx, `UPDATE core.resource_entities SET version=version+1 WHERE id=$1 AND site_id=$2 AND version=$3 RETURNING version;`, current.ID, current.SiteID, current.Version).Scan(&candidate.Version); errors.Is(err, pgx.ErrNoRows) {
		return resource.LibraryItem{}, resource.ErrConflict
	} else if err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	partitionAt := locked.CreatedAt
	if candidate.PublishedAt != nil {
		partitionAt = candidate.PublishedAt.UTC()
	}
	if _, err := tx.Exec(ctx, `UPDATE core.library_item_routes SET library_id=$2,slug=$3 WHERE resource_id=$1;`, candidate.ID, candidate.LibraryID, candidate.Slug); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	restored, err := scanLibraryItem(tx.QueryRow(ctx, `
UPDATE core.library_items AS item SET library_id=$2,partition_at=$3,template=$4,content_type=$5,title=$6,slug=$7,
 annotation=$8,content=$9,image_media_id=$10,is_public=$11,is_searchable=$12,published_at=$13,unpublished_at=$14,
 updated_at=now(),updated_by=$15
WHERE id=$1 RETURNING `+libraryItemColumns+`;`, candidate.ID, candidate.LibraryID, partitionAt, candidate.Template,
		candidate.ContentType, candidate.Title, candidate.Slug, candidate.Annotation, candidate.Content, candidate.ImageMediaID,
		candidate.IsPublic, candidate.IsSearchable, candidate.PublishedAt, candidate.UnpublishedAt, actorID))
	if err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	if err := ensureLibraryItemTemplateUsage(ctx, tx, restored.SiteID, restored.LibraryID, restored.Template); err != nil {
		return resource.LibraryItem{}, err
	}
	if locked.LibraryID != restored.LibraryID || !sameTemplateCode(locked.Template, restored.Template) {
		if err := pruneLibraryItemTemplateUsage(ctx, tx, locked.SiteID, locked.LibraryID, locked.Template); err != nil {
			return resource.LibraryItem{}, err
		}
	}
	if err := replaceResourceFields(ctx, tx, candidate.ID, candidate.SiteID, &candidate.LibraryID, candidate.FieldValues); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := replaceFileReferences(ctx, tx, candidate.ID, candidate.FileReferences); err != nil {
		return resource.LibraryItem{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.resource_widgets WHERE resource_id=$1;`, candidate.ID); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	for _, binding := range candidate.Widgets {
		rawParams, encodeErr := json.Marshal(binding.Params)
		if encodeErr != nil {
			return resource.LibraryItem{}, encodeErr
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.resource_widgets
 (resource_id,widget_code,area,position,view,columns,margin_top,margin_bottom,enabled,params,param_bindings)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb);`, candidate.ID, binding.Code, binding.Area,
			binding.Position, binding.Presentation.View, binding.Presentation.Columns, binding.Presentation.MarginTop,
			binding.Presentation.MarginBottom, binding.Presentation.Enabled, string(rawParams), nonNilParamBindings(binding.ParamBindings)); err != nil {
			return resource.LibraryItem{}, translateError(err)
		}
	}
	restored.Version = candidate.Version
	restored.Fields, restored.FieldValues, restored.FileReferences = candidate.Fields, candidate.FieldValues, candidate.FileReferences
	restored.Widgets = widget.CloneBindings(candidate.Widgets)
	if err := r.appendLibraryItemRevision(ctx, tx, restored, resource.RevisionRestored, &sourceVersion, actorID); err != nil {
		return resource.LibraryItem{}, err
	}
	if !sameMediaID(locked.ImageMediaID, candidate.ImageMediaID) && locked.ImageMediaID != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM core.media WHERE id=$1;`, *locked.ImageMediaID); err != nil {
			return resource.LibraryItem{}, translateError(err)
		}
	}
	if err := r.appendResourceEvent(ctx, tx, resource.EventUpdated, restored.ID, restored.SiteID, resource.StorageLibraryItem, restored.Version, actorID); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	return restored, nil
}

func lockRevisionMedia(ctx context.Context, tx pgx.Tx, current, candidate *media.ID, resourceID resource.ID) error {
	ids := make([]media.ID, 0, 2)
	if current != nil {
		ids = append(ids, *current)
	}
	if candidate != nil {
		ids = append(ids, *candidate)
	}
	if len(ids) > 0 {
		if err := medialock.Lock(ctx, tx, ids...); err != nil {
			return err
		}
	}
	if candidate != nil {
		return ensureMediaAvailable(ctx, tx, *candidate, resourceID)
	}
	return nil
}

func revisionWidgets(snapshot resource.Snapshot) []widget.Binding {
	result := make([]widget.Binding, len(snapshot.Widgets))
	for index, item := range snapshot.Widgets {
		result[index] = widget.Binding{Code: item.Code, Area: item.Area, Position: item.Position,
			Presentation: widget.Presentation{View: item.View, Columns: item.Columns, MarginTop: item.MarginTop, MarginBottom: item.MarginBottom, Enabled: item.Enabled}, Params: item.Params, ParamBindings: widget.CloneParamBindings(item.ParamBindings)}
	}
	return result
}

var _ resource.RevisionRepository = (*Repository)(nil)
