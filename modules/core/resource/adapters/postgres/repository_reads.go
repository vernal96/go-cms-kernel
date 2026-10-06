package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
)

func (r *Repository) ByID(
	ctx context.Context,
	id resource.ID,
) (resource.Resource, error) {
	if ctx == nil {
		return resource.Resource{}, errors.New(
			"get resource context is nil",
		)
	}

	tx, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return resource.Resource{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := scanResource(tx.QueryRow(ctx, `
SELECT
    id, site_id, parent_id, type, template, content_type,
	    title, menu_title, slug, path, annotation, content, image_media_id,
    target_resource_id,
    external_url, is_public, is_searchable, in_menu, in_sitemap,
    sort, published_at, unpublished_at, type_settings, created_at,
	    updated_at, created_by, updated_by, deleted_at, deleted_by
FROM core.resources
WHERE id = $1;
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return resource.Resource{}, resource.ErrNotFound
	}
	if err != nil {
		return resource.Resource{}, fmt.Errorf(
			"query core resource %d: %w",
			id,
			err,
		)
	}
	items := []resource.Resource{result}
	if err := loadResourceWidgets(
		ctx,
		tx,
		items,
	); err != nil {
		return resource.Resource{}, err
	}
	if err := loadResourceFields(ctx, tx, items); err != nil {
		return resource.Resource{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT version FROM core.resource_entities WHERE id=$1;`, id).Scan(&items[0].Version); err != nil {
		return resource.Resource{}, translateError(err)
	}

	return items[0], nil
}

func (r *Repository) ByPath(
	ctx context.Context,
	siteID site.ID,
	path string,
) (resource.Resource, error) {
	if ctx == nil {
		return resource.Resource{}, errors.New(
			"get resource by path context is nil",
		)
	}

	result, err := scanResource(r.connector.Pool().QueryRow(ctx, `
SELECT
    id, site_id, parent_id, type, template, content_type,
	    title, menu_title, slug, path, annotation, content, image_media_id,
    target_resource_id,
    external_url, is_public, is_searchable, in_menu, in_sitemap,
    sort, published_at, unpublished_at, type_settings, created_at,
	    updated_at, created_by, updated_by, deleted_at, deleted_by
FROM core.resources
WHERE site_id = $1
  AND path = $2;
`, siteID, path))
	if errors.Is(err, pgx.ErrNoRows) {
		return resource.Resource{}, resource.ErrNotFound
	}
	if err != nil {
		return resource.Resource{}, fmt.Errorf(
			"query core resource by path %q: %w",
			path,
			err,
		)
	}
	items := []resource.Resource{result}
	if err := loadResourceWidgets(
		ctx,
		r.connector.Pool(),
		items,
	); err != nil {
		return resource.Resource{}, err
	}
	if err := loadResourceFields(ctx, r.connector.Pool(), items); err != nil {
		return resource.Resource{}, err
	}

	return items[0], nil
}

func (r *Repository) ListBySite(
	ctx context.Context,
	siteID site.ID,
) ([]resource.Resource, error) {
	if ctx == nil {
		return nil, errors.New("list resources context is nil")
	}

	rows, err := r.connector.Pool().Query(ctx, `
SELECT
    id, site_id, parent_id, type, template, content_type,
	    title, menu_title, slug, path, annotation, content, image_media_id,
    target_resource_id,
    external_url, is_public, is_searchable, in_menu, in_sitemap,
    sort, published_at, unpublished_at, type_settings, created_at,
	    updated_at, created_by, updated_by, deleted_at, deleted_by
FROM core.resources
WHERE site_id = $1
ORDER BY parent_id NULLS FIRST, sort, id;
`, siteID)
	if err != nil {
		return nil, fmt.Errorf("query core resources: %w", err)
	}
	defer rows.Close()

	result := make([]resource.Resource, 0)
	for rows.Next() {
		item, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate core resources: %w", err)
	}
	rows.Close()
	if err := loadResourceWidgets(
		ctx,
		r.connector.Pool(),
		result,
	); err != nil {
		return nil, err
	}
	if err := loadResourceFields(ctx, r.connector.Pool(), result); err != nil {
		return nil, err
	}

	return result, nil
}

func (r *Repository) ListChildren(
	ctx context.Context,
	siteID site.ID,
	parentID *resource.ID,
) ([]resource.Child, error) {
	if ctx == nil {
		return nil, errors.New("list resource children context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `
WITH valid_parent AS (
    SELECT true AS ok
    WHERE $2::bigint IS NULL OR EXISTS (
        SELECT 1
        FROM core.resources parent
        WHERE parent.site_id = $1
          AND parent.id = $2
    )
), children AS (
SELECT
    current.id,
	entity.version,
    current.site_id,
    current.parent_id,
    current.type,
    current.template,
    current.title,
	current.menu_title,
	current.is_public,
	current.in_menu,
	current.published_at,
	current.unpublished_at,
	current.deleted_at,
	(current.deleted_at IS NULL AND current.path IS DISTINCT FROM '/') AS can_transfer_site,
    EXISTS (
        SELECT 1
        FROM core.resources child
        WHERE child.site_id = current.site_id
          AND child.parent_id = current.id
    ) AS has_children,
    current.sort
FROM core.resources current
JOIN core.resource_entities entity ON entity.id = current.id,
valid_parent
WHERE current.site_id = $1
  AND current.parent_id IS NOT DISTINCT FROM $2::bigint
)
SELECT
    EXISTS (SELECT 1 FROM valid_parent) AS parent_exists,
    children.id,
	children.version,
    children.site_id,
    children.parent_id,
    children.type,
    children.template,
    children.title,
	children.menu_title,
	children.is_public,
	children.in_menu,
	children.published_at,
	children.unpublished_at,
	children.deleted_at,
	children.can_transfer_site,
	children.sort,
    children.has_children
FROM (SELECT true) marker
LEFT JOIN children ON true
ORDER BY children.sort, children.id;`, siteID, parentID)
	if err != nil {
		return nil, fmt.Errorf("query resource children: %w", err)
	}
	defer rows.Close()

	items := make([]resource.Child, 0)
	for rows.Next() {
		var (
			parentExists     bool
			rawID            *int64
			rawVersion       *int64
			rawSiteID        *int64
			rawParent        *int64
			rawType          *string
			rawTemplate      *string
			rawTitle         *string
			rawMenuTitle     *string
			rawIsPublic      *bool
			rawInMenu        *bool
			rawPublishedAt   *time.Time
			rawUnpublishedAt *time.Time
			rawDeletedAt     *time.Time
			rawCanTransfer   *bool
			rawSort          *int
			rawHasChildren   *bool
		)
		if err := rows.Scan(
			&parentExists,
			&rawID,
			&rawVersion,
			&rawSiteID,
			&rawParent,
			&rawType,
			&rawTemplate,
			&rawTitle,
			&rawMenuTitle,
			&rawIsPublic,
			&rawInMenu,
			&rawPublishedAt,
			&rawUnpublishedAt,
			&rawDeletedAt,
			&rawCanTransfer,
			&rawSort,
			&rawHasChildren,
		); err != nil {
			return nil, fmt.Errorf("scan resource child: %w", err)
		}
		if !parentExists {
			return nil, resource.ErrNotFound
		}
		if rawID == nil {
			continue
		}
		item := resource.Child{
			ID:              resource.ID(*rawID),
			Version:         *rawVersion,
			SiteID:          site.ID(*rawSiteID),
			Type:            resourcetype.Code(*rawType),
			Title:           *rawTitle,
			MenuTitle:       *rawMenuTitle,
			Sort:            *rawSort,
			IsPublic:        rawIsPublic != nil && *rawIsPublic,
			InMenu:          rawInMenu != nil && *rawInMenu,
			PublishedAt:     rawPublishedAt,
			UnpublishedAt:   rawUnpublishedAt,
			DeletedAt:       rawDeletedAt,
			HasChildren:     *rawHasChildren,
			CanTransferSite: rawCanTransfer != nil && *rawCanTransfer,
		}
		if rawParent != nil {
			value := resource.ID(*rawParent)
			item.ParentID = &value
		}
		if rawTemplate != nil {
			value := template.Code(*rawTemplate)
			item.Template = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resource children: %w", err)
	}
	return items, nil
}

func (r *Repository) Statistics(
	ctx context.Context,
	query resource.StatisticsQuery,
) (resource.Statistics, error) {
	if ctx == nil {
		return resource.Statistics{}, errors.New("resource statistics context is nil")
	}
	allowed := make([]int64, len(query.Scope.SiteIDs))
	for index, id := range query.Scope.SiteIDs {
		allowed[index] = int64(id)
	}
	breakdown := make([]int64, len(query.SiteIDs))
	for index, id := range query.SiteIDs {
		breakdown[index] = int64(id)
	}

	result := resource.Statistics{BySite: make(map[site.ID]int, len(breakdown))}
	if err := r.connector.Pool().QueryRow(ctx, `
SELECT count(*)
FROM core.resources
WHERE deleted_at IS NULL
  AND ($1 OR site_id = ANY($2::bigint[]));`, query.Scope.All, allowed).Scan(&result.Total); err != nil {
		return resource.Statistics{}, fmt.Errorf("count core resource statistics: %w", err)
	}
	if len(breakdown) == 0 {
		return result, nil
	}

	rows, err := r.connector.Pool().Query(ctx, `
SELECT site_id, count(*)
FROM core.resources
WHERE site_id = ANY($1::bigint[])
  AND deleted_at IS NULL
  AND ($2 OR site_id = ANY($3::bigint[]))
GROUP BY site_id;`, breakdown, query.Scope.All, allowed)
	if err != nil {
		return resource.Statistics{}, fmt.Errorf("query core resource statistics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var siteID site.ID
		var count int
		if err := rows.Scan(&siteID, &count); err != nil {
			return resource.Statistics{}, fmt.Errorf("scan core resource statistics: %w", err)
		}
		result.BySite[siteID] = count
	}
	if err := rows.Err(); err != nil {
		return resource.Statistics{}, fmt.Errorf("iterate core resource statistics: %w", err)
	}
	return result, nil
}

func (r *Repository) ExistsInSite(
	ctx context.Context,
	siteID site.ID,
	id resource.ID,
) (bool, error) {
	if ctx == nil {
		return false, errors.New("check site resource context is nil")
	}
	var exists bool
	if err := r.connector.Pool().QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM core.resources WHERE site_id = $1 AND id = $2
);`, siteID, id).Scan(&exists); err != nil {
		return false, fmt.Errorf("check site resource: %w", err)
	}
	return exists, nil
}
