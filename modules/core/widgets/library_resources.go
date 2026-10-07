package widgets

import (
	"context"
	"errors"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/field/validation"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

var LibraryResources = widget.NewRef("library_resources")
var LibraryMirrorResources = widget.NewRef("library_mirror_resources")

type LibraryCollectionQuery func(context.Context, resource.LibraryItemQuery) (resource.LibraryItemPage, resource.LibraryCollection, error)

type libraryResourcesWidget struct {
	query  LibraryCollectionQuery
	mirror bool
}

func NewLibraryResources(query LibraryCollectionQuery, mirror bool) widget.Widget {
	return libraryResourcesWidget{query: query, mirror: mirror}
}

func (w libraryResourcesWidget) Definition() widget.Definition {
	reference, label := LibraryResources, "Ресурсы библиотеки"
	if w.mirror {
		reference, label = LibraryMirrorResources, "Ресурсы зеркала библиотеки"
	}
	return widget.Definition{Reference: reference, Label: label,
		Description: "Постраничный список опубликованных ресурсов текущей библиотеки",
		Fields: []field.Definition{
			{Key: "per_page", Type: field.TypeInteger, Label: "Ресурсов на странице", Required: true, Validators: []field.ValidatorDefinition{validation.Min(1), validation.Max(100)}},
			{Key: "filters", Type: field.TypeJSON, Label: "Фильтры ресурсов", Editor: "json"},
			{Key: "sorting", Type: field.TypeJSON, Label: "Сортировка ресурсов", Editor: "json"},
		}, SummaryFields: []string{"per_page"}}
}

func (w libraryResourcesWidget) New(values map[string]any) (widget.Instance, error) {
	if w.query == nil {
		return nil, errors.New("library collection query is unavailable")
	}
	limit := intValue(values, "per_page")
	if limit < 1 || limit > 100 {
		return nil, errors.New("per_page must be between 1 and 100")
	}
	filters, err := parseResourceFilters(values["filters"])
	if err != nil {
		return nil, err
	}
	sorts, err := resourceSorting(values["sorting"])
	if err != nil {
		return nil, err
	}
	for _, condition := range filters {
		if !resource.IsCustomFieldPath(condition.Field) {
			if err := condition.Validate(); err != nil {
				return nil, err
			}
		}
		if !resource.LibraryItemFilterField(condition.Field) {
			return nil, errors.New("resource filter field is invalid")
		}
	}
	for _, sort := range sorts {
		if !resource.LibraryItemSortField(sort.Field) {
			return nil, errors.New("resource sort field is invalid")
		}
	}
	return &libraryResourcesInstance{widget: w, limit: limit, filters: filters, sorts: sorts}, nil
}

// Collections remain uncached: publication time and mutations in either site
// take effect on the next read, with bounded keyset queries in the repository.
type libraryResourcesInstance struct {
	widget  libraryResourcesWidget
	limit   int
	filters []resource.FilterCondition
	sorts   []resource.Sort
}

func (i *libraryResourcesInstance) Render(ctx context.Context, input widget.RenderInput) (map[string]any, error) {
	if len(input.Cursor) > 16384 {
		return nil, resource.ErrInvalid
	}
	page, collection, err := i.widget.query(ctx, resource.LibraryItemQuery{
		SiteID: site.ID(input.Site.ID), LibraryID: resource.ID(input.Resource.ID),
		Limit: i.limit, Cursor: input.Cursor, PublicOnly: true, Filters: append([]resource.FilterCondition(nil), i.filters...), Sort: append([]resource.Sort(nil), i.sorts...),
	})
	if err != nil {
		return nil, err
	}
	expected := resourcetype.Library
	if i.widget.mirror {
		expected = resourcetype.LibraryMirror
	}
	if collection.Mount.Type != expected {
		return nil, resource.ErrInvalidReference
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		url, err := collection.URL(item)
		if err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"id": item.ID, "title": item.Title, "annotation": item.Annotation, "url": url, "image_media_id": item.ImageMediaID, "published_at": item.PublishedAt, "fields": item.Fields})
	}
	return map[string]any{"resources": items, "next_cursor": page.NextCursor, "cursor_parameter": "cursor." + input.Key}, nil
}
