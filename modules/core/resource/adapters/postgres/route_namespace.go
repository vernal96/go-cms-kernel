package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

type routeQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func lockRouteNamespace(ctx context.Context, tx pgx.Tx, siteID site.ID) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('core.routes:' || $1::bigint::text, 0));`, siteID); err != nil {
		return fmt.Errorf("lock site route namespace: %w", err)
	}
	return nil
}

func routeResourceByID(ctx context.Context, queryer routeQueryer, id resource.ID) (resource.Resource, error) {
	item, err := scanResource(queryer.QueryRow(ctx, `
SELECT id, site_id, parent_id, type, template, content_type, title, menu_title, slug, path,
 annotation, content, image_media_id, target_resource_id, external_url, is_public, is_searchable,
 in_menu, in_sitemap, sort, published_at, unpublished_at, type_settings, created_at, updated_at,
 created_by, updated_by, deleted_at, deleted_by
FROM core.resources WHERE id=$1;`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return resource.Resource{}, resource.ErrNotFound
	}
	return item, err
}

func ensureLibraryItemRouteAvailable(ctx context.Context, queryer routeQueryer, library resource.Resource, item resource.LibraryItem) error {
	path, err := resource.EffectiveLibraryItemURL(library, item)
	if err != nil {
		return err
	}
	var treeConflict bool
	if err := queryer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core.resources WHERE site_id=$1 AND path=$2);`, item.SiteID, path).Scan(&treeConflict); err != nil {
		return translateError(err)
	}
	if treeConflict {
		return resource.ErrRouteConflict
	}
	conflict, err := libraryItemRouteExists(ctx, queryer, item.SiteID, path, item.ID, nil)
	if err != nil {
		return err
	}
	if conflict {
		return resource.ErrRouteConflict
	}
	return nil
}

func ensureTreePathsAvailable(ctx context.Context, queryer routeQueryer, siteID site.ID, paths []string, override *resource.Resource) error {
	for _, path := range paths {
		if path == "" {
			continue
		}
		conflict, err := libraryItemRouteExists(ctx, queryer, siteID, path, 0, override)
		if err != nil {
			return err
		}
		if conflict {
			return resource.ErrRouteConflict
		}
	}
	return nil
}

func ensureProspectiveLibraryNamespaceAvailable(ctx context.Context, queryer routeQueryer, library resource.Resource) error {
	if library.Path == nil {
		return nil
	}
	rows, err := queryer.Query(ctx, `
SELECT id, site_id, parent_id, type, template, content_type, title, menu_title, slug, path,
 annotation, content, image_media_id, target_resource_id, external_url, is_public, is_searchable,
 in_menu, in_sitemap, sort, published_at, unpublished_at, type_settings, created_at, updated_at,
 created_by, updated_by, deleted_at, deleted_by
FROM core.resources
WHERE site_id=$1 AND type='library' AND id<>$2 AND path IS NOT NULL
  AND (path='/' OR $3='/' OR path=$3 OR path LIKE $3||'/%' OR $3 LIKE path||'/%')
ORDER BY length(path), id;`, library.SiteID, library.ID, *library.Path)
	if err != nil {
		return translateError(err)
	}
	defer rows.Close()
	peers := make([]resource.Resource, 0, 4)
	for rows.Next() {
		peer, err := scanResource(rows)
		if err != nil {
			return err
		}
		peers = append(peers, peer)
	}
	if err := rows.Err(); err != nil {
		return translateError(err)
	}
	potentialPeers := peers[:0]
	for _, peer := range peers {
		if libraryNamespacesMayOverlap(library, peer) {
			potentialPeers = append(potentialPeers, peer)
		}
	}
	if len(potentialPeers) == 0 {
		return nil
	}
	hasItems, err := libraryHasItems(ctx, queryer, library.ID)
	if err != nil || !hasItems {
		return err
	}
	for _, peer := range potentialPeers {
		peerHasItems, err := libraryHasItems(ctx, queryer, peer.ID)
		if err != nil {
			return err
		}
		if peerHasItems {
			return resource.ErrRouteMutationRequiresMaintenance
		}
	}
	return nil
}

