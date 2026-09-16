package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

const libraryItemColumns = `
    item.id, item.site_id, item.library_id, item.template, item.content_type,
    item.title, item.slug, item.annotation, item.content, item.image_media_id,
    item.is_public, item.is_searchable, item.published_at, item.unpublished_at,
    item.created_at, item.updated_at, item.created_by, item.updated_by,
    item.deleted_at, item.deleted_by`

type libraryRowQueryer interface {
	rowQueryer
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *Repository) CreateLibraryItem(ctx context.Context, actorID *security.UserID, item resource.LibraryItem, recordRevision bool) (_ resource.LibraryItem, resultErr error) {
	if ctx == nil {
		return resource.LibraryItem{}, errors.New("create library item context is nil")
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	defer func() {
		if resultErr != nil {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := lockRouteNamespace(ctx, tx, item.SiteID); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := ensureLibraryTarget(ctx, tx, item.SiteID, item.LibraryID); err != nil {
		return resource.LibraryItem{}, err
	}
	library, err := routeResourceByID(ctx, tx, item.LibraryID)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	item, err = r.prepareLibraryMutation(ctx, tx, nil, item)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if item.ImageMediaID != nil {
		if err := medialock.Lock(ctx, tx, *item.ImageMediaID); err != nil {
			return resource.LibraryItem{}, err
		}
		if err := ensureMediaAvailable(ctx, tx, *item.ImageMediaID, 0); err != nil {
			return resource.LibraryItem{}, err
		}
	}
	if err := tx.QueryRow(ctx, `INSERT INTO core.resource_entities (site_id, storage_kind) VALUES ($1, 'library_item') RETURNING id;`, item.SiteID).Scan(&item.ID); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	item.CreatedAt = time.Now().UTC()
	partitionAt := item.CreatedAt
	if item.PublishedAt != nil {
		partitionAt = item.PublishedAt.UTC()
	}
	if err := ensureLibraryItemRouteAvailable(ctx, tx, library, item); err != nil {
		return resource.LibraryItem{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.library_item_routes (resource_id, site_id, library_id, slug) VALUES ($1, $2, $3, $4);`, item.ID, item.SiteID, item.LibraryID, item.Slug); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	stored, err := scanLibraryItem(tx.QueryRow(ctx, `
INSERT INTO core.library_items AS item (
    id, site_id, library_id, partition_at, template, content_type, title, slug,
    annotation, content, image_media_id, is_public, is_searchable,
    published_at, unpublished_at, created_at, created_by, updated_by
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17)
RETURNING `+libraryItemColumns+`;`, item.ID, item.SiteID, item.LibraryID, partitionAt, item.Template, item.ContentType, item.Title, item.Slug, item.Annotation, item.Content, item.ImageMediaID, item.IsPublic, item.IsSearchable, item.PublishedAt, item.UnpublishedAt, item.CreatedAt, actorID))
	if err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	if err := ensureLibraryItemTemplateUsage(ctx, tx, stored.SiteID, stored.LibraryID, stored.Template); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := replaceResourceFields(ctx, tx, stored.ID, stored.SiteID, &stored.LibraryID, item.FieldValues); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := replaceFileReferences(ctx, tx, stored.ID, item.FileReferences); err != nil {
		return resource.LibraryItem{}, err
	}
	stored.Fields, stored.FieldValues, stored.FileReferences = item.Fields, item.FieldValues, item.FileReferences
	stored.Widgets = widget.CloneBindings(item.Widgets)
	stored.Version = 1
	if recordRevision {
		if err := r.appendLibraryItemRevision(ctx, tx, stored, resource.RevisionCreated, nil, actorID); err != nil {
			return resource.LibraryItem{}, err
		}
	}
	if err := r.appendResourceEvent(ctx, tx, resource.EventCreated, stored.ID, stored.SiteID, resource.StorageLibraryItem, stored.Version, actorID); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	return stored, nil
}

func (r *Repository) LibraryItemByID(ctx context.Context, id resource.ID) (resource.LibraryItem, error) {
	if ctx == nil || id <= 0 {
		return resource.LibraryItem{}, resource.ErrInvalid
	}
	item, err := r.libraryItemByID(ctx, r.connector.Pool(), id, false)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if err := r.loadLibraryItemFields(ctx, r.connector.Pool(), &item); err != nil {
		return resource.LibraryItem{}, err
	}
	return item, nil
}

func (r *Repository) libraryItemByID(ctx context.Context, queryer libraryRowQueryer, id resource.ID, lock bool) (resource.LibraryItem, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF item"
	}
	item, err := scanLibraryItem(queryer.QueryRow(ctx, `
SELECT `+libraryItemColumns+`
FROM core.library_item_routes route
JOIN core.library_items item
  ON item.id = route.resource_id AND item.library_id = route.library_id
WHERE route.resource_id = $1`+suffix+`;`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return resource.LibraryItem{}, resource.ErrNotFound
	}
	if err != nil {
		return resource.LibraryItem{}, fmt.Errorf("query library item %d: %w", id, err)
	}
	if err := queryer.QueryRow(ctx, `SELECT version FROM core.resource_entities WHERE id=$1;`, id).Scan(&item.Version); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	return item, nil
}

func (r *Repository) UpdateLibraryItem(ctx context.Context, actorID *security.UserID, current, item resource.LibraryItem, recordRevision bool) (_ resource.LibraryItem, resultErr error) {
	return retryLibraryItemTransaction(ctx, func() (resource.LibraryItem, error) {
		return r.updateLibraryItemOnce(ctx, actorID, current, item, recordRevision)
	})
}

func (r *Repository) updateLibraryItemOnce(ctx context.Context, actorID *security.UserID, current, item resource.LibraryItem, recordRevision bool) (_ resource.LibraryItem, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return resource.LibraryItem{}, err
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
	if current.Version <= 0 || locked.Version != current.Version {
		return resource.LibraryItem{}, resource.ErrConflict
	}
	item, err = r.prepareLibraryMutation(ctx, tx, &locked, item)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if err := lockRouteNamespace(ctx, tx, locked.SiteID); err != nil {
		return resource.LibraryItem{}, err
	}
	library, err := routeResourceByID(ctx, tx, locked.LibraryID)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if err := ensureLibraryItemRouteAvailable(ctx, tx, library, item); err != nil {
		return resource.LibraryItem{}, err
	}
	var nextVersion int64
	if err := tx.QueryRow(ctx, `UPDATE core.resource_entities SET version=version+1 WHERE id=$1 AND version=$2 RETURNING version;`, item.ID, current.Version).Scan(&nextVersion); errors.Is(err, pgx.ErrNoRows) {
		return resource.LibraryItem{}, resource.ErrConflict
	} else if err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	if item.ImageMediaID != nil || locked.ImageMediaID != nil {
		ids := make([]media.ID, 0, 2)
		if item.ImageMediaID != nil {
			ids = append(ids, *item.ImageMediaID)
		}
		if locked.ImageMediaID != nil {
			ids = append(ids, *locked.ImageMediaID)
		}
		if err := medialock.Lock(ctx, tx, ids...); err != nil {
			return resource.LibraryItem{}, err
		}
		if item.ImageMediaID != nil {
			if err := ensureMediaAvailable(ctx, tx, *item.ImageMediaID, item.ID); err != nil {
				return resource.LibraryItem{}, err
			}
		}
	}
	partitionAt := locked.CreatedAt
	if item.PublishedAt != nil {
		partitionAt = item.PublishedAt.UTC()
	}
	if _, err := tx.Exec(ctx, `UPDATE core.library_item_routes SET slug = $2 WHERE resource_id = $1;`, item.ID, item.Slug); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	updated, err := scanLibraryItem(tx.QueryRow(ctx, `
UPDATE core.library_items AS item SET
    partition_at=$2, template=$3, content_type=$4, title=$5, slug=$6,
    annotation=$7, content=$8, image_media_id=$9, is_public=$10,
    is_searchable=$11, published_at=$12, unpublished_at=$13,
    updated_at=now(), updated_by=$14
WHERE id=$1 AND library_id=$15
RETURNING `+libraryItemColumns+`;`, item.ID, partitionAt, item.Template, item.ContentType, item.Title, item.Slug, item.Annotation, item.Content, item.ImageMediaID, item.IsPublic, item.IsSearchable, item.PublishedAt, item.UnpublishedAt, actorID, locked.LibraryID))
	if errors.Is(err, pgx.ErrNoRows) {
		return resource.LibraryItem{}, resource.ErrNotFound
	}
	if err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	if err := ensureLibraryItemTemplateUsage(ctx, tx, updated.SiteID, updated.LibraryID, updated.Template); err != nil {
		return resource.LibraryItem{}, err
	}
	if !sameTemplateCode(locked.Template, updated.Template) {
		if err := pruneLibraryItemTemplateUsage(ctx, tx, locked.SiteID, locked.LibraryID, locked.Template); err != nil {
			return resource.LibraryItem{}, err
		}
	}
	if err := replaceResourceFields(ctx, tx, updated.ID, updated.SiteID, &updated.LibraryID, item.FieldValues); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := replaceFileReferences(ctx, tx, updated.ID, item.FileReferences); err != nil {
		return resource.LibraryItem{}, err
	}
	updated.Fields, updated.FieldValues, updated.FileReferences = item.Fields, item.FieldValues, item.FileReferences
	updated.Widgets = widget.CloneBindings(item.Widgets)
	updated.Version = nextVersion
	if recordRevision {
		if err := r.appendLibraryItemRevision(ctx, tx, updated, resource.RevisionUpdated, nil, actorID); err != nil {
			return resource.LibraryItem{}, err
		}
	}
	if !sameMediaID(locked.ImageMediaID, item.ImageMediaID) && locked.ImageMediaID != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM core.media WHERE id=$1;`, *locked.ImageMediaID); err != nil {
			return resource.LibraryItem{}, translateError(err)
		}
	}
	if err := r.appendResourceEvent(ctx, tx, resource.EventUpdated, updated.ID, updated.SiteID, resource.StorageLibraryItem, updated.Version, actorID); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	return updated, nil
}

func (r *Repository) SoftDeleteLibraryItem(ctx context.Context, actorID *security.UserID, id resource.ID) error {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	state, err := r.eventState(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := prepareLifecycle(ctx, []resource.EventState{state}, true, true); err != nil {
		return err
	}
	if err := r.finishLifecycle(ctx, tx, []resource.EventState{state}, true, actorID, true); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) RestoreLibraryItem(ctx context.Context, actorID *security.UserID, id resource.ID) error {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	item, err := r.libraryInTransaction(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := lockRouteNamespace(ctx, tx, item.SiteID); err != nil {
		return err
	}
	library, err := routeResourceByID(ctx, tx, item.LibraryID)
	if err != nil || library.DeletedAt != nil {
		return resource.ErrNotFound
	}
	if err := ensureLibraryItemRouteAvailable(ctx, tx, library, item); err != nil {
		return err
	}
	hookState := resource.StateFromLibraryItem(item)
	if err := prepareLifecycle(ctx, []resource.EventState{hookState}, false, true); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE core.library_items SET deleted_at=NULL, deleted_by=NULL, updated_at=now(), updated_by=$2 WHERE id=$1;`, id, actorID)
	if err != nil {
		return translateError(err)
	}
	if command.RowsAffected() == 0 {
		return resource.ErrNotFound
	}
	if err := r.finishLifecycle(ctx, tx, []resource.EventState{hookState}, false, actorID, true); err != nil {
		return err
	}
	return translateError(tx.Commit(ctx))
}

func (r *Repository) DeleteLibraryItem(ctx context.Context, id resource.ID) error {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	item, err := r.libraryInTransaction(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := lockRouteNamespace(ctx, tx, item.SiteID); err != nil {
		return err
	}
	if err := r.appendStateEvent(ctx, tx, resource.EventDeleted, resource.StateFromLibraryItem(item), nil); err != nil {
		return err
	}
	ownedMedia, err := fieldMediaIDs(ctx, tx, []resource.ID{id})
	if err != nil {
		return err
	}
	if item.ImageMediaID != nil {
		ownedMedia = append(ownedMedia, *item.ImageMediaID)
	}
	if err := medialock.Lock(ctx, tx, ownedMedia...); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM core.file_field_references WHERE owner_kind='resource' AND owner_id=$1;`, id); err != nil {
		return translateDeleteError(err)
	}
	command, err := tx.Exec(ctx, `DELETE FROM core.resource_entities WHERE id=$1 AND storage_kind='library_item';`, id)
	if err != nil {
		return translateDeleteError(err)
	}
	if command.RowsAffected() == 0 {
		return resource.ErrNotFound
	}
	if err := pruneLibraryItemTemplateUsage(ctx, tx, item.SiteID, item.LibraryID, item.Template); err != nil {
		return err
	}
	if item.ImageMediaID != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM core.media WHERE id=$1`, *item.ImageMediaID); err != nil {
			return translateDeleteError(err)
		}
	}
	if err := deleteUnusedMedia(ctx, tx, ownedMedia); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *Repository) MoveLibraryItem(ctx context.Context, actorID *security.UserID, id, targetLibraryID resource.ID, expectedVersion int64, recordRevision bool) (_ resource.LibraryItem, resultErr error) {
	return retryLibraryItemTransaction(ctx, func() (resource.LibraryItem, error) {
		return r.moveLibraryItemOnce(ctx, actorID, id, targetLibraryID, expectedVersion, recordRevision)
	})
}

func (r *Repository) moveLibraryItemOnce(ctx context.Context, actorID *security.UserID, id, targetLibraryID resource.ID, expectedVersion int64, recordRevision bool) (_ resource.LibraryItem, resultErr error) {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	defer func() {
		if resultErr != nil {
			_ = tx.Rollback(context.Background())
		}
	}()
	item, err := r.libraryInTransaction(ctx, tx, id)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if item.Version != expectedVersion {
		return resource.LibraryItem{}, resource.ErrConflict
	}
	hookBefore := item
	hookCandidate := item
	hookCandidate.LibraryID = targetLibraryID
	hookCandidate, err = r.prepareLibraryMutation(ctx, tx, &hookBefore, hookCandidate)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	targetLibraryID = hookCandidate.LibraryID
	if err := lockRouteNamespace(ctx, tx, item.SiteID); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := tx.QueryRow(ctx, `UPDATE core.resource_entities SET version=version+1 WHERE id=$1 AND version=$2 RETURNING version;`, id, expectedVersion).Scan(&item.Version); errors.Is(err, pgx.ErrNoRows) {
		return resource.LibraryItem{}, resource.ErrConflict
	} else if err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	if err := ensureLibraryTarget(ctx, tx, item.SiteID, targetLibraryID); err != nil {
		return resource.LibraryItem{}, err
	}
	targetLibrary, err := routeResourceByID(ctx, tx, targetLibraryID)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	prospective := item
	prospective.LibraryID = targetLibraryID
	if err := ensureLibraryItemRouteAvailable(ctx, tx, targetLibrary, prospective); err != nil {
		return resource.LibraryItem{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE core.library_item_routes SET library_id=$2 WHERE resource_id=$1;`, id, targetLibraryID); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	moved, err := scanLibraryItem(tx.QueryRow(ctx, `UPDATE core.library_items AS item SET library_id=$2, updated_at=now(), updated_by=$3 WHERE id=$1 AND library_id=$4 RETURNING `+libraryItemColumns+`;`, id, targetLibraryID, actorID, item.LibraryID))
	if err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	if err := ensureLibraryItemTemplateUsage(ctx, tx, moved.SiteID, moved.LibraryID, moved.Template); err != nil {
		return resource.LibraryItem{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE core.resource_field_values SET library_id=$2 WHERE resource_id=$1;`, id, targetLibraryID); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	if err := pruneLibraryItemTemplateUsage(ctx, tx, item.SiteID, item.LibraryID, item.Template); err != nil {
		return resource.LibraryItem{}, err
	}
	moved.Version = item.Version
	if recordRevision {
		if err := r.loadLibraryItemFields(ctx, tx, &moved); err != nil {
			return resource.LibraryItem{}, err
		}
		if err := r.appendLibraryItemRevision(ctx, tx, moved, resource.RevisionUpdated, nil, actorID); err != nil {
			return resource.LibraryItem{}, err
		}
	}
	if err := r.appendResourceEvent(ctx, tx, resource.EventUpdated, moved.ID, moved.SiteID, resource.StorageLibraryItem, moved.Version, actorID); err != nil {
		return resource.LibraryItem{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return resource.LibraryItem{}, translateError(err)
	}
	if err := r.loadLibraryItemFields(ctx, r.connector.Pool(), &moved); err != nil {
		return resource.LibraryItem{}, err
	}
	return moved, nil
}

func (r *Repository) QueryLibraryItems(ctx context.Context, query resource.LibraryItemQuery) (resource.LibraryItemPage, error) {
	if err := query.Validate(); err != nil {
		return resource.LibraryItemPage{}, err
	}
	sorts, _ := resource.LibraryItemSorts(query)
	primaryCustom := len(sorts) > 0 && resource.IsCustomFieldPath(sorts[0].Field)
	items := make([]resource.LibraryItem, 0, query.Limit+1)
	if primaryCustom {
		cursor, err := resource.DecodeLibraryCursor(query)
		if err != nil {
			return resource.LibraryItemPage{}, err
		}
		if query.Cursor == "" || len(cursor.Values) == 0 || cursor.Values[0] != nil {
			present := true
			loaded, err := r.queryLibraryItemBranch(ctx, query, &present, query.Limit+1)
			if err != nil {
				return resource.LibraryItemPage{}, err
			}
			items = append(items, loaded...)
		}
		if len(items) <= query.Limit {
			missing := false
			loaded, err := r.queryLibraryItemBranch(ctx, query, &missing, query.Limit+1-len(items))
			if err != nil {
				return resource.LibraryItemPage{}, err
			}
			items = append(items, loaded...)
		}
	} else {
		loaded, err := r.queryLibraryItemBranch(ctx, query, nil, query.Limit+1)
		if err != nil {
			return resource.LibraryItemPage{}, err
		}
		items = loaded
	}

	page := resource.LibraryItemPage{}
	hasMore := len(items) > query.Limit
	if hasMore {
		items = items[:query.Limit]
	}
	if err := r.loadLibraryItemsFields(ctx, r.connector.Pool(), items); err != nil {
		return resource.LibraryItemPage{}, err
	}
	if err := r.loadLibraryItemVersions(ctx, r.connector.Pool(), items); err != nil {
		return resource.LibraryItemPage{}, err
	}
	if hasMore {
		var err error
		page.NextCursor, err = resource.EncodeLibraryCursor(query, items[len(items)-1])
		if err != nil {
			return resource.LibraryItemPage{}, err
		}
	}
	page.Items = items
	return page, nil
}

// queryLibraryItemBranch executes either the ordinary query path or one half
// of a primary custom-field ordering. customValues=true reads present scalar
// values in typed-index order; false reads the NULL/missing tail.
func (r *Repository) queryLibraryItemBranch(ctx context.Context, query resource.LibraryItemQuery, customValues *bool, limitValue int) ([]resource.LibraryItem, error) {
	args := make([]any, 0, 16)
	add := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	sorts, idDirection := resource.LibraryItemSorts(query)
	primaryCustom := customValues != nil && len(sorts) > 0 && resource.IsCustomFieldPath(sorts[0].Field)

	from := "core.library_items item JOIN core.resources library ON library.id=item.library_id AND library.site_id=item.site_id"
	joins := make([]string, 0, len(sorts))
	expressions := make([]libraryItemSortExpression, 0, len(sorts))
	sortAliases := make(map[resource.FieldPath]libraryItemSortAlias, len(sorts))
	order := make([]string, 0, len(sorts)+1)
	for index, item := range sorts {
		column, custom := libraryItemQueryColumn(item.Field)
		if custom {
			valueColumn, err := fieldValueColumn(item.Kind)
			if err != nil {
				return nil, err
			}
			kindLiteral, err := fieldStorageKindSQL(item.Kind)
			if err != nil {
				return nil, err
			}
			alias := "sort_value_" + strconv.Itoa(index)
			key := strings.TrimPrefix(string(item.Field), "resource.field.")
			if primaryCustom && index == 0 && *customValues {
				from = "core.resource_field_values " + alias +
					" JOIN core.library_item_routes route ON route.resource_id=" + alias + ".resource_id AND route.site_id=" + alias + ".site_id AND route.library_id=" + alias + ".library_id" +
					" JOIN core.library_items item ON item.id=route.resource_id AND item.site_id=route.site_id AND item.library_id=route.library_id" +
					" JOIN core.resources library ON library.id=item.library_id AND library.site_id=item.site_id"
			} else {
				fieldKey := add(key)
				joins = append(joins, "LEFT JOIN core.resource_field_values "+alias+
					" ON "+alias+".resource_id=item.id AND "+alias+".site_id=item.site_id"+
					" AND "+alias+".library_id=item.library_id"+
					" AND "+alias+".field_key="+fieldKey+" AND "+alias+".value_kind="+kindLiteral+
					" AND "+alias+".position=0 AND NOT "+alias+".is_multi")
			}
			column = alias + "." + valueColumn
			if _, exists := sortAliases[item.Field]; !exists {
				sortAliases[item.Field] = libraryItemSortAlias{name: alias, valueColumn: valueColumn}
			}
		}
		expressions = append(expressions, libraryItemSortExpression{sort: item, sql: column})
		order = append(order, column+" "+sortDirectionSQL(item.Direction)+" NULLS LAST")
	}
	order = append(order, "item.id "+sortDirectionSQL(idDirection))

	where := []string{
		"item.site_id=" + add(query.SiteID),
		"item.library_id=" + add(query.LibraryID),
		"library.type='library'",
		"library.deleted_at IS NULL",
	}
	if search := strings.TrimSpace(query.Search); search != "" {
		if id, parseErr := strconv.ParseInt(search, 10, 64); parseErr == nil && id > 0 {
			where = append(where, "item.id="+add(resource.ID(id)))
		} else {
			pattern := add("%" + strings.ToLower(search) + "%")
			where = append(where, "(lower(item.title) LIKE "+pattern+" OR lower(item.slug) LIKE "+pattern+")")
		}
	}
	if query.PublicOnly {
		where = append(where, "item.deleted_at IS NULL", "item.is_public", "(item.published_at IS NULL OR item.published_at<=now())", "(item.unpublished_at IS NULL OR now()<item.unpublished_at)")
	}
	if query.Deleted != nil {
		if *query.Deleted {
			where = append(where, "item.deleted_at IS NOT NULL")
		} else {
			where = append(where, "item.deleted_at IS NULL")
		}
	}
	for _, condition := range query.Filters {
		var fragment string
		var err error
		if alias, exists := sortAliases[condition.Field]; exists {
			fragment, err = libraryItemSortAliasFilter(condition, alias, add)
		} else {
			fragment, err = resourceQueryFilterFor(condition, add, "item", libraryItemQueryColumn)
		}
		if err != nil {
			return nil, err
		}
		where = append(where, fragment)
		if libraryItemPartitionFilter(condition) {
			partitionFragment, err := resourceQueryFilterFor(condition, add, "item", libraryItemPartitionQueryColumn)
			if err != nil {
				return nil, err
			}
			where = append(where, partitionFragment)
		}
	}
	if primaryCustom {
		alias := "sort_value_0"
		if *customValues {
			kindLiteral, err := fieldStorageKindSQL(sorts[0].Kind)
			if err != nil {
				return nil, err
			}
			where = append(where,
				alias+".site_id=item.site_id",
				alias+".library_id="+add(query.LibraryID),
				alias+".field_key="+add(strings.TrimPrefix(string(sorts[0].Field), "resource.field.")),
				alias+".value_kind="+kindLiteral,
				alias+".position=0", "NOT "+alias+".is_multi",
				"route.library_id=item.library_id",
			)
		} else {
			where = append(where, alias+".resource_id IS NULL")
		}
	}
	if query.Cursor != "" {
		cursor, err := resource.DecodeLibraryCursor(query)
		if err != nil {
			return nil, err
		}
		where = append(where, libraryItemKeysetPredicate(
			expressions,
			cursor,
			idDirection,
			primaryCustom && *customValues,
			add,
		))
	}
	limit := add(limitValue)
	rows, err := r.connector.Pool().Query(ctx, `SELECT `+libraryItemColumns+` FROM `+from+` `+strings.Join(joins, " ")+` WHERE `+strings.Join(where, " AND ")+` ORDER BY `+strings.Join(order, ", ")+` LIMIT `+limit+`;`, args...)
	if err != nil {
		return nil, fmt.Errorf("query library items: %w", err)
	}
	defer rows.Close()
	items := make([]resource.LibraryItem, 0, limitValue)
	for rows.Next() {
		item, err := scanLibraryItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *Repository) LibraryItemTemplateCodes(ctx context.Context, siteID site.ID, libraryID resource.ID) ([]template.Code, error) {
	rows, err := r.connector.Pool().Query(ctx, `SELECT template FROM core.library_item_template_usage WHERE site_id=$1 AND library_id=$2 ORDER BY template;`, siteID, libraryID)
	if err != nil {
		return nil, translateError(err)
	}
	defer rows.Close()
	result := make([]template.Code, 0, 4)
	for rows.Next() {
		var code template.Code
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		result = append(result, code)
	}
	return result, translateError(rows.Err())
}

func (r *Repository) LibraryItemWidgetCodes(ctx context.Context, siteID site.ID, libraryID resource.ID) ([]widget.Code, error) {
	rows, err := r.connector.Pool().Query(ctx, `
SELECT DISTINCT binding.widget_code
FROM core.library_item_routes route
JOIN core.resource_widgets binding ON binding.resource_id=route.resource_id
WHERE route.site_id=$1 AND route.library_id=$2
ORDER BY binding.widget_code;`, siteID, libraryID)
	if err != nil {
		return nil, translateError(err)
	}
	defer rows.Close()
	result := make([]widget.Code, 0, 4)
	for rows.Next() {
		var code widget.Code
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		result = append(result, code)
	}
	return result, translateError(rows.Err())
}

func ensureLibraryItemTemplateUsage(ctx context.Context, tx pgx.Tx, siteID site.ID, libraryID resource.ID, code *template.Code) error {
	if code == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO core.library_item_template_usage (site_id, library_id, template)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;`, siteID, libraryID, *code); err != nil {
		return translateError(err)
	}
	return nil
}

func pruneLibraryItemTemplateUsage(ctx context.Context, tx pgx.Tx, siteID site.ID, libraryID resource.ID, code *template.Code) error {
	if code == nil {
		return nil
	}
	// Serialize cleanup for one usage tuple. Without this lock, two items can
	// concurrently leave the same template, each observe the other's
	// uncommitted old row, and both incorrectly retain stale metadata.
	if _, err := tx.Exec(ctx, `
SELECT 1
FROM core.library_item_template_usage
WHERE site_id=$1 AND library_id=$2 AND template=$3
FOR UPDATE;`, siteID, libraryID, *code); err != nil {
		return translateError(err)
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM core.library_item_template_usage usage
WHERE usage.site_id=$1 AND usage.library_id=$2 AND usage.template=$3
  AND NOT EXISTS (
      SELECT 1
      FROM core.library_items item
      WHERE item.site_id=$1 AND item.library_id=$2 AND item.template=$3
  );`, siteID, libraryID, *code); err != nil {
		return translateError(err)
	}
	return nil
}

func sameTemplateCode(left, right *template.Code) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type libraryItemSortExpression struct {
	sort resource.Sort
	sql  string
}

type libraryItemSortAlias struct {
	name        string
	valueColumn string
}

func libraryItemSortAliasFilter(condition resource.FilterCondition, alias libraryItemSortAlias, add func(any) string) (string, error) {
	if err := condition.Validate(); err != nil {
		return "", err
	}
	kind, value, err := filterStorageValue(condition.Kind, condition.Value)
	if err != nil {
		return "", err
	}
	column, err := fieldValueColumn(kind)
	if err != nil {
		return "", err
	}
	if column != alias.valueColumn {
		return "", fmt.Errorf("sort alias for field %q has incompatible storage kind %q", condition.Field, kind)
	}

	operator := filterOperatorSQL(condition.Operator)
	negative := condition.Operator == resource.FilterNotEqual || condition.Operator == resource.FilterNotIn
	if condition.Operator == resource.FilterNotEqual {
		operator = filterOperatorSQL(resource.FilterEqual)
	} else if condition.Operator == resource.FilterNotIn {
		operator = filterOperatorSQL(resource.FilterIn)
	}
	placeholder := add(value)
	comparison := alias.name + "." + alias.valueColumn + " " + operator + " " + placeholder
	if condition.Operator == resource.FilterIn || condition.Operator == resource.FilterNotIn {
		comparison = alias.name + "." + alias.valueColumn + " " + operator + " (" + placeholder + ")"
	}
	if negative {
		return "(" + alias.name + ".resource_id IS NULL OR NOT (" + comparison + "))", nil
	}
	return comparison, nil
}

func libraryItemQueryColumn(path resource.FieldPath) (string, bool) {
	switch path {
	case resource.FieldID:
		return "item.id", false
	case resource.FieldTitle:
		return "item.title", false
	case resource.FieldSlug:
		return "item.slug", false
	case resource.FieldAnnotation:
		return "item.annotation", false
	case resource.FieldTemplate:
		return "item.template", false
	case resource.FieldIsPublic:
		return "item.is_public", false
	case resource.FieldIsSearchable:
		return "item.is_searchable", false
	case resource.FieldPublishedAt:
		return "item.published_at", false
	case resource.FieldCreatedAt:
		return "item.created_at", false
	case resource.FieldUpdatedAt:
		return "item.updated_at", false
	default:
		return "", true
	}
}

func libraryItemPartitionQueryColumn(path resource.FieldPath) (string, bool) {
	if path == resource.FieldPublishedAt {
		return "item.partition_at", false
	}
	return libraryItemQueryColumn(path)
}

func libraryItemPartitionFilter(condition resource.FilterCondition) bool {
	if condition.Field != resource.FieldPublishedAt {
		return false
	}
	switch condition.Operator {
	case resource.FilterEqual, resource.FilterIn, resource.FilterGreaterThan,
		resource.FilterGreaterThanOrEqual, resource.FilterLessThan, resource.FilterLessThanOrEqual:
		return true
	default:
		return false
	}
}

func libraryItemKeysetPredicate(expressions []libraryItemSortExpression, cursor resource.LibraryCursor, idDirection resource.SortDirection, primaryCustomPresent bool, add func(any) string) string {
	branches := make([]string, 0, len(expressions)+1)
	prefix := make([]string, 0, len(expressions))
	for index, expression := range expressions {
		value := cursor.Values[index]
		if value != nil {
			operator := ">"
			if expression.sort.Direction == resource.SortDescending {
				operator = "<"
			}
			comparison := expression.sql + operator + add(value)
			if !(primaryCustomPresent && index == 0) {
				comparison = "(" + comparison + " OR " + expression.sql + " IS NULL)"
			}
			branches = append(branches, "("+strings.Join(append(append([]string(nil), prefix...), comparison), " AND ")+")")
		}
		if value == nil {
			prefix = append(prefix, expression.sql+" IS NULL")
		} else {
			prefix = append(prefix, expression.sql+" IS NOT DISTINCT FROM "+add(value))
		}
	}
	idOperator := ">"
	if idDirection == resource.SortDescending {
		idOperator = "<"
	}
	idComparison := "item.id" + idOperator + add(cursor.ID)
	branches = append(branches, "("+strings.Join(append(prefix, idComparison), " AND ")+")")
	return "(" + strings.Join(branches, " OR ") + ")"
}

func sortDirectionSQL(direction resource.SortDirection) string {
	if direction == resource.SortDescending {
		return "DESC"
	}
	return "ASC"
}

func fieldStorageKindSQL(kind field.StorageKind) (string, error) {
	switch kind {
	case field.StorageString, field.StorageInteger, field.StorageFloat, field.StorageBoolean,
		field.StorageTimestamp, field.StorageReference, field.StorageJSON:
		return "'" + string(kind) + "'", nil
	default:
		return "", fmt.Errorf("resource field storage kind %q is invalid", kind)
	}
}

func (r *Repository) ResolveLibraryItemRoute(ctx context.Context, siteID site.ID, path string) (resource.LibraryItem, resource.Resource, error) {
	rows, err := r.connector.Pool().Query(ctx, `SELECT id, site_id, parent_id, type, template, content_type, title, menu_title, slug, path, annotation, content, image_media_id, target_resource_id, external_url, is_public, is_searchable, in_menu, in_sitemap, sort, published_at, unpublished_at, type_settings, created_at, updated_at, created_by, updated_by, deleted_at, deleted_by FROM core.resources WHERE site_id=$1 AND type='library' AND path IS NOT NULL AND (path='/' OR $2=path OR $2 LIKE path||'/%') ORDER BY length(path) DESC, id;`, siteID, path)
	if err != nil {
		return resource.LibraryItem{}, resource.Resource{}, err
	}
	defer rows.Close()
	for rows.Next() {
		library, err := scanResource(rows)
		if err != nil {
			return resource.LibraryItem{}, resource.Resource{}, err
		}
		pattern, _ := library.TypeSettings["item_url_pattern"].(string)
		if pattern == "" {
			pattern = resourcetype.DefaultItemURLPattern
		}
		relative := strings.TrimPrefix(path, *library.Path)
		if *library.Path == "/" {
			relative = path
		}
		key, matched := resource.MatchLibraryItemPattern(pattern, relative)
		if !matched {
			continue
		}
		var item resource.LibraryItem
		if key.ID > 0 {
			item, err = r.libraryItemByID(ctx, r.connector.Pool(), key.ID, false)
		} else {
			item, err = scanLibraryItem(r.connector.Pool().QueryRow(ctx, `SELECT `+libraryItemColumns+` FROM core.library_item_routes route JOIN core.library_items item ON item.id=route.resource_id AND item.library_id=route.library_id WHERE route.library_id=$1 AND route.slug=$2;`, library.ID, key.Slug))
		}
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, resource.ErrNotFound) {
			continue
		}
		if err != nil {
			return resource.LibraryItem{}, resource.Resource{}, err
		}
		if item.LibraryID != library.ID {
			continue
		}
		effectiveURL, urlErr := resource.EffectiveLibraryItemURL(library, item)
		if urlErr != nil {
			return resource.LibraryItem{}, resource.Resource{}, urlErr
		}
		if effectiveURL != path {
			continue
		}
		if err := r.loadLibraryItemFields(ctx, r.connector.Pool(), &item); err != nil {
			return resource.LibraryItem{}, resource.Resource{}, err
		}
		return item, library, nil
	}
	return resource.LibraryItem{}, resource.Resource{}, resource.ErrNotFound
}

func ensureLibraryTarget(ctx context.Context, queryer libraryRowQueryer, siteID site.ID, libraryID resource.ID) error {
	var id resource.ID
	err := queryer.QueryRow(ctx, `SELECT id FROM core.resources WHERE id=$1 AND site_id=$2 AND type='library' AND deleted_at IS NULL FOR SHARE;`, libraryID, siteID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return resource.ErrInvalidReference
	}
	if err != nil {
		return translateError(err)
	}
	return nil
}

func scanLibraryItem(scanner rowScanner) (resource.LibraryItem, error) {
	var item resource.LibraryItem
	var templateCode, contentType *string
	var imageMediaID *int64
	err := scanner.Scan(&item.ID, &item.SiteID, &item.LibraryID, &templateCode, &contentType, &item.Title, &item.Slug, &item.Annotation, &item.Content, &imageMediaID, &item.IsPublic, &item.IsSearchable, &item.PublishedAt, &item.UnpublishedAt, &item.CreatedAt, &item.UpdatedAt, &item.CreatedBy, &item.UpdatedBy, &item.DeletedAt, &item.DeletedBy)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if templateCode != nil {
		code := template.Code(*templateCode)
		item.Template = &code
	}
	item.ContentType = contentType
	if imageMediaID != nil {
		id := media.ID(*imageMediaID)
		item.ImageMediaID = &id
	}
	return item, nil
}

func (r *Repository) loadLibraryItemFields(ctx context.Context, queryer rowQueryer, item *resource.LibraryItem) error {
	items := []resource.LibraryItem{*item}
	if err := r.loadLibraryItemsFields(ctx, queryer, items); err != nil {
		return err
	}
	*item = items[0]
	return nil
}
func (r *Repository) loadLibraryItemsFields(ctx context.Context, queryer rowQueryer, items []resource.LibraryItem) error {
	projected := make([]resource.Resource, len(items))
	for i := range items {
		projected[i] = resource.Resource{ID: items[i].ID}
	}
	if err := loadResourceFields(ctx, queryer, projected); err != nil {
		return err
	}
	if err := loadResourceWidgets(ctx, queryer, projected); err != nil {
		return err
	}
	for i := range items {
		items[i].Fields = projected[i].Fields
		items[i].FieldValues = projected[i].FieldValues
		items[i].Widgets = projected[i].Widgets
	}
	return nil
}

func (r *Repository) loadLibraryItemVersions(ctx context.Context, queryer rowQueryer, items []resource.LibraryItem) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, len(items))
	byID := make(map[resource.ID]int, len(items))
	for index := range items {
		ids[index] = int64(items[index].ID)
		byID[items[index].ID] = index
	}
	rows, err := queryer.Query(ctx, `SELECT id, version FROM core.resource_entities WHERE id = ANY($1::bigint[]);`, ids)
	if err != nil {
		return fmt.Errorf("query library item versions: %w", err)
	}
	defer rows.Close()
	loaded := 0
	for rows.Next() {
		var id resource.ID
		var version int64
		if err := rows.Scan(&id, &version); err != nil {
			return fmt.Errorf("scan library item version: %w", err)
		}
		index, exists := byID[id]
		if !exists {
			return fmt.Errorf("query library item versions returned unexpected resource %d", id)
		}
		items[index].Version = version
		loaded++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("query library item versions: %w", err)
	}
	if loaded != len(items) {
		return fmt.Errorf("query library item versions returned %d of %d resources", loaded, len(items))
	}
	return nil
}

var _ resource.LibraryItemRepository = (*Repository)(nil)
