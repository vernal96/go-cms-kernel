package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
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
	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return resource.LibraryItem{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	item, err := r.libraryItemByID(ctx, tx, id, false)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if err := r.loadLibraryItemFields(ctx, tx, &item); err != nil {
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