func libraryNamespacesMayOverlap(left, right resource.Resource) bool {
	if left.Path == nil || right.Path == nil {
		return false
	}
	leftPattern, _ := left.TypeSettings["item_url_pattern"].(string)
	if leftPattern == "" {
		leftPattern = resourcetype.DefaultItemURLPattern
	}
	rightPattern, _ := right.TypeSettings["item_url_pattern"].(string)
	if rightPattern == "" {
		rightPattern = resourcetype.DefaultItemURLPattern
	}
	leftSegments := strings.Split(strings.Trim(strings.TrimRight(*left.Path, "/")+leftPattern, "/"), "/")
	rightSegments := strings.Split(strings.Trim(strings.TrimRight(*right.Path, "/")+rightPattern, "/"), "/")
	if len(leftSegments) != len(rightSegments) {
		return false
	}
	for index := range leftSegments {
		if libraryPatternSegmentsDisjoint(leftSegments[index], rightSegments[index]) {
			return false
		}
	}
	return true
}

func libraryPatternSegmentsDisjoint(left, right string) bool {
	leftShape := libraryPatternSegmentShape(left)
	rightShape := libraryPatternSegmentShape(right)
	if !leftShape.dynamic && !rightShape.dynamic {
		return left != right
	}
	if !leftShape.dynamic {
		return !rightShape.expression.MatchString(left)
	}
	if !rightShape.dynamic {
		return !leftShape.expression.MatchString(right)
	}
	if leftShape.maxLength >= 0 && leftShape.maxLength < rightShape.minLength ||
		rightShape.maxLength >= 0 && rightShape.maxLength < leftShape.minLength {
		return true
	}
	if leftShape.prefix != "" && rightShape.prefix != "" &&
		!strings.HasPrefix(leftShape.prefix, rightShape.prefix) &&
		!strings.HasPrefix(rightShape.prefix, leftShape.prefix) {
		return true
	}
	return leftShape.suffix != "" && rightShape.suffix != "" &&
		!strings.HasSuffix(leftShape.suffix, rightShape.suffix) &&
		!strings.HasSuffix(rightShape.suffix, leftShape.suffix)
}

type libraryPatternShape struct {
	dynamic    bool
	prefix     string
	suffix     string
	minLength  int
	maxLength  int
	expression *regexp.Regexp
}

func libraryPatternSegmentShape(segment string) libraryPatternShape {
	shape := libraryPatternShape{maxLength: 0}
	var expression strings.Builder
	expression.WriteByte('^')
	firstToken := strings.IndexByte(segment, '{')
	lastTokenEnd := -1
	for cursor := 0; cursor < len(segment); {
		open := strings.IndexByte(segment[cursor:], '{')
		if open < 0 {
			literal := segment[cursor:]
			expression.WriteString(regexp.QuoteMeta(literal))
			shape.minLength += len(literal)
			if shape.maxLength >= 0 {
				shape.maxLength += len(literal)
			}
			break
		}
		open += cursor
		literal := segment[cursor:open]
		expression.WriteString(regexp.QuoteMeta(literal))
		shape.minLength += len(literal)
		if shape.maxLength >= 0 {
			shape.maxLength += len(literal)
		}
		close := strings.IndexByte(segment[open:], '}') + open
		shape.dynamic = true
		lastTokenEnd = close + 1
		switch segment[open+1 : close] {
		case "year":
			expression.WriteString(`[0-9]{4}`)
			shape.minLength += 4
			if shape.maxLength >= 0 {
				shape.maxLength += 4
			}
		case "month", "day":
			expression.WriteString(`[0-9]{2}`)
			shape.minLength += 2
			if shape.maxLength >= 0 {
				shape.maxLength += 2
			}
		case "id":
			expression.WriteString(`[1-9][0-9]*`)
			shape.minLength++
			shape.maxLength = -1
		case "slug":
			expression.WriteString(`[a-z0-9]+(?:-[a-z0-9]+)*`)
			shape.minLength++
			shape.maxLength = -1
		}
		cursor = close + 1
	}
	expression.WriteByte('$')
	shape.expression = regexp.MustCompile(expression.String())
	if firstToken >= 0 {
		shape.prefix = segment[:firstToken]
		shape.suffix = segment[lastTokenEnd:]
	}
	return shape
}

