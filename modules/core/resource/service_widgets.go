package resource

import (
	"context"
	"errors"
	"fmt"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

func (s *Service) CreateWidget(
	ctx context.Context,
	actor security.Actor,
	resourceID ID,
	input CreateWidgetInput,
) (widget.Binding, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationWidgetCreate)
	if hookErr != nil {
		return widget.Binding{}, hookErr
	}
	defer finishHooks()

	current, profileRuntime, templateRuntime, recordRevision, err := s.widgetMutationContext(ctx, actor, resourceID)
	if err != nil {
		return widget.Binding{}, err
	}
	presentation := widget.Presentation{
		View: widget.NormalizeView(input.View), Columns: input.Columns,
		MarginTop: input.MarginTop, MarginBottom: input.MarginBottom,
		Enabled: input.Enabled == nil || *input.Enabled,
	}
	runtime, exists := profileRuntime.Widget(input.Code)
	if !exists {
		return widget.Binding{}, fmt.Errorf("%w: widget %q is unavailable", ErrInvalid, input.Code)
	}
	if !templateRuntime.AllowsResourceArea(input.Area) {
		return widget.Binding{}, fmt.Errorf("%w: template does not support widget area %q", ErrInvalid, input.Area)
	}
	if err := runtime.ValidatePresentation(presentation); err != nil {
		return widget.Binding{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	params, err := runtime.NormalizeConfiguration(input.Params, input.ParamBindings, templateRuntime.FieldSchema())
	if err != nil {
		return widget.Binding{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := validateLiteralWidgetInstance(runtime, params, input.ParamBindings); err != nil {
		return widget.Binding{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	position := 0
	for _, binding := range current.Widgets {
		if binding.Area == input.Area {
			position++
		}
	}
	if input.ExpectedVersion != current.Version {
		return widget.Binding{}, ErrConflict
	}
	created, err := s.widgets.CreateWidget(ctx, actor.AuditUserID(), resourceID, input.ExpectedVersion, widget.Binding{
		Code: input.Code, Area: input.Area, Position: position,
		Presentation: presentation, Params: params, ParamBindings: widget.CloneParamBindings(input.ParamBindings),
	}, recordRevision)
	if err != nil {
		return widget.Binding{}, fmt.Errorf("create resource %d widget: %w", resourceID, err)
	}
	return widget.CloneBinding(created), nil
}

func (s *Service) UpdateWidget(
	ctx context.Context,
	actor security.Actor,
	resourceID ID,
	bindingID widget.BindingID,
	input UpdateWidgetInput,
) (widget.Binding, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationWidgetUpdate)
	if hookErr != nil {
		return widget.Binding{}, hookErr
	}
	defer finishHooks()

	current, profileRuntime, templateRuntime, recordRevision, err := s.widgetMutationContext(ctx, actor, resourceID)
	if err != nil {
		return widget.Binding{}, err
	}
	binding, exists := findWidget(current.Widgets, bindingID)
	if !exists {
		return widget.Binding{}, ErrNotFound
	}
	runtime, exists := profileRuntime.Widget(binding.Code)
	if !exists {
		return widget.Binding{}, fmt.Errorf("%w: widget %q is unavailable", ErrInvalid, binding.Code)
	}
	binding.Presentation = widget.Presentation{
		View: widget.NormalizeView(input.View), Columns: input.Columns,
		MarginTop: input.MarginTop, MarginBottom: input.MarginBottom,
		Enabled: input.Enabled == nil || *input.Enabled,
	}
	if err := runtime.ValidatePresentation(binding.Presentation); err != nil {
		return widget.Binding{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	binding.ParamBindings = widget.CloneParamBindings(input.ParamBindings)
	binding.Params, err = runtime.NormalizeConfiguration(input.Params, input.ParamBindings, templateRuntime.FieldSchema())
	if err != nil {
		return widget.Binding{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := validateLiteralWidgetInstance(runtime, binding.Params, binding.ParamBindings); err != nil {
		return widget.Binding{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if input.ExpectedVersion != current.Version {
		return widget.Binding{}, ErrConflict
	}
	updated, err := s.widgets.UpdateWidget(ctx, actor.AuditUserID(), resourceID, input.ExpectedVersion, binding, recordRevision)
	if err != nil {
		return widget.Binding{}, fmt.Errorf("update resource %d widget %d: %w", resourceID, bindingID, err)
	}
	return widget.CloneBinding(updated), nil
}

func (s *Service) DeleteWidget(
	ctx context.Context,
	actor security.Actor,
	resourceID ID,
	bindingID widget.BindingID,
	expectedVersion int64,
) error {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationWidgetDelete)
	if hookErr != nil {
		return hookErr
	}
	defer finishHooks()

	current, _, _, recordRevision, err := s.widgetMutationContext(ctx, actor, resourceID)
	if err != nil {
		return err
	}
	if _, exists := findWidget(current.Widgets, bindingID); !exists {
		return ErrNotFound
	}
	if expectedVersion != current.Version {
		return ErrConflict
	}
	if err := s.widgets.DeleteWidget(ctx, actor.AuditUserID(), resourceID, expectedVersion, bindingID, recordRevision); err != nil {
		return fmt.Errorf("delete resource %d widget %d: %w", resourceID, bindingID, err)
	}
	return nil
}

func (s *Service) ReorderWidgets(
	ctx context.Context,
	actor security.Actor,
	resourceID ID,
	expectedVersion int64,
	order []widget.Order,
) ([]widget.Binding, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationWidgetReorder)
	if hookErr != nil {
		return nil, hookErr
	}
	defer finishHooks()

	current, _, templateRuntime, recordRevision, err := s.widgetMutationContext(ctx, actor, resourceID)
	if err != nil {
		return nil, err
	}
	if len(order) != len(current.Widgets) {
		return nil, fmt.Errorf("%w: widget order must contain every binding", ErrInvalid)
	}
	if expectedVersion != current.Version {
		return nil, ErrConflict
	}
	known := make(map[widget.BindingID]widget.AreaCode, len(current.Widgets))
	for _, binding := range current.Widgets {
		known[binding.ID] = binding.Area
	}
	positions := make(map[widget.AreaCode]int)
	seen := make(map[widget.BindingID]struct{}, len(order))
	for _, item := range order {
		previousArea, exists := known[item.ID]
		if !exists {
			return nil, fmt.Errorf("%w: unknown widget binding %d", ErrInvalid, item.ID)
		}
		if _, exists := seen[item.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate widget binding %d", ErrInvalid, item.ID)
		}
		seen[item.ID] = struct{}{}
		if !widget.ValidArea(item.Area) || (item.Area != previousArea && !templateRuntime.AllowsResourceArea(item.Area)) {
			return nil, fmt.Errorf("%w: template does not support widget area %q", ErrInvalid, item.Area)
		}
		if item.Position != positions[item.Area] {
			return nil, fmt.Errorf("%w: widget %d in %q has position %d instead of %d", ErrInvalid, item.ID, item.Area, item.Position, positions[item.Area])
		}
		positions[item.Area]++
	}
	updated, err := s.widgets.ReorderWidgets(ctx, actor.AuditUserID(), resourceID, expectedVersion, order, recordRevision)
	if err != nil {
		return nil, fmt.Errorf("reorder resource %d widgets: %w", resourceID, err)
	}
	return widget.CloneBindings(updated), nil
}

func (s *Service) widgetMutationContext(
	ctx context.Context,
	actor security.Actor,
	resourceID ID,
) (Resource, interface {
	Widget(widget.Code) (*widget.Runtime, bool)
}, interface {
	AllowsResourceArea(widget.AreaCode) bool
	FieldSchema() *field.Schema
}, bool, error) {
	if err := validateContext(ctx, "resource widget mutation"); err != nil {
		return Resource{}, nil, nil, false, err
	}
	if err := s.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return Resource{}, nil, nil, false, err
	}
	if resourceID <= 0 {
		return Resource{}, nil, nil, false, errors.New("resource id is invalid")
	}
	stored, err := s.repository.ByID(ctx, resourceID)
	libraryItemProjection := false
	if errors.Is(err, ErrNotFound) {
		libraryRepository, ok := s.repository.(LibraryItemRepository)
		if !ok {
			return Resource{}, nil, nil, false, fmt.Errorf("get resource %d for widget mutation: %w", resourceID, err)
		}
		libraryItem, itemErr := libraryRepository.LibraryItemByID(ctx, resourceID)
		if itemErr != nil {
			return Resource{}, nil, nil, false, fmt.Errorf("get resource %d for widget mutation: %w", resourceID, itemErr)
		}
		stored = Resource{
			ID: libraryItem.ID, SiteID: libraryItem.SiteID, Version: libraryItem.Version, Type: resourcetype.Page,
			Template: libraryItem.Template, ContentType: libraryItem.ContentType,
			Title: libraryItem.Title, Slug: libraryItem.Slug,
			Annotation: libraryItem.Annotation, Content: libraryItem.Content,
			ImageMediaID: libraryItem.ImageMediaID, IsPublic: libraryItem.IsPublic,
			IsSearchable: libraryItem.IsSearchable, PublishedAt: libraryItem.PublishedAt,
			UnpublishedAt: libraryItem.UnpublishedAt, Fields: libraryItem.Fields,
			FieldValues: libraryItem.FieldValues, Widgets: libraryItem.Widgets,
			CreatedAt: libraryItem.CreatedAt, UpdatedAt: libraryItem.UpdatedAt,
			CreatedBy: libraryItem.CreatedBy, UpdatedBy: libraryItem.UpdatedBy,
			DeletedAt: libraryItem.DeletedAt, DeletedBy: libraryItem.DeletedBy,
		}
		libraryItemProjection = true
		err = nil
	}
	if err != nil {
		return Resource{}, nil, nil, false, fmt.Errorf("get resource %d for widget mutation: %w", resourceID, err)
	}
	current := stored
	if !libraryItemProjection {
		current, err = s.validateStored(ctx, stored)
		if err != nil {
			return Resource{}, nil, nil, false, err
		}
	}
	siteRuntime, exists := s.runtime(ctx, current.SiteID)
	if !exists || current.Template == nil {
		return Resource{}, nil, nil, false, fmt.Errorf("%w: resource template does not support widgets", ErrInvalid)
	}
	templateRuntime, exists := siteRuntime.Profile().Template(*current.Template)
	if !exists || !templateRuntime.SupportsResourceWidgets() {
		return Resource{}, nil, nil, false, fmt.Errorf("%w: resource template does not support widgets", ErrInvalid)
	}
	recordRevision := true
	if libraryItemProjection {
		recordRevision = revisionPolicyFor(siteRuntime).LibraryItems
	}
	return current, siteRuntime.Profile(), templateRuntime, recordRevision, nil
}

func findWidget(bindings []widget.Binding, id widget.BindingID) (widget.Binding, bool) {
	for _, binding := range bindings {
		if binding.ID == id {
			return widget.CloneBinding(binding), true
		}
	}
	return widget.Binding{}, false
}

func validateLiteralWidgetInstance(runtime *widget.Runtime, params map[string]any, bindings widget.ParamBindings) error {
	if len(bindings) != 0 {
		return nil
	}
	_, err := runtime.New(params)
	return err
}
