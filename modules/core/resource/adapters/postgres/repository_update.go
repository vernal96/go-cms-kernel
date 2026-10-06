package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) Update(
	ctx context.Context,
	actorID *security.UserID,
	current resource.Resource,
	item resource.Resource,
	validate resource.ValidateImageMedia,
) (_ resource.Resource, resultErr error) {
	if ctx == nil {
		return resource.Resource{}, errors.New(
			"update resource context is nil",
		)
	}

	transaction, err := r.connector.Pool().BeginTx(
		ctx,
		pgx.TxOptions{},
	)
	if err != nil {
		return resource.Resource{}, fmt.Errorf(
			"begin resource update: %w",
			err,
		)
	}
	defer func() {
		if resultErr == nil {
			return
		}

		rollbackErr := transaction.Rollback(context.Background())
		if rollbackErr != nil &&
			!errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()

	if current.ID != item.ID {
		return resource.Resource{}, resource.ErrInvalidReference
	}
	if current.Version <= 0 {
		return resource.Resource{}, resource.ErrConflict
	}
	if err := lockRouteNamespace(ctx, transaction, item.SiteID); err != nil {
		return resource.Resource{}, err
	}
	if _, err := transaction.Exec(ctx, `LOCK TABLE core.resources IN SHARE ROW EXCLUSIVE MODE;`); err != nil {
		return resource.Resource{}, fmt.Errorf("lock resources for update: %w", err)
	}

	lockedForHooks, err := r.treeInTransaction(ctx, transaction, current.ID)
	if err != nil {
		return resource.Resource{}, err
	}
	if lockedForHooks.Version != current.Version {
		return resource.Resource{}, resource.ErrConflict
	}
	item, err = resource.PrepareResourceMutation(ctx, &lockedForHooks, item)
	if err != nil {
		return resource.Resource{}, err
	}
	hookRelated, err := r.relatedChangeStates(ctx, transaction, lockedForHooks, item)
	if err != nil {
		return resource.Resource{}, err
	}
	current = lockedForHooks
	mediaIDs := make([]media.ID, 0, 2)
	if current.ImageMediaID != nil {
		mediaIDs = append(mediaIDs, *current.ImageMediaID)
	}
	if item.ImageMediaID != nil {
		mediaIDs = append(mediaIDs, *item.ImageMediaID)
	}
	if err := medialock.Lock(ctx, transaction, mediaIDs...); err != nil {
		return resource.Resource{}, err
	}

	var (
		currentSiteID       site.ID
		currentImageMediaID *int64
		currentParentID     *int64
		currentSort         int
		currentDeletedAt    *time.Time
	)
	if err := transaction.QueryRow(ctx, `
SELECT site_id, image_media_id, parent_id, sort, deleted_at
FROM core.resources
WHERE id = $1
FOR UPDATE;
`, item.ID).Scan(
		&currentSiteID,
		&currentImageMediaID,
		&currentParentID,
		&currentSort,
		&currentDeletedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		return resource.Resource{}, resource.ErrNotFound
	} else if err != nil {
		return resource.Resource{}, fmt.Errorf(
			"lock resource %d: %w",
			item.ID,
			err,
		)
	}
	if currentSiteID != item.SiteID {
		return resource.Resource{}, resource.ErrInvalidReference
	}
	if err := transaction.QueryRow(ctx, `
UPDATE core.resource_entities SET version=version+1
WHERE id=$1 AND version=$2
RETURNING version;`, item.ID, current.Version).Scan(&item.Version); errors.Is(err, pgx.ErrNoRows) {
		return resource.Resource{}, resource.ErrConflict
	} else if err != nil {
		return resource.Resource{}, translateError(err)
	}
	if !equalMediaID(current.ImageMediaID, currentImageMediaID) {
		return resource.Resource{}, resource.ErrConflict
	}
	lockedParentID := resourceIDFromInt64(currentParentID)
	if currentDeletedAt != nil && (!sameResourceID(lockedParentID, item.ParentID) || currentSort != item.Sort) {
		return resource.Resource{}, resource.ErrInvalidTree
	}

	if item.ImageMediaID != nil {
		if validate == nil {
			return resource.Resource{}, errors.New(
				"resource image media validator is nil",
			)
		}
		if err := ensureMediaAvailable(
			ctx,
			transaction,
			*item.ImageMediaID,
			item.ID,
		); err != nil {
			return resource.Resource{}, err
		}
		if err := validate(ctx, *item.ImageMediaID); err != nil {
			return resource.Resource{}, err
		}
	}

	item, err = r.prepareResourceUpdateTree(ctx, transaction, current, item, lockedParentID, currentDeletedAt)
	if err != nil {
		return resource.Resource{}, err
	}

	updated, err := r.updateResourceRow(ctx, transaction, actorID, item)
	if err != nil {
		return resource.Resource{}, err
	}
	if err := updateResourceDescendantPaths(ctx, transaction, item.ID, actorID); err != nil {
		return resource.Resource{}, err
	}

	updated.Widgets = widget.CloneBindings(item.Widgets)
	if err := replaceFileReferences(ctx, transaction, updated.ID, item.FileReferences); err != nil {
		return resource.Resource{}, err
	}
	if err := replaceResourceFields(ctx, transaction, updated.ID, updated.SiteID, nil, item.FieldValues); err != nil {
		return resource.Resource{}, err
	}
	updated.Fields = cloneFieldMap(item.Fields)
	updated.FieldValues = append([]field.StoredValue(nil), item.FieldValues...)
	updated.FileReferences = cloneFileReferences(item.FileReferences)
	updated.Version = item.Version
	if err := r.appendRevision(ctx, transaction, updated, resource.RevisionUpdated, nil, actorID); err != nil {
		return resource.Resource{}, err
	}
	if err := r.finishRelated(ctx, transaction, hookRelated, actorID); err != nil {
		return resource.Resource{}, err
	}
	if err := r.appendResourceEvent(ctx, transaction, resource.EventUpdated, updated.ID, updated.SiteID, resource.StorageTree, updated.Version, actorID); err != nil {
		return resource.Resource{}, err
	}

	if !sameMediaID(current.ImageMediaID, item.ImageMediaID) &&
		current.ImageMediaID != nil {
		if _, err := transaction.Exec(ctx, `
DELETE FROM core.media
WHERE id = $1;
`, *current.ImageMediaID); err != nil {
			return resource.Resource{}, translateError(err)
		}
	}

	if err := transaction.Commit(ctx); err != nil {
		return resource.Resource{}, translateError(err)
	}
	return updated, nil
}

func (r *Repository) updateResourceRow(
	ctx context.Context,
	transaction pgx.Tx,
	actorID *security.UserID,
	item resource.Resource,
) (resource.Resource, error) {
	if item.TypeSettings == nil {
		item.TypeSettings = map[string]any{}
	}
	rawSettings, err := json.Marshal(item.TypeSettings)
	if err != nil {
		return resource.Resource{}, fmt.Errorf(
			"encode resource type_settings: %w",
			err,
		)
	}

	updated, err := scanResource(transaction.QueryRow(ctx, `
UPDATE core.resources
SET
    parent_id = $2,
    type = $3,
    template = $4,
    content_type = $5,
    title = $6,
    menu_title = $7,
	    slug = $8,
	    path = $9,
	    annotation = $10,
	    content = $11,
	    image_media_id = $12,
	    target_resource_id = $13,
	    external_url = $14,
	    is_public = $15,
	    is_searchable = $16,
	    in_menu = $17,
	    in_sitemap = $18,
	    sort = $19,
	    published_at = $20,
	    unpublished_at = $21,
	    type_settings = $22::jsonb,
	    updated_at = now(),
	    updated_by = $23
WHERE id = $1
RETURNING
    id, site_id, parent_id, type, template, content_type,
	    title, menu_title, slug, path, annotation, content, image_media_id,
    target_resource_id,
    external_url, is_public, is_searchable, in_menu, in_sitemap,
    sort, published_at, unpublished_at, type_settings, created_at,
	    updated_at, created_by, updated_by, deleted_at, deleted_by;
`,
		item.ID,
		item.ParentID,
		item.Type,
		item.Template,
		item.ContentType,
		item.Title,
		item.MenuTitle,
		item.Slug,
		item.Path,
		item.Annotation,
		item.Content,
		item.ImageMediaID,
		item.TargetResourceID,
		item.ExternalURL,
		item.IsPublic,
		item.IsSearchable,
		item.InMenu,
		item.InSitemap,
		item.Sort,
		item.PublishedAt,
		item.UnpublishedAt,
		string(rawSettings),
		actorID,
	))
	if err != nil {
		return resource.Resource{}, translateError(err)
	}

	return updated, nil
}

func updateResourceDescendantPaths(ctx context.Context, transaction pgx.Tx, id resource.ID, actorID *security.UserID) error {
	if _, err := transaction.Exec(ctx, `
WITH RECURSIVE tree AS
(
    SELECT id, path
    FROM core.resources
    WHERE id = $1

    UNION ALL

    SELECT
        child.id,
        CASE
            WHEN child.path IS NULL THEN NULL
            WHEN tree.path IS NULL THEN NULL
            WHEN tree.path = '/' THEN '/' || child.slug
            ELSE tree.path || '/' || child.slug
        END
    FROM core.resources AS child
    JOIN tree
      ON child.parent_id = tree.id
)
UPDATE core.resources AS item
SET
    path = tree.path,
    updated_at = now(),
    updated_by = $2
FROM tree
WHERE item.id = tree.id
  AND item.id <> $1;
`, id, actorID); err != nil {
		return translateError(err)
	}

	return nil
}

func (r *Repository) prepareResourceUpdateTree(
	ctx context.Context,
	transaction pgx.Tx,
	current resource.Resource,
	item resource.Resource,
	lockedParentID *resource.ID,
	currentDeletedAt *time.Time,
) (resource.Resource, error) {
	var err error
	var parent *resource.Resource
	if item.ParentID != nil {
		parentItem, err := lockResource(
			ctx,
			transaction,
			*item.ParentID,
		)
		if err != nil {
			return resource.Resource{}, err
		}
		if parentItem.SiteID != item.SiteID {
			return resource.Resource{}, resource.ErrInvalidReference
		}
		if parentItem.DeletedAt != nil && currentDeletedAt == nil {
			return resource.Resource{}, resource.ErrInvalidTree
		}
		parent = &parentItem

		var cycle bool
		if err := transaction.QueryRow(ctx, `
WITH RECURSIVE ancestors AS
(
    SELECT id, parent_id
    FROM core.resources
    WHERE id = $1

    UNION ALL

    SELECT resource.id, resource.parent_id
    FROM core.resources AS resource
    JOIN ancestors
      ON resource.id = ancestors.parent_id
)
SELECT EXISTS
(
    SELECT 1
    FROM ancestors
    WHERE id = $2
);
`, *item.ParentID, item.ID).Scan(&cycle); err != nil {
			return resource.Resource{}, fmt.Errorf(
				"check resource parent cycle: %w",
				err,
			)
		}
		if cycle {
			return resource.Resource{}, resource.ErrInvalidTree
		}
	}

	if item.Path != nil {
		item.Path, err = resource.BuildPath(parent, item.Slug)
		if err != nil {
			return resource.Resource{}, err
		}
	}
	paths, err := prospectiveTreePaths(ctx, transaction, item.ID, item.Path)
	if err != nil {
		return resource.Resource{}, err
	}
	if err := ensureTreePathsAvailable(ctx, transaction, item.SiteID, paths, &item); err != nil {
		return resource.Resource{}, err
	}
	if item.Type == resourcetype.Library && (!sameOptionalText(current.Path, item.Path) || !reflect.DeepEqual(current.TypeSettings, item.TypeSettings)) {
		if err := ensureProspectiveLibraryNamespaceAvailable(ctx, transaction, item); err != nil {
			return resource.Resource{}, err
		}
	}
	item.Sort, err = reorderSiblings(
		ctx, transaction, item.SiteID, item.ID, lockedParentID, item.ParentID, item.Sort,
	)
	if err != nil {
		return resource.Resource{}, err
	}

	return item, nil
}
