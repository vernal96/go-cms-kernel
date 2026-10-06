package management

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/modules/resourceextension"
	"github.com/vernal96/go-cms-kernel/security"
)

func (m *Resources) CreateResourceWidget(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	input resource.CreateWidgetInput,
) (ResourceWidget, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceWidget{}, err
	}
	if err := m.requireResourceSite(ctx, actor, siteID, resourceID); err != nil {
		return ResourceWidget{}, err
	}
	created, err := m.resources.CreateWidget(ctx, actor, resourceID, input)
	if err != nil {
		return ResourceWidget{}, validationError(err)
	}
	result := resourceWidgets([]widget.Binding{created})[0]
	current, loadErr := m.resourceEntity(ctx, actor, resourceID)
	if loadErr != nil {
		return ResourceWidget{}, loadErr
	}
	result.ResourceVersion = current.Version
	return result, nil
}

func (m *Resources) UpdateResourceWidget(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	bindingID widget.BindingID,
	input resource.UpdateWidgetInput,
) (ResourceWidget, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceWidget{}, err
	}
	if err := m.requireResourceSite(ctx, actor, siteID, resourceID); err != nil {
		return ResourceWidget{}, err
	}
	updated, err := m.resources.UpdateWidget(ctx, actor, resourceID, bindingID, input)
	if err != nil {
		if errors.Is(err, resource.ErrNotFound) {
			return ResourceWidget{}, err
		}
		return ResourceWidget{}, validationError(err)
	}
	result := resourceWidgets([]widget.Binding{updated})[0]
	current, loadErr := m.resourceEntity(ctx, actor, resourceID)
	if loadErr != nil {
		return ResourceWidget{}, loadErr
	}
	result.ResourceVersion = current.Version
	return result, nil
}

func (m *Resources) DeleteResourceWidget(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	bindingID widget.BindingID,
	expectedVersion int64,
) error {
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return err
	}
	if err := m.requireResourceSite(ctx, actor, siteID, resourceID); err != nil {
		return err
	}
	return m.resources.DeleteWidget(ctx, actor, resourceID, bindingID, expectedVersion)
}

func (m *Resources) ReorderResourceWidgets(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	expectedVersion int64,
	order []widget.Order,
) ([]ResourceWidget, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return nil, err
	}
	if err := m.requireResourceSite(ctx, actor, siteID, resourceID); err != nil {
		return nil, err
	}
	updated, err := m.resources.ReorderWidgets(ctx, actor, resourceID, expectedVersion, order)
	if err != nil {
		return nil, validationError(err)
	}
	return resourceWidgets(updated), nil
}

func (m *Resources) requireResourceSite(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
) error {
	current, err := m.resourceEntity(ctx, actor, resourceID)
	if err != nil {
		return err
	}
	if current.SiteID != siteID {
		return resource.ErrNotFound
	}
	return nil
}

func (m *Resources) resourceEntity(
	ctx context.Context,
	actor security.Actor,
	resourceID resource.ID,
) (resource.Resource, error) {
	current, err := m.resourceRepo.ByID(ctx, resourceID)
	if !errors.Is(err, resource.ErrNotFound) || m.libraryItems == nil {
		return current, err
	}
	item, err := m.libraryItems.Get(ctx, actor, resourceID)
	if err != nil {
		return resource.Resource{}, err
	}
	return resource.Resource{
		ID: item.ID, SiteID: item.SiteID, Version: item.Version, Type: resourcetype.Page,
		Template: item.Template, ContentType: item.ContentType,
		Title: item.Title, Slug: item.Slug, Annotation: item.Annotation,
		Content: item.Content, ImageMediaID: item.ImageMediaID,
		IsPublic: item.IsPublic, IsSearchable: item.IsSearchable,
		PublishedAt: item.PublishedAt, UnpublishedAt: item.UnpublishedAt,
		Fields: item.Fields, FieldValues: item.FieldValues, Widgets: item.Widgets,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		CreatedBy: item.CreatedBy, UpdatedBy: item.UpdatedBy,
		DeletedAt: item.DeletedAt, DeletedBy: item.DeletedBy,
	}, nil
}

func (m *Resources) MoveResource(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	parentID *resource.ID,
	position int,
	expectedVersion int64,
) (ResourceDTO, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceDTO{}, err
	}
	current, err := m.resources.Get(ctx, actor, resourceID)
	if err != nil {
		return ResourceDTO{}, err
	}
	if current.SiteID != siteID {
		return ResourceDTO{}, resource.ErrNotFound
	}
	if parentID != nil {
		parent, parentErr := m.resources.Get(ctx, actor, *parentID)
		if parentErr != nil {
			return ResourceDTO{}, parentErr
		}
		if parent.SiteID != siteID {
			return ResourceDTO{}, resource.ErrInvalidTree
		}
	}
	updated, err := m.resources.Move(ctx, actor, resourceID, parentID, position, expectedVersion)
	if err != nil {
		return ResourceDTO{}, err
	}
	return resourceDTO(updated), nil
}

