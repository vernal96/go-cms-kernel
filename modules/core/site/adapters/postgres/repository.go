package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

const siteColumns = `
    id, profile_code, domain, locale, settings, is_public,
    created_at, updated_at, created_by, updated_by`

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

func (r *Repository) List(ctx context.Context) ([]site.Site, error) {
	if ctx == nil {
		return nil, errors.New("list all sites context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `SELECT `+siteColumns+`
FROM core.sites
ORDER BY id;`)
	if err != nil {
		return nil, fmt.Errorf("query core sites: %w", err)
	}
	return scanSites(rows)
}

func (r *Repository) FindByID(ctx context.Context, id site.ID) (site.Site, error) {
	if ctx == nil {
		return site.Site{}, errors.New("find site context is nil")
	}
	return scanOne(r.connector.Pool().QueryRow(ctx, `SELECT `+siteColumns+`
FROM core.sites WHERE id = $1;`, id))
}

func (r *Repository) FindByDomain(ctx context.Context, domain string) (site.Site, error) {
	if ctx == nil {
		return site.Site{}, errors.New("find site by domain context is nil")
	}
	normalized, err := site.NormalizeDomain(domain)
	if err != nil {
		return site.Site{}, err
	}
	return scanOne(r.connector.Pool().QueryRow(ctx, `SELECT `+siteColumns+`
FROM core.sites WHERE domain = $1;`, normalized))
}

func (r *Repository) ListPage(ctx context.Context, query site.ListQuery) (site.Page, error) {
	if ctx == nil {
		return site.Page{}, errors.New("list sites context is nil")
	}
	if query.Page < 1 || query.PerPage < 1 || query.PerPage > 100 {
		return site.Page{}, errors.New("site pagination is invalid")
	}
	search := strings.TrimSpace(query.Search)
	allowed := make([]int64, len(query.Scope.SiteIDs))
	for index, id := range query.Scope.SiteIDs {
		allowed[index] = int64(id)
	}
	var excluded *int64
	if query.ExcludeID != nil {
		value := int64(*query.ExcludeID)
		excluded = &value
	}

	var total int
	err := r.connector.Pool().QueryRow(ctx, `
SELECT count(*)
FROM core.sites
WHERE ($1 = '' OR domain ILIKE '%' || $1 || '%')
  AND ($2 OR id = ANY($3::bigint[]))
  AND ($4::bigint IS NULL OR id <> $4);`, search, query.Scope.All, allowed, excluded).Scan(&total)
	if err != nil {
		return site.Page{}, fmt.Errorf("count core sites: %w", err)
	}

	rows, err := r.connector.Pool().Query(ctx, `SELECT `+siteColumns+`
FROM core.sites
WHERE ($1 = '' OR domain ILIKE '%' || $1 || '%')
  AND ($2 OR id = ANY($3::bigint[]))
  AND ($4::bigint IS NULL OR id <> $4)
ORDER BY id
LIMIT $5 OFFSET $6;`,
		search,
		query.Scope.All,
		allowed,
		excluded,
		query.PerPage,
		(query.Page-1)*query.PerPage,
	)
	if err != nil {
		return site.Page{}, fmt.Errorf("query paginated core sites: %w", err)
	}
	items, err := scanSites(rows)
	if err != nil {
		return site.Page{}, err
	}
	return site.Page{Items: items, Total: total}, nil
}

func (r *Repository) Statistics(
	ctx context.Context,
	query site.StatisticsQuery,
) (site.Statistics, error) {
	if ctx == nil {
		return site.Statistics{}, errors.New("site statistics context is nil")
	}
	if query.Limit < 1 || query.Limit > 100 {
		return site.Statistics{}, errors.New("site statistics limit is invalid")
	}
	allowed := make([]int64, len(query.Scope.SiteIDs))
	for index, id := range query.Scope.SiteIDs {
		allowed[index] = int64(id)
	}

	var result site.Statistics
	err := r.connector.Pool().QueryRow(ctx, `
SELECT
    count(*),
    count(*) FILTER (WHERE is_public),
    count(*) FILTER (WHERE NOT is_public)
FROM core.sites
WHERE $1 OR id = ANY($2::bigint[]);`, query.Scope.All, allowed).Scan(
		&result.Total,
		&result.Public,
		&result.Private,
	)
	if err != nil {
		return site.Statistics{}, fmt.Errorf("count core site statistics: %w", err)
	}

	rows, err := r.connector.Pool().Query(ctx, `SELECT `+siteColumns+`
FROM core.sites
WHERE $1 OR id = ANY($2::bigint[])
ORDER BY domain, id
LIMIT $3;`, query.Scope.All, allowed, query.Limit)
	if err != nil {
		return site.Statistics{}, fmt.Errorf("query core site statistics: %w", err)
	}
	result.Items, err = scanSites(rows)
	if err != nil {
		return site.Statistics{}, err
	}
	return result, nil
}

func (r *Repository) Create(
	ctx context.Context,
	actorID *security.UserID,
	item site.Site,
) (site.Site, error) {
	if ctx == nil {
		return site.Site{}, errors.New("create site context is nil")
	}
	rawSettings, err := encodeSettings(item.Settings)
	if err != nil {
		return site.Site{}, err
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return site.Site{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := scanOne(tx.QueryRow(ctx, `
INSERT INTO core.sites
    (profile_code, domain, locale, settings, is_public, created_by, updated_by)
VALUES ($1, $2, $3, $4::jsonb, $5, $6, $6)
RETURNING `+siteColumns+`;`,
		item.ProfileCode,
		item.Domain,
		item.Locale,
		rawSettings,
		item.IsPublic,
		actorID,
	))
	if err != nil {
		return site.Site{}, translateError("create core site", err)
	}
	if err := replaceFileReferences(ctx, tx, "site", int64(result.ID), item.FileReferences); err != nil {
		return site.Site{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return site.Site{}, err
	}
	result.FileReferences = cloneFileReferences(item.FileReferences)
	return result, nil
}

func (r *Repository) Update(
	ctx context.Context,
	actorID *security.UserID,
	item site.Site,
) (site.Site, error) {
	if ctx == nil {
		return site.Site{}, errors.New("update site context is nil")
	}
	if item.ID <= 0 {
		return site.Site{}, errors.New("invalid site id")
	}
	rawSettings, err := encodeSettings(item.Settings)
	if err != nil {
		return site.Site{}, err
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return site.Site{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := scanOne(tx.QueryRow(ctx, `
UPDATE core.sites
SET profile_code = $2, domain = $3, locale = $4, settings = $5::jsonb,
    is_public = $6, updated_at = now(), updated_by = $7
WHERE id = $1
RETURNING `+siteColumns+`;`,
		item.ID,
		item.ProfileCode,
		item.Domain,
		item.Locale,
		rawSettings,
		item.IsPublic,
		actorID,
	))
	if err != nil {
		return site.Site{}, translateError(fmt.Sprintf("update core site %d", item.ID), err)
	}
	if err := replaceFileReferences(ctx, tx, "site", int64(result.ID), item.FileReferences); err != nil {
		return site.Site{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return site.Site{}, err
	}
	result.FileReferences = cloneFileReferences(item.FileReferences)
	return result, nil
}

func (r *Repository) Delete(ctx context.Context, id site.ID) error {
	if ctx == nil {
		return errors.New("delete site context is nil")
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
DELETE FROM core.file_field_references
WHERE owner_kind = 'resource'
  AND owner_id IN (SELECT id FROM core.resources WHERE site_id = $1);
`, id); err != nil {
		return fmt.Errorf("delete site resource file references: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core.file_field_references WHERE owner_kind = 'site' AND owner_id = $1;`, id); err != nil {
		return fmt.Errorf("delete site file references: %w", err)
	}
	result, err := tx.Exec(ctx, `DELETE FROM core.sites WHERE id = $1;`, id)
	if err != nil {
		return fmt.Errorf("delete core site %d: %w", id, err)
	}
	if result.RowsAffected() == 0 {
		return site.ErrNotFound
	}
	return tx.Commit(ctx)
}

type rowScanner interface {
	Scan(...any) error
}

func scanOne(row rowScanner) (site.Site, error) {
	var item site.Site
	var rawSettings []byte
	if err := row.Scan(
		&item.ID,
		&item.ProfileCode,
		&item.Domain,
		&item.Locale,
		&rawSettings,
		&item.IsPublic,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.CreatedBy,
		&item.UpdatedBy,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return site.Site{}, site.ErrNotFound
		}
		return site.Site{}, err
	}
	settings, err := decodeSettings(rawSettings)
	if err != nil {
		return site.Site{}, fmt.Errorf("decode settings for site %d: %w", item.ID, err)
	}
	item.Settings = settings
	return item, nil
}

func scanSites(rows pgx.Rows) ([]site.Site, error) {
	defer rows.Close()
	items := make([]site.Site, 0)
	for rows.Next() {
		item, err := scanOne(rows)
		if err != nil {
			return nil, fmt.Errorf("scan core site: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate core sites: %w", err)
	}
	return items, nil
}

func encodeSettings(settings map[string]any) (string, error) {
	if settings == nil {
		settings = map[string]any{}
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("encode site settings: %w", err)
	}
	return string(raw), nil
}

func decodeSettings(raw []byte) (map[string]any, error) {
	result := make(map[string]any)
	if len(raw) == 0 {
		return result, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func replaceFileReferences(
	ctx context.Context,
	tx pgx.Tx,
	ownerKind string,
	ownerID int64,
	references map[string]file.ID,
) error {
	if _, err := tx.Exec(ctx, `DELETE FROM core.file_field_references WHERE owner_kind = $1 AND owner_id = $2;`, ownerKind, ownerID); err != nil {
		return fmt.Errorf("delete file field references: %w", err)
	}
	for key, id := range references {
		if _, err := tx.Exec(ctx, `
INSERT INTO core.file_field_references (owner_kind, owner_id, field_key, file_id)
VALUES ($1, $2, $3, $4);`, ownerKind, ownerID, key, id); err != nil {
			return fmt.Errorf("insert file field reference: %w", err)
		}
	}
	return nil
}

func cloneFileReferences(source map[string]file.ID) map[string]file.ID {
	if source == nil {
		return nil
	}
	result := make(map[string]file.ID, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func translateError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == pgerrcode.UniqueViolation {
		return fmt.Errorf("%w: %v", site.ErrConflict, err)
	}
	if errors.Is(err, site.ErrNotFound) {
		return site.ErrNotFound
	}
	return fmt.Errorf("%s: %w", operation, err)
}

var _ site.Repository = (*Repository)(nil)
var _ site.ManagementRepository = (*Repository)(nil)
var _ site.StatisticsRepository = (*Repository)(nil)
