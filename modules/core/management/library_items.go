package management

import (
	"context"
	"errors"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type LibraryItemDTO struct {
	ImageMediaID  *media.ID        `json:"image_media_id"`
	ID            resource.ID      `json:"id"`
	Version       int64            `json:"version"`
	SiteID        site.ID          `json:"site_id"`
	LibraryID     resource.ID      `json:"library_id"`
	TemplateCode  *template.Code   `json:"template_code"`
	Title         string           `json:"title"`
	Slug          string           `json:"slug"`
	Annotation    string           `json:"annotation"`
	ContentType   *string          `json:"content_type"`
	Content       string           `json:"content"`
	IsPublic      bool             `json:"is_public"`
	IsSearchable  bool             `json:"is_searchable"`
	PublishedAt   *time.Time       `json:"published_at"`
	UnpublishedAt *time.Time       `json:"unpublished_at"`
	Deleted       bool             `json:"deleted"`
	DeletedAt     *time.Time       `json:"deleted_at"`
	Fields        map[string]any   `json:"fields"`
	Widgets       []ResourceWidget `json:"widgets"`
	EffectiveURL  string           `json:"effective_url"`
}

type LibraryItemDetails struct {
	Item        LibraryItemDTO `json:"item"`
	Permissions struct {
		Update        bool `json:"update"`
		Delete        bool `json:"delete"`
		Restore       bool `json:"restore"`
		HistoryRead   bool `json:"history_read"`
		HistoryDelete bool `json:"history_delete"`
	} `json:"permissions"`
}

type LibraryItemsPage struct {
	Items      []LibraryItemDTO `json:"items"`
	NextCursor string           `json:"next_cursor"`
}

type LibraryItemsInput struct {
	Cursor  string
	Limit   int
	Search  string
	Filters []resource.FilterCondition
	Sort    []resource.Sort
}

type LibraryItemCreateInput struct {
	ImageMediaID  *media.ID
	Template      *template.Code
	Title         string
	Slug          string
	Annotation    string
	Content       string
	IsPublic      *bool
	IsSearchable  *bool
	PublishedAt   *time.Time
	UnpublishedAt *time.Time
	Fields        map[string]any
}

type LibraryItemUpdateInput struct {
	LibraryItemCreateInput
	ExpectedVersion int64
	IsPublic        *bool
	IsSearchable    *bool
}

func (m *Resources) LibraryItems(ctx context.Context, actor security.Actor, siteID site.ID, libraryID resource.ID, input LibraryItemsInput) (LibraryItemsPage, error) {
	if m.libraryItems == nil {
		return LibraryItemsPage{}, errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessEdit); err != nil {
		return LibraryItemsPage{}, err
	}
	page, err := m.libraryItems.Query(ctx, actor, resource.LibraryItemQuery{
		SiteID: siteID, LibraryID: libraryID, Cursor: input.Cursor, Limit: input.Limit,
		Search: input.Search, Filters: input.Filters, Sort: input.Sort,
	})
	if err != nil {
		return LibraryItemsPage{}, validationError(err)
	}
	library, err := m.resources.Get(ctx, actor, libraryID)
	if err != nil {
		return LibraryItemsPage{}, err
	}
	result := LibraryItemsPage{Items: make([]LibraryItemDTO, len(page.Items)), NextCursor: page.NextCursor}
	for index, item := range page.Items {
		result.Items[index] = libraryItemDTO(library, item)
	}
	return result, nil
}

func (m *Resources) CreateLibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, libraryID resource.ID, input LibraryItemCreateInput) (LibraryItemDetails, error) {
	if m.libraryItems == nil {
		return LibraryItemDetails{}, errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceCreatePermission, SiteAccessEdit); err != nil {
		return LibraryItemDetails{}, err
	}
	item, err := m.libraryItems.Create(ctx, actor, resource.CreateLibraryItemInput{ImageMediaID: input.ImageMediaID, SiteID: siteID, LibraryID: libraryID, Template: input.Template, Title: input.Title, Slug: input.Slug, Annotation: input.Annotation, Content: input.Content, IsPublic: input.IsPublic, IsSearchable: input.IsSearchable, PublishedAt: input.PublishedAt, UnpublishedAt: input.UnpublishedAt, Fields: input.Fields})
	if err != nil {
		return LibraryItemDetails{}, validationError(err)
	}
	return m.libraryItemDetails(ctx, actor, item)
}

func (m *Resources) LibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, itemID resource.ID) (LibraryItemDetails, error) {
	if m.libraryItems == nil {
		return LibraryItemDetails{}, errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessEdit); err != nil {
		return LibraryItemDetails{}, err
	}
	item, err := m.libraryItems.Get(ctx, actor, itemID)
	if err != nil {
		return LibraryItemDetails{}, err
	}
	if item.SiteID != siteID {
		return LibraryItemDetails{}, resource.ErrNotFound
	}
	return m.libraryItemDetails(ctx, actor, item)
}

