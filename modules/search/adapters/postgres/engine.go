package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/connectors/pgtrgm"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/search"
)

type Engine struct{ connector *pgtrgm.Connector }

func NewEngine(connector *connectorpostgres.Connector) (*Engine, error) {
	trigrams, err := pgtrgm.New(connector)
	if err != nil {
		return nil, err
	}
	return &Engine{connector: trigrams}, nil
}

// The same predicates drive both count and page. Publication time is the
// transaction timestamp, so crossing a publication boundary cannot split them.
const matchesSQL = `WITH matches AS (
 SELECT r.id, 'tree'::text AS storage_kind, r.title, r.annotation, r.content,
        r.path, ''::text AS pattern, r.slug, r.created_at, r.published_at
 FROM core.resources r
 WHERE r.site_id=$1 AND r.deleted_at IS NULL AND r.is_public AND r.is_searchable
   AND r.path IS NOT NULL AND r.type=ANY($4::text[])
   AND (r.published_at IS NULL OR r.published_at<=now())
   AND (r.unpublished_at IS NULL OR r.unpublished_at>now())
   AND (lower(r.title || E'\n' || r.annotation || E'\n' || r.content) LIKE lower($3)
     OR lower(r.title || E'\n' || r.annotation || E'\n' || r.content) %> lower($2))
   AND (r.type<>'resource_link' OR EXISTS (
     SELECT 1 FROM core.resources target
     WHERE target.id=r.target_resource_id AND target.site_id=r.site_id
       AND target.deleted_at IS NULL AND target.is_public AND target.path IS NOT NULL
       AND target.type=ANY($4::text[])
       AND (target.published_at IS NULL OR target.published_at<=now())
       AND (target.unpublished_at IS NULL OR target.unpublished_at>now())
   ))
   AND (lower(r.title) LIKE lower($3) OR lower(r.annotation) LIKE lower($3) OR lower(r.content) LIKE lower($3)
     OR lower(r.title) %> lower($2) OR lower(r.annotation) %> lower($2) OR lower(r.content) %> lower($2))
 UNION ALL
 SELECT i.id, 'library_item'::text, i.title, i.annotation, i.content,
        library.path, coalesce(library.type_settings->>'item_url_pattern',''), i.slug, i.created_at, i.published_at
 FROM core.library_items i
 JOIN core.resources library ON library.id=i.library_id AND library.site_id=i.site_id
 WHERE i.site_id=$1 AND i.deleted_at IS NULL AND i.is_public AND i.is_searchable
   AND (i.published_at IS NULL OR i.published_at<=now())
   AND (i.unpublished_at IS NULL OR i.unpublished_at>now())
   AND (lower(i.title || E'\n' || i.annotation || E'\n' || i.content) LIKE lower($3)
     OR lower(i.title || E'\n' || i.annotation || E'\n' || i.content) %> lower($2))
   AND library.type='library' AND library.type=ANY($4::text[]) AND library.path IS NOT NULL
   AND library.deleted_at IS NULL AND library.is_public
   AND (library.published_at IS NULL OR library.published_at<=now())
   AND (library.unpublished_at IS NULL OR library.unpublished_at>now())
   AND (lower(i.title) LIKE lower($3) OR lower(i.annotation) LIKE lower($3) OR lower(i.content) LIKE lower($3)
     OR lower(i.title) %> lower($2) OR lower(i.annotation) %> lower($2) OR lower(i.content) %> lower($2))
)
`

const countSQL = matchesSQL + `SELECT count(*) FROM matches`
const pageSQL = matchesSQL + `SELECT id, storage_kind, title, annotation, path, pattern, slug, created_at, published_at,
 greatest(3*word_similarity(lower($2),lower(title)), 2*word_similarity(lower($2),lower(annotation)), word_similarity(lower($2),lower(content))) AS score
 FROM matches
 ORDER BY CASE WHEN lower(title)=lower($2) THEN 0
   WHEN lower(title) LIKE lower($3) OR lower(annotation) LIKE lower($3) OR lower(content) LIKE lower($3) THEN 1 ELSE 2 END,
 score DESC, id
 LIMIT $5 OFFSET $6`

func (e *Engine) Search(ctx context.Context, query search.Query) (search.Page, error) {
	input, err := search.NormalizeInput(query.Input)
	if err != nil {
		return search.Page{}, err
	}
	if query.SiteID <= 0 {
		return search.Page{}, fmt.Errorf("%w: site is required", search.ErrInvalid)
	}
	types := make([]string, len(query.RouteTypes))
	for index, code := range query.RouteTypes {
		types[index] = string(code)
	}
	args := []any{query.SiteID, input.Text, pgtrgm.ContainsPattern(input.Text), types}
	result := search.Page{Items: []search.Item{}, Pagination: search.Pagination{Page: input.Page, PerPage: input.PerPage}}
	err = e.connector.Read(ctx, 0.6, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, countSQL, args...).Scan(&result.Pagination.Total); err != nil {
			return err
		}
		if result.Pagination.Total == 0 {
			return nil
		}
		rows, err := tx.Query(ctx, pageSQL, append(args, input.PerPage, int64(input.Page-1)*int64(input.PerPage))...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item search.Item
			var path, pattern, slug string
			var createdAt time.Time
			var publishedAt *time.Time
			if err := rows.Scan(&item.ID, &item.StorageKind, &item.Title, &item.Annotation, &path, &pattern, &slug, &createdAt, &publishedAt, &item.Score); err != nil {
				return err
			}
			item.URL = path
			if item.StorageKind == resource.StorageLibraryItem {
				item.URL, err = resource.EffectiveLibraryItemURL(
					resource.Resource{Path: &path, TypeSettings: map[string]any{"item_url_pattern": pattern}},
					resource.LibraryItem{ID: item.ID, Slug: slug, CreatedAt: createdAt, PublishedAt: publishedAt},
				)
				if err != nil {
					return fmt.Errorf("search result URL: %w", err)
				}
			}
			result.Items = append(result.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return search.Page{}, err
	}
	return result, nil
}

var _ search.Engine = (*Engine)(nil)
