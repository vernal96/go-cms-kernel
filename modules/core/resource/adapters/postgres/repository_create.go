package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/security"
)

func (r *Repository) Create(
	ctx context.Context,
	actorID *security.UserID,
	item resource.Resource,
	validate resource.ValidateImageMedia,
) (_ resource.Resource, resultErr error) {
	if ctx == nil {
		return resource.Resource{}, errors.New(
			"create resource context is nil",
		)
	}

	transaction, err := r.connector.Pool().BeginTx(
		ctx,
		pgx.TxOptions{},
	)
	if err != nil {
		return resource.Resource{}, fmt.Errorf(
			"begin resource create: %w",
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
	if err := lockRouteNamespace(ctx, transaction, item.SiteID); err != nil {
		return resource.Resource{}, err
	}
	if _, err := transaction.Exec(ctx, `LOCK TABLE core.resources IN SHARE ROW EXCLUSIVE MODE;`); err != nil {
		return resource.Resource{}, fmt.Errorf("lock resources for create: %w", err)
	}
	if err := transaction.QueryRow(ctx, `
SELECT COALESCE(max(sort) + 1, 0)
FROM core.resources
WHERE site_id = $1
  AND parent_id IS NOT DISTINCT FROM $2::bigint;`, item.SiteID, item.ParentID).Scan(&item.Sort); err != nil {
		return resource.Resource{}, fmt.Errorf("resolve resource create position: %w", err)
	}

	item, err = resource.PrepareResourceMutation(ctx, nil, item)
	if err != nil {
		return resource.Resource{}, err
	}
	// A before-create hook may choose another parent; ordering belongs to that
	// final sibling list and is allocated by the adapter under the tree lock.
	if err := transaction.QueryRow(ctx, `SELECT COALESCE(max(sort)+1,0) FROM core.resources WHERE site_id=$1 AND parent_id IS NOT DISTINCT FROM $2::bigint`, item.SiteID, item.ParentID).Scan(&item.Sort); err != nil {
		return resource.Resource{}, translateError(err)
	}
	if item.ImageMediaID != nil {
		if validate == nil {
			return resource.Resource{}, errors.New(
				"resource image media validator is nil",
			)
		}
		if err := medialock.Lock(
			ctx,
			transaction,
			*item.ImageMediaID,
		); err != nil {
			return resource.Resource{}, err
		}
		if err := ensureMediaAvailable(
			ctx,
			transaction,
			*item.ImageMediaID,
			0,
		); err != nil {
			return resource.Resource{}, err
		}
		if err := validate(ctx, *item.ImageMediaID); err != nil {
			return resource.Resource{}, err
		}
	}

	if item.TypeSettings == nil {
		item.TypeSettings = map[string]any{}
	}
	if item.Path != nil {
		if err := ensureTreePathsAvailable(ctx, transaction, item.SiteID, []string{*item.Path}, &item); err != nil {
			return resource.Resource{}, err
		}
	}
	rawSettings, err := json.Marshal(item.TypeSettings)
	if err != nil {
		return resource.Resource{}, fmt.Errorf(
			"encode resource type_settings: %w",
			err,
		)
	}

	result, err := scanResource(transaction.QueryRow(ctx, `
WITH entity AS (
    INSERT INTO core.resource_entities (site_id, storage_kind)
    VALUES ($1, 'tree')
    RETURNING id
)
INSERT INTO core.resources
(
    id,
    site_id,
    parent_id,
    type,
    template,
    content_type,
    title,
    menu_title,
	    slug,
	    path,
	    annotation,
	    content,
    image_media_id,
    target_resource_id,
    external_url,
    is_public,
    is_searchable,
    in_menu,
    in_sitemap,
    sort,
    published_at,
    unpublished_at,
    type_settings,
    created_by,
    updated_by
)
VALUES
(
	    (SELECT id FROM entity),
	    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
	    $11, $12, $13, $14, $15, $16, $17, $18, $19,
	    $20, $21, $22::jsonb, $23, $23
)
RETURNING
    id, site_id, parent_id, type, template, content_type,
	    title, menu_title, slug, path, annotation, content, image_media_id,
    target_resource_id,
    external_url, is_public, is_searchable, in_menu, in_sitemap,
    sort, published_at, unpublished_at, type_settings, created_at,
	    updated_at, created_by, updated_by, deleted_at, deleted_by;
`,
		item.SiteID,
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
	if err := replaceFileReferences(ctx, transaction, result.ID, item.FileReferences); err != nil {
		return resource.Resource{}, err
	}
	if err := replaceResourceFields(ctx, transaction, result.ID, result.SiteID, nil, item.FieldValues); err != nil {
		return resource.Resource{}, err
	}
	result.Fields = cloneFieldMap(item.Fields)
	result.FieldValues = append([]field.StoredValue(nil), item.FieldValues...)
	result.FileReferences = cloneFileReferences(item.FileReferences)
	result.Version = 1
	if err := r.appendRevision(ctx, transaction, result, resource.RevisionCreated, nil, actorID); err != nil {
		return resource.Resource{}, err
	}
	if err := r.appendResourceEvent(ctx, transaction, resource.EventCreated, result.ID, result.SiteID, resource.StorageTree, result.Version, actorID); err != nil {
		return resource.Resource{}, err
	}

	if err := validateMirrorNamespaces(ctx, transaction, item.Type == resourcetype.LibraryMirror); err != nil {
		return resource.Resource{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return resource.Resource{}, translateError(err)
	}
	return result, nil
}