func (m *Resources) TransferResourceToSite(
	ctx context.Context,
	actor security.Actor,
	sourceSiteID site.ID,
	resourceID resource.ID,
	targetSiteID site.ID,
	expectedVersion int64,
) (ResourceDTO, error) {
	if err := m.requireSite(ctx, actor, sourceSiteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceDTO{}, err
	}
	if err := m.requireSite(ctx, actor, targetSiteID, ResourceUpdatePermission, SiteAccessEdit); err != nil {
		return ResourceDTO{}, err
	}
	current, err := m.resources.Get(ctx, actor, resourceID)
	if err != nil {
		return ResourceDTO{}, err
	}
	if current.SiteID != sourceSiteID {
		return ResourceDTO{}, resource.ErrNotFound
	}
	result, err := m.resources.TransferToSite(
		ctx, actor, resourceID, targetSiteID, expectedVersion, validateTransferExtensions,
	)
	if err != nil {
		return ResourceDTO{}, err
	}
	return resourceDTO(result.Resource), nil
}

func validateTransferExtensions(
	ctx context.Context,
	items []resource.Resource,
	sourceRuntime *site.Runtime,
	targetRuntime *site.Runtime,
) error {
	targetEditors := make(map[resourceextension.Code]resourceextension.Editor)
	for _, moduleRuntime := range targetRuntime.Profile().Modules() {
		provider, ok := moduleRuntime.(resourceextension.EditorProvider)
		if !ok {
			continue
		}
		editor := provider.ResourceEditorExtension()
		if editor != nil {
			targetEditors[editor.Metadata().Code] = editor
		}
	}
	for _, moduleRuntime := range sourceRuntime.Profile().Modules() {
		provider, ok := moduleRuntime.(resourceextension.EditorProvider)
		if !ok {
			continue
		}
		editor := provider.ResourceEditorExtension()
		if editor == nil {
			continue
		}
		applicableIDs := make([]resource.ID, 0, len(items))
		for _, item := range items {
			if editor.AppliesTo(item.Type) {
				applicableIDs = append(applicableIDs, item.ID)
			}
		}
		if len(applicableIDs) == 0 {
			continue
		}
		if usage, supportsUsage := editor.(resourceextension.TransferUsage); supportsUsage {
			used, err := usage.UsedByResources(ctx, sourceRuntime.Site().ID, applicableIDs)
			if err != nil {
				return fmt.Errorf("query resource extension %q usage: %w", editor.Metadata().Code, err)
			}
			if !used {
				continue
			}
		}
		target, exists := targetEditors[editor.Metadata().Code]
		if !exists || !reflect.DeepEqual(editor.Metadata(), target.Metadata()) {
			return fmt.Errorf("%w: resource extension %q is unavailable or incompatible", resource.ErrIncompatibleTargetSite, editor.Metadata().Code)
		}
		for _, item := range items {
			if editor.AppliesTo(item.Type) && !target.AppliesTo(item.Type) {
				return fmt.Errorf("%w: resource extension %q does not support type %q", resource.ErrIncompatibleTargetSite, editor.Metadata().Code, item.Type)
			}
		}
	}
	return nil
}

func (m *Resources) DeleteResource(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	permanent bool,
) error {
	if err := m.requireSite(ctx, actor, siteID, ResourceDeletePermission, SiteAccessEdit); err != nil {
		return err
	}
	current, err := m.resources.Get(ctx, actor, resourceID)
	if err != nil {
		return err
	}
	if current.SiteID != siteID {
		return resource.ErrNotFound
	}
	if permanent {
		return m.resources.DeletePermanent(ctx, actor, resourceID)
	}
	return m.resources.Delete(ctx, actor, resourceID)
}

func (m *Resources) RestoreResource(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	resourceID resource.ID,
	withDescendants bool,
) error {
	if err := m.requireSite(ctx, actor, siteID, ResourceDeletePermission, SiteAccessEdit); err != nil {
		return err
	}
	current, err := m.resources.Get(ctx, actor, resourceID)
	if err != nil {
		return err
	}
	if current.SiteID != siteID {
		return resource.ErrNotFound
	}
	return m.resources.Restore(ctx, actor, resourceID, withDescendants)
}
