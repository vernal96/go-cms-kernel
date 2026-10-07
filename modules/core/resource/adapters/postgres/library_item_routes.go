package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
)

func (r *Repository) lookupLibraryItemRoute(ctx context.Context, siteID site.ID, path string) (resource.LibraryItem, resource.Resource, error) {
	rows, err := r.connector.Pool().Query(ctx, `SELECT id, site_id, parent_id, type, template, content_type, title, menu_title, slug, path, annotation, content, image_media_id, target_resource_id, external_url, is_public, is_searchable, in_menu, in_sitemap, sort, published_at, unpublished_at, type_settings, created_at, updated_at, created_by, updated_by, deleted_at, deleted_by FROM core.resources WHERE site_id=$1 AND type IN ('library','library_mirror') AND path IS NOT NULL AND (path='/' OR $2=path OR $2 LIKE path||'/%') ORDER BY length(path) DESC, id;`, siteID, path)
	if err != nil {
		return resource.LibraryItem{}, resource.Resource{}, err
	}
	defer rows.Close()
	libraries := make([]resource.Resource, 0)
	for rows.Next() {
		library, err := scanResource(rows)
		if err != nil {
			return resource.LibraryItem{}, resource.Resource{}, err
		}
		libraries = append(libraries, library)
	}
	if err := rows.Err(); err != nil {
		return resource.LibraryItem{}, resource.Resource{}, err
	}
	rows.Close()
	for _, mount := range libraries {
		library, err := effectiveRouteLibrary(ctx, r.connector.Pool(), mount, nil)
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
		return item, mount, nil
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
	return scanLibraryItemColumns(scanner)
}

func scanLibraryItemWithVersion(scanner rowScanner) (resource.LibraryItem, error) {
	var version *int64
	item, err := scanLibraryItemColumns(scanner, &version)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	if version == nil {
		return resource.LibraryItem{}, fmt.Errorf("library item %d has no resource version", item.ID)
	}
	item.Version = *version
	return item, nil
}

func scanLibraryItemColumns(scanner rowScanner, extra ...any) (resource.LibraryItem, error) {
	var item resource.LibraryItem
	var templateCode, contentType *string
	var imageMediaID *int64
	columns := []any{&item.ID, &item.SiteID, &item.LibraryID, &templateCode, &contentType, &item.Title, &item.Slug, &item.Annotation, &item.Content, &imageMediaID, &item.IsPublic, &item.IsSearchable, &item.PublishedAt, &item.UnpublishedAt, &item.CreatedAt, &item.UpdatedAt, &item.CreatedBy, &item.UpdatedBy, &item.DeletedAt, &item.DeletedBy}
	err := scanner.Scan(append(columns, extra...)...)
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

var _ resource.LibraryItemRepository = (*Repository)(nil)

func (r *Repository) ResolveLibraryItemRoute(ctx context.Context, siteID site.ID, path string) (resource.LibraryItem, resource.Resource, error) {
	item, library, err := r.lookupLibraryItemRoute(ctx, siteID, path)
	if err != nil {
		return item, library, err
	}
	if err = r.loadLibraryItemFields(ctx, r.connector.Pool(), &item); err != nil {
		return resource.LibraryItem{}, resource.Resource{}, err
	}
	return item, library, nil
}