func (m *Resources) UpdateLibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, itemID resource.ID, input LibraryItemUpdateInput) (LibraryItemDetails, error) {
	if m.libraryItems == nil {
		return LibraryItemDetails{}, errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return LibraryItemDetails{}, err
	}
	current, err := m.libraryItems.Get(ctx, actor, itemID)
	if err != nil {
		return LibraryItemDetails{}, err
	}
	if current.SiteID != siteID {
		return LibraryItemDetails{}, resource.ErrNotFound
	}
	if input.IsPublic == nil || input.IsSearchable == nil {
		return LibraryItemDetails{}, ErrValidation
	}
	item, err := m.libraryItems.Update(ctx, actor, resource.UpdateLibraryItemInput{ImageMediaID: input.ImageMediaID, ID: itemID, ExpectedVersion: input.ExpectedVersion, Template: input.Template, Title: input.Title, Slug: input.Slug, Annotation: input.Annotation, Content: input.Content, IsPublic: *input.IsPublic, IsSearchable: *input.IsSearchable, PublishedAt: input.PublishedAt, UnpublishedAt: input.UnpublishedAt, Fields: input.Fields})
	if err != nil {
		return LibraryItemDetails{}, validationError(err)
	}
	return m.libraryItemDetails(ctx, actor, item)
}

func (m *Resources) MoveLibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, itemID, targetLibraryID resource.ID, expectedVersion int64) (LibraryItemDetails, error) {
	if m.libraryItems == nil {
		return LibraryItemDetails{}, errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return LibraryItemDetails{}, err
	}
	current, err := m.libraryItems.Get(ctx, actor, itemID)
	if err != nil {
		return LibraryItemDetails{}, err
	}
	if current.SiteID != siteID {
		return LibraryItemDetails{}, resource.ErrNotFound
	}
	item, err := m.libraryItems.Move(ctx, actor, itemID, targetLibraryID, expectedVersion)
	if err != nil {
		return LibraryItemDetails{}, validationError(err)
	}
	return m.libraryItemDetails(ctx, actor, item)
}

func (m *Resources) DeleteLibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, itemID resource.ID, permanent bool) error {
	if m.libraryItems == nil {
		return errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceDeletePermission, SiteAccessEdit); err != nil {
		return err
	}
	item, err := m.libraryItems.Get(ctx, actor, itemID)
	if err != nil {
		return err
	}
	if item.SiteID != siteID {
		return resource.ErrNotFound
	}
	return m.libraryItems.Delete(ctx, actor, itemID, permanent)
}

func (m *Resources) RestoreLibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, itemID resource.ID) error {
	if m.libraryItems == nil {
		return errors.New("library item service is unavailable")
	}
	if err := m.requireSite(ctx, actor, siteID, ResourceDeletePermission, SiteAccessEdit); err != nil {
		return err
	}
	item, err := m.libraryItems.Get(ctx, actor, itemID)
	if err != nil {
		return err
	}
	if item.SiteID != siteID {
		return resource.ErrNotFound
	}
	return m.libraryItems.Restore(ctx, actor, itemID)
}

func (m *Resources) libraryItemDetails(ctx context.Context, actor security.Actor, item resource.LibraryItem) (LibraryItemDetails, error) {
	library, err := m.resources.Get(ctx, actor, item.LibraryID)
	if err != nil {
		return LibraryItemDetails{}, err
	}
	codes := []permission.Code{ResourceUpdatePermission, ResourceDeletePermission}
	if m.revisions.LibraryHistoryEnabled(item.SiteID) {
		codes = append(codes, resource.HistoryReadPermission, resource.HistoryDeletePermission)
	}
	allowed, err := m.allowedPermissions(ctx, actor, codes)
	if err != nil {
		return LibraryItemDetails{}, err
	}
	result := LibraryItemDetails{Item: libraryItemDTO(library, item)}
	result.Permissions.Update = allowed[ResourceUpdatePermission]
	result.Permissions.Delete = allowed[ResourceDeletePermission]
	result.Permissions.Restore = result.Permissions.Delete && library.DeletedAt == nil
	result.Permissions.HistoryRead = allowed[resource.HistoryReadPermission]
	result.Permissions.HistoryDelete = allowed[resource.HistoryDeletePermission]
	return result, nil
}

func libraryItemDTO(library resource.Resource, item resource.LibraryItem) LibraryItemDTO {
	url, _ := resource.EffectiveLibraryItemURL(library, item)
	fields := make(map[string]any, len(item.Fields))
	for key, value := range item.Fields {
		fields[key] = value
	}
	return LibraryItemDTO{ImageMediaID: item.ImageMediaID, ID: item.ID, Version: item.Version, SiteID: item.SiteID, LibraryID: item.LibraryID, TemplateCode: item.Template, Title: item.Title, Slug: item.Slug, Annotation: item.Annotation, ContentType: item.ContentType, Content: item.Content, IsPublic: item.IsPublic, IsSearchable: item.IsSearchable, PublishedAt: item.PublishedAt, UnpublishedAt: item.UnpublishedAt, Deleted: item.DeletedAt != nil, DeletedAt: item.DeletedAt, Fields: fields, Widgets: resourceWidgets(item.Widgets), EffectiveURL: url}
}
