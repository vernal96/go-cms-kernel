package postgres

import (
	"context"
	"fmt"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

func (r *Repository) MenuRecords(ctx context.Context, siteID site.ID, input resource.MenuInput) ([]resource.MenuRecord, error) {
	rows, err := r.connector.Pool().Query(ctx, `
WITH RECURSIVE tree(id, level, visited) AS (
 SELECT id, CASE WHEN $2::bigint IS NULL THEN 1 ELSE 0 END, ARRAY[id]
 FROM core.resources WHERE site_id=$1 AND
 (($2::bigint IS NULL AND parent_id IS NULL) OR id=$2)
 UNION ALL
 SELECT r.id, t.level+1, t.visited || r.id
 FROM core.resources r JOIN tree t ON r.parent_id=t.id
 WHERE r.site_id=$1 AND ($3::bigint=0 OR t.level<$3) AND NOT r.id=ANY(t.visited)
), linked(id) AS (
 SELECT id FROM tree
 UNION
 SELECT target.id FROM linked l
 JOIN core.resources source ON source.id=l.id AND source.site_id=$1
 JOIN core.resources target ON target.id=source.target_resource_id AND target.site_id=$1
)
SELECT r.id,r.parent_id,r.type,r.title,r.menu_title,r.path,r.external_url,
 r.target_resource_id,r.sort,r.is_public,r.in_menu,r.published_at,r.unpublished_at,r.deleted_at,
 EXISTS(SELECT 1 FROM tree t WHERE t.id=r.id AND t.level>0)
FROM core.resources r JOIN linked l ON l.id=r.id WHERE r.site_id=$1`, siteID, input.ParentID, input.Depth)
	if err != nil {
		return nil, fmt.Errorf("query menu: %w", err)
	}
	defer rows.Close()
	result := []resource.MenuRecord{}
	for rows.Next() {
		var item resource.MenuRecord
		if err := rows.Scan(&item.ID, &item.ParentID, &item.Type, &item.Title, &item.MenuTitle, &item.Path, &item.ExternalURL, &item.TargetResourceID, &item.Sort, &item.IsPublic, &item.InMenu, &item.PublishedAt, &item.UnpublishedAt, &item.DeletedAt, &item.InTree); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
