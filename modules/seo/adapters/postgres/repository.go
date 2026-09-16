package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/seo"
)

type Repository struct {
	connector *connectorpostgres.Connector
}

func NewRepository(connector *connectorpostgres.Connector) (*Repository, error) {
	if connector == nil {
		return nil, errors.New("postgres connector is nil")
	}
	if connector.Pool() == nil {
		return nil, errors.New("postgres pool is nil")
	}
	return &Repository{connector: connector}, nil
}

func (r *Repository) ByResource(
	ctx context.Context,
	siteID site.ID,
	resourceID resource.ID,
) (seo.Metadata, error) {
	if ctx == nil {
		return seo.Metadata{}, errors.New("SEO metadata query context is nil")
	}
	metadata, err := scanMetadata(r.connector.Pool().QueryRow(ctx, `
SELECT
    resource_id, site_id, title_template, description_template,
    keywords_template, canonical_template, robots_index, robots_follow,
    og_title_template, og_description_template,
    created_at, updated_at, created_by, updated_by
FROM seo.resource_metadata
WHERE resource_id = $1 AND site_id = $2;
`, resourceID, siteID))
	if errors.Is(err, pgx.ErrNoRows) {
		return seo.Metadata{}, seo.ErrNotFound
	}
	if err != nil {
		return seo.Metadata{}, fmt.Errorf("query SEO metadata: %w", err)
	}
	return metadata, nil
}

func (r *Repository) UsedByResources(
	ctx context.Context,
	siteID site.ID,
	resourceIDs []resource.ID,
) (bool, error) {
	if ctx == nil {
		return false, errors.New("SEO metadata usage query context is nil")
	}
	ids := make([]int64, len(resourceIDs))
	for index, id := range resourceIDs {
		ids[index] = int64(id)
	}
	var used bool
	if err := r.connector.Pool().QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM seo.resource_metadata
    WHERE site_id=$1 AND resource_id=ANY($2::bigint[])
);`, siteID, ids).Scan(&used); err != nil {
		return false, fmt.Errorf("query SEO metadata usage: %w", err)
	}
	return used, nil
}

func (r *Repository) Save(
	ctx context.Context,
	metadata seo.Metadata,
) (seo.Metadata, error) {
	if ctx == nil {
		return seo.Metadata{}, errors.New("SEO metadata save context is nil")
	}
	stored, err := scanMetadata(r.connector.Pool().QueryRow(ctx, `
INSERT INTO seo.resource_metadata
(
    resource_id, site_id, title_template, description_template,
    keywords_template, canonical_template, robots_index, robots_follow,
    og_title_template, og_description_template, created_by, updated_by
)
SELECT
    resources.id, resources.site_id, $3, $4, $5, $6, $7, $8,
    $9, $10, $11, $12
FROM core.resource_entities AS resources
WHERE resources.id = $1 AND resources.site_id = $2
ON CONFLICT (resource_id) DO UPDATE SET
    title_template = EXCLUDED.title_template,
    description_template = EXCLUDED.description_template,
    keywords_template = EXCLUDED.keywords_template,
    canonical_template = EXCLUDED.canonical_template,
    robots_index = EXCLUDED.robots_index,
    robots_follow = EXCLUDED.robots_follow,
    og_title_template = EXCLUDED.og_title_template,
    og_description_template = EXCLUDED.og_description_template,
    updated_at = now(),
    updated_by = EXCLUDED.updated_by
WHERE seo.resource_metadata.site_id = EXCLUDED.site_id
RETURNING
    resource_id, site_id, title_template, description_template,
    keywords_template, canonical_template, robots_index, robots_follow,
    og_title_template, og_description_template,
    created_at, updated_at, created_by, updated_by;
`,
		metadata.ResourceID,
		metadata.SiteID,
		metadata.TitleTemplate,
		metadata.DescriptionTemplate,
		metadata.KeywordsTemplate,
		metadata.CanonicalTemplate,
		metadata.RobotsIndex,
		metadata.RobotsFollow,
		metadata.OGTitleTemplate,
		metadata.OGDescriptionTemplate,
		metadata.CreatedBy,
		metadata.UpdatedBy,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return seo.Metadata{}, seo.ErrInvalidReference
	}
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) &&
			postgresError.Code == pgerrcode.ForeignKeyViolation {
			return seo.Metadata{}, seo.ErrInvalidReference
		}
		return seo.Metadata{}, fmt.Errorf("save SEO metadata: %w", err)
	}
	return stored, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanMetadata(row rowScanner) (seo.Metadata, error) {
	var metadata seo.Metadata
	err := row.Scan(
		&metadata.ResourceID,
		&metadata.SiteID,
		&metadata.TitleTemplate,
		&metadata.DescriptionTemplate,
		&metadata.KeywordsTemplate,
		&metadata.CanonicalTemplate,
		&metadata.RobotsIndex,
		&metadata.RobotsFollow,
		&metadata.OGTitleTemplate,
		&metadata.OGDescriptionTemplate,
		&metadata.CreatedAt,
		&metadata.UpdatedAt,
		&metadata.CreatedBy,
		&metadata.UpdatedBy,
	)
	return metadata, err
}

var _ seo.Repository = (*Repository)(nil)