func libraryHasItems(ctx context.Context, queryer routeQueryer, libraryID resource.ID) (bool, error) {
	var exists bool
	if err := queryer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core.library_item_routes WHERE library_id=$1 LIMIT 1);`, libraryID).Scan(&exists); err != nil {
		return false, translateError(err)
	}
	return exists, nil
}

func libraryItemRouteExists(ctx context.Context, queryer routeQueryer, siteID site.ID, path string, excludeID resource.ID, override *resource.Resource) (bool, error) {
	rows, err := queryer.Query(ctx, `
SELECT id, site_id, parent_id, type, template, content_type, title, menu_title, slug, path,
 annotation, content, image_media_id, target_resource_id, external_url, is_public, is_searchable,
 in_menu, in_sitemap, sort, published_at, unpublished_at, type_settings, created_at, updated_at,
 created_by, updated_by, deleted_at, deleted_by
FROM core.resources
WHERE site_id=$1 AND type='library' AND path IS NOT NULL
  AND (path='/' OR $2=path OR $2 LIKE path||'/%')
ORDER BY length(path) DESC, id;`, siteID, path)
	if err != nil {
		return false, translateError(err)
	}
	defer rows.Close()
	libraries := make([]resource.Resource, 0, 4)
	for rows.Next() {
		library, err := scanResource(rows)
		if err != nil {
			return false, err
		}
		libraries = append(libraries, library)
	}
	if err := rows.Err(); err != nil {
		return false, translateError(err)
	}
	if override != nil && override.SiteID == siteID && override.Type == resourcetype.Library {
		filtered := libraries[:0]
		for _, library := range libraries {
			if library.ID != override.ID {
				filtered = append(filtered, library)
			}
		}
		libraries = filtered
		if override.Path != nil && pathInLibrary(path, *override.Path) {
			libraries = append(libraries, resource.Clone(*override))
		}
	}
	for _, library := range libraries {
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
		var candidate resource.LibraryItem
		if key.ID > 0 {
			candidate, err = scanLibraryItem(queryer.QueryRow(ctx, `SELECT `+libraryItemColumns+` FROM core.library_item_routes route JOIN core.library_items item ON item.id=route.resource_id AND item.library_id=route.library_id WHERE route.library_id=$1 AND route.resource_id=$2;`, library.ID, key.ID))
		} else {
			candidate, err = scanLibraryItem(queryer.QueryRow(ctx, `SELECT `+libraryItemColumns+` FROM core.library_item_routes route JOIN core.library_items item ON item.id=route.resource_id AND item.library_id=route.library_id WHERE route.library_id=$1 AND route.slug=$2;`, library.ID, key.Slug))
		}
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, translateError(err)
		}
		if candidate.ID == excludeID {
			continue
		}
		effectiveURL, err := resource.EffectiveLibraryItemURL(library, candidate)
		if err != nil {
			return false, err
		}
		if effectiveURL == path {
			return true, nil
		}
	}
	return false, nil
}

func pathInLibrary(path, libraryPath string) bool {
	return libraryPath == "/" || path == libraryPath || strings.HasPrefix(path, strings.TrimRight(libraryPath, "/")+"/")
}

func prospectiveTreePaths(ctx context.Context, queryer routeQueryer, rootID resource.ID, rootPath *string) ([]string, error) {
	rows, err := queryer.Query(ctx, `
WITH RECURSIVE tree AS (
 SELECT id, $2::text AS path FROM core.resources WHERE id=$1
 UNION ALL
 SELECT child.id,
  CASE WHEN child.path IS NULL OR tree.path IS NULL THEN NULL
       WHEN tree.path='/' THEN '/'||child.slug ELSE tree.path||'/'||child.slug END
 FROM core.resources child JOIN tree ON child.parent_id=tree.id
)
SELECT path FROM tree WHERE path IS NOT NULL;`, rootID, rootPath)
	if err != nil {
		return nil, translateError(err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, translateError(rows.Err())
}
