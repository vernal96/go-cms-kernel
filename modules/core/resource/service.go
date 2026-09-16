package resource

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

var (
	errPersistence = errors.New("resource persistence failed")

	readPermission = permission.MustCode(
		"core",
		"resource",
		permission.Read,
	)
	createPermission = permission.MustCode(
		"core",
		"resource",
		permission.Create,
	)
	updatePermission = permission.MustCode(
		"core",
		"resource",
		permission.Update,
	)
	deletePermission = permission.MustCode(
		"core",
		"resource",
		permission.Delete,
	)
)

type SiteResolver interface {
	RuntimeByID(site.ID) (*site.Runtime, bool)
}

type Service struct {
	repository Repository
	widgets    WidgetRepository
	sites      SiteResolver
	media      media.Service
	authorizer security.Authorizer
	files      file.Service
}

func NewService(
	repository Repository,
	sites SiteResolver,
	mediaService media.Service,
	authorizer security.Authorizer,
	fileServices ...file.Service,
) (*Service, error) {
	if repository == nil {
		return nil, errors.New("resource repository is nil")
	}
	if sites == nil {
		return nil, errors.New("resource site resolver is nil")
	}
	if mediaService == nil {
		return nil, errors.New("resource media service is nil")
	}
	if authorizer == nil {
		return nil, errors.New("resource authorizer is nil")
	}
	widgets, ok := repository.(WidgetRepository)
	if !ok {
		return nil, errors.New("resource widget repository is unavailable")
	}

	result := &Service{
		repository: repository,
		widgets:    widgets,
		sites:      sites,
		media:      mediaService,
		authorizer: authorizer,
	}
	if len(fileServices) > 0 {
		result.files = fileServices[0]
	}
	return result, nil
}

func (s *Service) Create(
	ctx context.Context,
	actor security.Actor,
	input CreateInput,
) (Resource, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationCreate)
	if hookErr != nil {
		return Resource{}, hookErr
	}
	defer finishHooks()

	if err := validateContext(ctx, "resource create"); err != nil {
		return Resource{}, err
	}
	if err := s.authorizer.Check(ctx, actor, createPermission); err != nil {
		return Resource{}, err
	}
	if input.SiteID <= 0 {
		return Resource{}, errors.New("resource site id is invalid")
	}

	siteRuntime, exists := s.runtime(ctx, input.SiteID)
	if !exists {
		return Resource{}, fmt.Errorf(
			"resource site %d not found",
			input.SiteID,
		)
	}

	resourceType := input.Type
	if resourceType == "" {
		resourceType = resourcetype.Page
	}

	item := Resource{
		SiteID:           input.SiteID,
		ParentID:         cloneID(input.ParentID),
		Type:             resourceType,
		Template:         cloneTemplateCode(input.Template),
		ContentType:      cloneString(input.ContentType),
		Title:            input.Title,
		MenuTitle:        input.MenuTitle,
		Slug:             input.Slug,
		Annotation:       input.Annotation,
		Content:          input.Content,
		ImageMediaID:     cloneMediaID(input.ImageMediaID),
		TargetResourceID: cloneID(input.TargetResourceID),
		ExternalURL:      cloneString(input.ExternalURL),
		IsPublic:         boolDefault(input.IsPublic, true),
		IsSearchable:     boolDefault(input.IsSearchable, true),
		InMenu:           boolDefault(input.InMenu, true),
		InSitemap:        boolDefault(input.InSitemap, true),
		Sort:             input.Sort,
		PublishedAt:      cloneTime(input.PublishedAt),
		UnpublishedAt:    cloneTime(input.UnpublishedAt),
		Fields:           cloneMap(input.Fields),
		TypeSettings:     cloneMap(input.TypeSettings),
		CreatedBy:        actor.AuditUserID(),
		UpdatedBy:        actor.AuditUserID(),
	}
	if item.Slug == "" {
		if item.ParentID != nil {
			item.Slug = GenerateSlug(item.Title)
		} else if _, lookupErr := s.repository.ByPath(ctx, item.SiteID, "/"); lookupErr == nil {
			item.Slug = GenerateSlug(item.Title)
		} else if !errors.Is(lookupErr, ErrNotFound) {
			return Resource{}, fmt.Errorf("check main resource: %w", lookupErr)
		}
	}

	normalized, err := s.normalize(
		ctx,
		actor,
		item,
		siteRuntime,
		nil,
		nil,
	)
	if err != nil {
		if errors.Is(err, errPersistence) {
			return Resource{}, err
		}
		return Resource{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	created, err := s.repository.Create(
		ctx,
		actor.AuditUserID(),
		normalized,
		s.validateImageMedia,
	)
	if err != nil {
		return Resource{}, fmt.Errorf("create resource: %w", err)
	}

	return s.validateStored(ctx, created)
}

func (s *Service) Get(
	ctx context.Context,
	actor security.Actor,
	id ID,
) (Resource, error) {
	if err := validateContext(ctx, "resource get"); err != nil {
		return Resource{}, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return Resource{}, err
	}
	if id <= 0 {
		return Resource{}, errors.New("resource id is invalid")
	}

	item, err := s.repository.ByID(ctx, id)
	if err != nil {
		return Resource{}, fmt.Errorf("get resource %d: %w", id, err)
	}

	return s.validateStored(ctx, item)
}

func (s *Service) GetByPath(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	path string,
) (Resource, error) {
	if err := validateContext(ctx, "resource get by path"); err != nil {
		return Resource{}, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return Resource{}, err
	}
	if siteID <= 0 {
		return Resource{}, errors.New("resource site id is invalid")
	}
	if !validLookupPath(path) {
		return Resource{}, fmt.Errorf(
			"resource path %q is invalid",
			path,
		)
	}

	item, err := s.repository.ByPath(ctx, siteID, path)
	if err != nil {
		return Resource{}, fmt.Errorf(
			"get resource by path %q: %w",
			path,
			err,
		)
	}

	return s.validateStored(ctx, item)
}

func (s *Service) Update(
	ctx context.Context,
	actor security.Actor,
	input UpdateInput,
) (Resource, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationUpdate)
	if hookErr != nil {
		return Resource{}, hookErr
	}
	defer finishHooks()

	if err := validateContext(ctx, "resource update"); err != nil {
		return Resource{}, err
	}
	if err := s.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return Resource{}, err
	}
	if input.ID <= 0 {
		return Resource{}, errors.New("resource id is invalid")
	}
	if input.ExpectedVersion <= 0 {
		return Resource{}, fmt.Errorf("%w: expected resource version is required", ErrInvalid)
	}
	if input.Type == "" {
		return Resource{}, errors.New("resource type is empty")
	}

	current, err := s.repository.ByID(ctx, input.ID)
	if err != nil {
		return Resource{}, fmt.Errorf(
			"get resource %d for update: %w",
			input.ID,
			err,
		)
	}
	if current.Version != input.ExpectedVersion {
		return Resource{}, ErrConflict
	}
	current, err = s.validateStored(ctx, current)
	if err != nil {
		return Resource{}, fmt.Errorf(
			"validate resource %d for update: %w",
			input.ID,
			err,
		)
	}
	siteRuntime, exists := s.runtime(ctx, current.SiteID)
	if !exists {
		return Resource{}, fmt.Errorf(
			"resource site %d not found",
			current.SiteID,
		)
	}
	currentType, exists := siteRuntime.Profile().Registry().ResourceType(current.Type)
	if !exists {
		return Resource{}, fmt.Errorf("resource references unknown current type %q", current.Type)
	}
	if current.Type != input.Type && (!currentType.Metadata().Capabilities.MutableType || input.Type == resourcetype.Library) {
		return Resource{}, fmt.Errorf("%w: resource type %q is immutable", ErrInvalid, current.Type)
	}

	item := Resource{
		ID:               current.ID,
		SiteID:           current.SiteID,
		Version:          current.Version,
		ParentID:         cloneID(input.ParentID),
		Type:             input.Type,
		Template:         cloneTemplateCode(input.Template),
		ContentType:      cloneString(input.ContentType),
		Title:            input.Title,
		MenuTitle:        input.MenuTitle,
		Slug:             input.Slug,
		Annotation:       input.Annotation,
		Content:          input.Content,
		ImageMediaID:     cloneMediaID(input.ImageMediaID),
		TargetResourceID: cloneID(input.TargetResourceID),
		ExternalURL:      cloneString(input.ExternalURL),
		IsPublic:         input.IsPublic,
		IsSearchable:     input.IsSearchable,
		InMenu:           input.InMenu,
		InSitemap:        input.InSitemap,
		Sort:             input.Sort,
		PublishedAt:      cloneTime(input.PublishedAt),
		UnpublishedAt:    cloneTime(input.UnpublishedAt),
		Fields:           cloneMap(input.Fields),
		TypeSettings:     cloneMap(input.TypeSettings),
		Widgets:          widget.CloneBindings(current.Widgets),
		CreatedAt:        current.CreatedAt,
		UpdatedAt:        current.UpdatedAt,
		CreatedBy:        cloneUserID(current.CreatedBy),
		UpdatedBy:        actor.AuditUserID(),
		DeletedAt:        cloneTime(current.DeletedAt),
		DeletedBy:        cloneUserID(current.DeletedBy),
	}
	if item.Slug == "" && !(item.ParentID == nil && current.ParentID == nil && current.Path != nil && *current.Path == "/") {
		item.Slug = GenerateSlug(item.Title)
	}

	if err := s.ensureNoParentCycle(ctx, item); err != nil {
		return Resource{}, err
	}

	normalized, err := s.normalize(
		ctx,
		actor,
		item,
		siteRuntime,
		nil,
		current.FileReferences,
	)
	if err != nil {
		if errors.Is(err, errPersistence) {
			return Resource{}, err
		}
		return Resource{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if current.Path != nil && normalized.Path == nil {
		if err := s.ensureNoRouteDescendants(
			ctx,
			current,
			siteRuntime,
		); err != nil {
			return Resource{}, err
		}
	}

	updated, err := s.repository.Update(
		ctx,
		actor.AuditUserID(),
		current,
		normalized,
		s.validateImageMedia,
	)
	if err != nil {
		return Resource{}, fmt.Errorf(
			"update resource %d: %w",
			input.ID,
			err,
		)
	}

	return s.validateStored(ctx, updated)
}

func (s *Service) Delete(
	ctx context.Context,
	actor security.Actor,
	id ID,
) error {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationTrash)
	if hookErr != nil {
		return hookErr
	}
	defer finishHooks()

	if err := validateContext(ctx, "resource delete"); err != nil {
		return err
	}
	if err := s.authorizer.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("resource id is invalid")
	}

	lifecycle, ok := s.repository.(LifecycleRepository)
	if !ok {
		return errors.New("resource lifecycle repository is unavailable")
	}
	if err := lifecycle.SoftDelete(ctx, actor.AuditUserID(), id); err != nil {
		return fmt.Errorf("delete resource %d: %w", id, err)
	}
	return nil
}

func (s *Service) Restore(ctx context.Context, actor security.Actor, id ID, withDescendants bool) error {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationRestore)
	if hookErr != nil {
		return hookErr
	}
	defer finishHooks()

	if err := validateContext(ctx, "resource restore"); err != nil {
		return err
	}
	if err := s.authorizer.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("resource id is invalid")
	}
	lifecycle, ok := s.repository.(LifecycleRepository)
	if !ok {
		return errors.New("resource lifecycle repository is unavailable")
	}
	if err := lifecycle.Restore(ctx, actor.AuditUserID(), id, withDescendants); err != nil {
		return fmt.Errorf("restore resource %d: %w", id, err)
	}
	return nil
}

func (s *Service) DeletePermanent(ctx context.Context, actor security.Actor, id ID) error {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationDelete)
	if hookErr != nil {
		return hookErr
	}
	defer finishHooks()

	if err := validateContext(ctx, "resource permanent delete"); err != nil {
		return err
	}
	if err := s.authorizer.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("resource id is invalid")
	}
	if err := s.repository.Delete(ctx, id); err != nil {
		return fmt.Errorf("permanently delete resource %d: %w", id, err)
	}
	return nil
}

func (s *Service) Move(ctx context.Context, actor security.Actor, id ID, parentID *ID, position int, expectedVersion int64) (Resource, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationMove)
	if hookErr != nil {
		return Resource{}, hookErr
	}
	defer finishHooks()

	if position < 0 {
		return Resource{}, fmt.Errorf("%w: resource position is invalid", ErrInvalid)
	}
	current, err := s.Get(ctx, actor, id)
	if err != nil {
		return Resource{}, err
	}
	if expectedVersion <= 0 || current.Version != expectedVersion {
		return Resource{}, ErrConflict
	}
	if current.DeletedAt != nil {
		return Resource{}, ErrInvalidTree
	}
	return s.Update(ctx, actor, UpdateInput{
		ID: current.ID, ExpectedVersion: expectedVersion, ParentID: parentID, Type: current.Type, Template: current.Template,
		ContentType: current.ContentType, Title: current.Title, MenuTitle: current.MenuTitle,
		Slug: current.Slug, Annotation: current.Annotation, Content: current.Content,
		ImageMediaID: current.ImageMediaID, TargetResourceID: current.TargetResourceID,
		ExternalURL: current.ExternalURL, IsPublic: current.IsPublic, IsSearchable: current.IsSearchable,
		InMenu: current.InMenu, InSitemap: current.InSitemap, Sort: position,
		PublishedAt: current.PublishedAt, UnpublishedAt: current.UnpublishedAt,
		Fields: current.Fields, TypeSettings: current.TypeSettings,
	})
}

func (s *Service) TransferToSite(
	ctx context.Context,
	actor security.Actor,
	id ID,
	targetSiteID site.ID,
	expectedVersion int64,
	compatibility SiteTransferCompatibility,
) (SiteTransferResult, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationTransfer)
	if hookErr != nil {
		return SiteTransferResult{}, hookErr
	}
	defer finishHooks()

	if err := validateContext(ctx, "resource site transfer"); err != nil {
		return SiteTransferResult{}, err
	}
	if err := s.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return SiteTransferResult{}, err
	}
	if id <= 0 || targetSiteID <= 0 {
		return SiteTransferResult{}, fmt.Errorf("%w: resource or target site id is invalid", ErrInvalid)
	}
	if expectedVersion <= 0 {
		return SiteTransferResult{}, ErrConflict
	}

	current, err := s.repository.ByID(ctx, id)
	if err != nil {
		return SiteTransferResult{}, fmt.Errorf("get resource %d for site transfer: %w", id, err)
	}
	if current.Version != expectedVersion {
		return SiteTransferResult{}, ErrConflict
	}
	if current.SiteID == targetSiteID || current.DeletedAt != nil || current.Path != nil && *current.Path == "/" {
		return SiteTransferResult{}, ErrInvalidTree
	}
	sourceRuntime, exists := s.runtime(ctx, current.SiteID)
	if !exists {
		return SiteTransferResult{}, fmt.Errorf("resource source site %d not found", current.SiteID)
	}
	targetRuntime, exists := s.runtime(ctx, targetSiteID)
	if !exists {
		return SiteTransferResult{}, fmt.Errorf("resource target site %d not found", targetSiteID)
	}

	items, err := s.repository.ListBySite(ctx, current.SiteID)
	if err != nil {
		return SiteTransferResult{}, fmt.Errorf("list resource transfer subtree: %w", err)
	}
	subtree, err := transferSubtree(items, id)
	if err != nil {
		return SiteTransferResult{}, err
	}
	known := make(map[ID]Resource, len(subtree))
	for index, item := range subtree {
		item.SiteID = targetSiteID
		if item.ID == id {
			item.ParentID = nil
		}
		subtree[index] = item
		known[item.ID] = Clone(item)
	}
	for _, item := range subtree {
		if item.TargetResourceID == nil {
			continue
		}
		if _, exists := known[*item.TargetResourceID]; !exists {
			return SiteTransferResult{}, fmt.Errorf(
				"%w: resource %d targets resource %d outside the transferred subtree",
				ErrCrossSiteReference,
				item.ID,
				*item.TargetResourceID,
			)
		}
	}
	for index, item := range subtree {
		normalized, normalizeErr := s.normalize(
			ctx, actor, item, targetRuntime, known, item.FileReferences,
		)
		if normalizeErr != nil {
			return SiteTransferResult{}, fmt.Errorf("%w: resource %d: %v", ErrIncompatibleTargetSite, item.ID, normalizeErr)
		}
		subtree[index] = normalized
		known[normalized.ID] = Clone(normalized)
	}
	if err := s.validateTransferredLibraries(ctx, sourceRuntime, targetRuntime, subtree); err != nil {
		return SiteTransferResult{}, err
	}
	if compatibility != nil {
		if err := compatibility(ctx, subtree, sourceRuntime, targetRuntime); err != nil {
			return SiteTransferResult{}, err
		}
	}

	repository, ok := s.repository.(SiteTransferRepository)
	if !ok {
		return SiteTransferResult{}, errors.New("resource site transfer repository is unavailable")
	}
	result, err := repository.TransferToSite(
		ctx, actor.AuditUserID(), id, current.SiteID, targetSiteID, expectedVersion,
		string(sourceRuntime.Site().ProfileCode), string(targetRuntime.Site().ProfileCode),
	)
	if err != nil {
		return SiteTransferResult{}, fmt.Errorf("transfer resource %d to site %d: %w", id, targetSiteID, err)
	}
	result.Resource, err = s.validateStored(ctx, result.Resource)
	if err != nil {
		return SiteTransferResult{}, fmt.Errorf("validate transferred resource %d: %w", id, err)
	}
	return result, nil
}

func transferSubtree(items []Resource, rootID ID) ([]Resource, error) {
	byParent := make(map[ID][]Resource)
	var root *Resource
	for _, item := range items {
		if item.ID == rootID {
			copy := Clone(item)
			root = &copy
		}
		if item.ParentID != nil {
			byParent[*item.ParentID] = append(byParent[*item.ParentID], Clone(item))
		}
	}
	if root == nil {
		return nil, ErrNotFound
	}
	result := make([]Resource, 0)
	visiting := make(map[ID]bool)
	visited := make(map[ID]bool)
	var appendTree func(Resource) error
	appendTree = func(item Resource) error {
		if visiting[item.ID] || visited[item.ID] {
			return ErrInvalidTree
		}
		visiting[item.ID] = true
		result = append(result, Clone(item))
		children := byParent[item.ID]
		sort.Slice(children, func(left, right int) bool {
			if children[left].Sort != children[right].Sort {
				return children[left].Sort < children[right].Sort
			}
			return children[left].ID < children[right].ID
		})
		for _, child := range children {
			if err := appendTree(child); err != nil {
				return err
			}
		}
		delete(visiting, item.ID)
		visited[item.ID] = true
		return nil
	}
	if err := appendTree(*root); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) validateTransferredLibraries(
	ctx context.Context,
	sourceRuntime *site.Runtime,
	targetRuntime *site.Runtime,
	items []Resource,
) error {
	libraries := make([]ID, 0)
	for _, item := range items {
		if item.Type == resourcetype.Library {
			libraries = append(libraries, item.ID)
		}
	}
	if len(libraries) == 0 {
		return nil
	}
	repository, ok := s.repository.(LibraryItemRepository)
	if !ok {
		return errors.New("resource library item repository is unavailable")
	}
	usedWidgetCodes := make(map[widget.Code]struct{})
	for _, item := range items {
		for _, binding := range item.Widgets {
			usedWidgetCodes[binding.Code] = struct{}{}
		}
	}
	for _, libraryID := range libraries {
		codes, err := repository.LibraryItemTemplateCodes(ctx, sourceRuntime.Site().ID, libraryID)
		if err != nil {
			return fmt.Errorf("list library %d template usage: %w", libraryID, err)
		}
		for _, code := range codes {
			sourceTemplate, sourceExists := sourceRuntime.Profile().Template(code)
			targetTemplate, targetExists := targetRuntime.Profile().Template(code)
			if !sourceExists || !targetExists || !reflect.DeepEqual(sourceTemplate.Definition(), targetTemplate.Definition()) {
				return fmt.Errorf("%w: library template %q is unavailable or incompatible", ErrIncompatibleTargetSite, code)
			}
		}
		widgetCodes, err := repository.LibraryItemWidgetCodes(ctx, sourceRuntime.Site().ID, libraryID)
		if err != nil {
			return fmt.Errorf("list library %d widget usage: %w", libraryID, err)
		}
		for _, code := range widgetCodes {
			usedWidgetCodes[code] = struct{}{}
		}
	}
	sourceWidgets := make(map[widget.Code]widget.Definition)
	for _, definition := range sourceRuntime.Profile().Widgets() {
		sourceWidgets[definition.Code] = definition
	}
	targetWidgets := make(map[widget.Code]widget.Definition)
	for _, definition := range targetRuntime.Profile().Widgets() {
		targetWidgets[definition.Code] = definition
	}
	for code := range usedWidgetCodes {
		source, sourceExists := sourceWidgets[code]
		target, targetExists := targetWidgets[code]
		if !sourceExists || !targetExists || !reflect.DeepEqual(source, target) {
			return fmt.Errorf("%w: widget %q is unavailable or incompatible", ErrIncompatibleTargetSite, code)
		}
	}
	return nil
}

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
	known := make(map[widget.BindingID]struct{}, len(current.Widgets))
	for _, binding := range current.Widgets {
		known[binding.ID] = struct{}{}
	}
	positions := map[widget.AreaCode]int{widget.AreaBody: 0, widget.AreaSidebar: 0}
	seen := make(map[widget.BindingID]struct{}, len(order))
	for _, item := range order {
		if _, exists := known[item.ID]; !exists {
			return nil, fmt.Errorf("%w: unknown widget binding %d", ErrInvalid, item.ID)
		}
		if _, exists := seen[item.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate widget binding %d", ErrInvalid, item.ID)
		}
		seen[item.ID] = struct{}{}
		if !templateRuntime.AllowsResourceArea(item.Area) {
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

func (s *Service) Tree(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
) ([]Node, error) {
	if err := validateContext(ctx, "resource tree"); err != nil {
		return nil, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return nil, err
	}
	if siteID <= 0 {
		return nil, errors.New("resource site id is invalid")
	}

	siteRuntime, exists := s.runtime(ctx, siteID)
	if !exists {
		return nil, fmt.Errorf("resource site %d not found", siteID)
	}

	items, err := s.repository.ListBySite(ctx, siteID)
	if err != nil {
		return nil, fmt.Errorf("list resources for site %d: %w", siteID, err)
	}

	rawByID := make(map[ID]Resource, len(items))
	for index, item := range items {
		if item.ID <= 0 {
			return nil, fmt.Errorf(
				"resource at index %d has invalid id",
				index,
			)
		}
		if item.SiteID != siteID {
			return nil, fmt.Errorf(
				"resource %d belongs to site %d instead of %d",
				item.ID,
				item.SiteID,
				siteID,
			)
		}
		if _, exists := rawByID[item.ID]; exists {
			return nil, fmt.Errorf(
				"duplicate resource id %d",
				item.ID,
			)
		}
		rawByID[item.ID] = item
	}

	normalized := make([]Resource, 0, len(items))
	for _, item := range items {
		storedPath := cloneString(item.Path)
		result, err := s.normalize(
			ctx,
			security.System(),
			item,
			siteRuntime,
			rawByID,
			nil,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"validate stored resource %d: %w",
				item.ID,
				err,
			)
		}
		if !equalStrings(storedPath, result.Path) {
			return nil, fmt.Errorf(
				"validate stored resource %d: stored path is inconsistent",
				item.ID,
			)
		}
		normalized = append(normalized, result)
	}

	return buildTree(normalized)
}

func (s *Service) validateStored(
	ctx context.Context,
	item Resource,
) (Resource, error) {
	if item.ID <= 0 {
		return Resource{}, errors.New("stored resource id is invalid")
	}
	if item.SiteID <= 0 {
		return Resource{}, errors.New("stored resource site id is invalid")
	}

	siteRuntime, exists := s.runtime(ctx, item.SiteID)
	if !exists {
		return Resource{}, fmt.Errorf(
			"stored resource %d references unknown site %d",
			item.ID,
			item.SiteID,
		)
	}

	storedPath := cloneString(item.Path)
	normalized, err := s.normalize(ctx, security.System(), item, siteRuntime, nil, nil)
	if err != nil {
		return Resource{}, err
	}
	if !equalStrings(storedPath, normalized.Path) {
		return Resource{}, fmt.Errorf(
			"stored resource %d path is inconsistent",
			item.ID,
		)
	}
	return normalized, nil
}

func (s *Service) normalize(
	ctx context.Context,
	actor security.Actor,
	item Resource,
	siteRuntime *site.Runtime,
	known map[ID]Resource,
	trustedFileReferences map[string]file.ID,
) (Resource, error) {
	item = Clone(item)
	item.Title = strings.TrimSpace(item.Title)
	item.MenuTitle = strings.TrimSpace(item.MenuTitle)

	if item.Title == "" {
		return Resource{}, errors.New("resource title is empty")
	}
	if !validSlug(item.Slug, item.ParentID) {
		return Resource{}, fmt.Errorf(
			"resource slug %q is invalid",
			item.Slug,
		)
	}
	if item.PublishedAt != nil &&
		item.UnpublishedAt != nil &&
		!item.UnpublishedAt.After(*item.PublishedAt) {
		return Resource{}, errors.New(
			"resource unpublished_at must be after published_at",
		)
	}
	if item.ImageMediaID != nil {
		if err := s.validateImageMedia(
			ctx,
			*item.ImageMediaID,
		); err != nil {
			return Resource{}, err
		}
	}

	profileRuntime := siteRuntime.Profile()

	resourceType, exists := profileRuntime.Registry().ResourceType(
		item.Type,
	)
	if !exists {
		return Resource{}, fmt.Errorf(
			"resource references unknown type %q",
			item.Type,
		)
	}
	parent, err := s.relatedResource(
		ctx,
		item.ParentID,
		item.SiteID,
		known,
		"parent",
	)
	if err != nil {
		return Resource{}, err
	}
	if parent != nil && parent.DeletedAt != nil && item.DeletedAt == nil {
		return Resource{}, ErrInvalidTree
	}

	payload := resourcetype.Payload{
		Template:         cloneTemplateCode(item.Template),
		ContentType:      cloneString(item.ContentType),
		Content:          item.Content,
		TargetResourceID: resourceTypeID(item.TargetResourceID),
		ExternalURL:      cloneString(item.ExternalURL),
		Fields:           cloneMap(item.Fields),
		TypeSettings:     cloneMap(item.TypeSettings),
	}
	payload, err = resourceType.Normalize(payload)
	if err != nil {
		return Resource{}, fmt.Errorf(
			"normalize resource type %q: %w",
			item.Type,
			err,
		)
	}
	if item.Type == resourcetype.Library {
		if value, ok := payload.TypeSettings["default_item_template"]; ok {
			code, ok := value.(string)
			if !ok || code == "" {
				return Resource{}, errors.New("library default item template is invalid")
			}
			if _, exists := profileRuntime.Template(template.Code(code)); !exists {
				return Resource{}, fmt.Errorf("library references unknown default item template %q", code)
			}
		}
	}

	if payload.TargetResourceID != nil {
		targetID := ID(*payload.TargetResourceID)
		if targetID == item.ID && item.ID != 0 {
			return Resource{}, errors.New(
				"resource cannot target itself",
			)
		}
		if _, err := s.relatedResource(
			ctx,
			&targetID,
			item.SiteID,
			known,
			"target",
		); err != nil {
			return Resource{}, err
		}
	}

	if payload.Template == nil {
		if len(item.Widgets) != 0 {
			return Resource{}, errors.New("resource without template has widgets")
		}
		if len(payload.Fields) != 0 {
			return Resource{}, errors.New(
				"resource without template has fields",
			)
		}
		payload.Fields = map[string]any{}
	} else {
		templateRuntime, exists := profileRuntime.Template(
			*payload.Template,
		)
		if !exists {
			return Resource{}, fmt.Errorf(
				"resource references unknown template %q",
				*payload.Template,
			)
		}
		widgets, err := normalizeWidgetBindings(profileRuntime, templateRuntime, item.Widgets)
		if err != nil {
			return Resource{}, err
		}
		item.Widgets = widgets

		fields, err := templateRuntime.FieldSchema().Validate(
			payload.Fields,
		)
		if err != nil {
			return Resource{}, fmt.Errorf(
				"validate resource template %q fields: %w",
				*payload.Template,
				err,
			)
		}
		payload.Fields = fields
		item.FieldValues, err = templateRuntime.FieldSchema().StoredValues(fields)
		if err != nil {
			return Resource{}, fmt.Errorf("encode resource template %q fields: %w", *payload.Template, err)
		}
		if err := s.validateMediaFields(ctx, actor, item.FieldValues); err != nil {
			return Resource{}, err
		}
		fileReferences, err := templateRuntime.FieldSchema().FileReferences(fields)
		if err != nil {
			return Resource{}, fmt.Errorf("collect resource file references: %w", err)
		}
		if err := s.validateFileReferences(ctx, actor, fileReferences, trustedFileReferences); err != nil {
			return Resource{}, err
		}
		item.FileReferences = resourceFileReferenceMap(fileReferences)
	}

	switch resourceType.PathMode() {
	case resourcetype.PathRoute:
		item.Path, err = BuildPath(parent, item.Slug)
		if err != nil {
			return Resource{}, err
		}
	case resourcetype.PathNone:
		item.Path = nil
	default:
		return Resource{}, fmt.Errorf(
			"resource type %q has invalid path mode %q",
			item.Type,
			resourceType.PathMode(),
		)
	}

	item.Template = cloneTemplateCode(payload.Template)
	item.ContentType = cloneString(payload.ContentType)
	item.Content = payload.Content
	item.TargetResourceID = resourceID(payload.TargetResourceID)
	item.ExternalURL = cloneString(payload.ExternalURL)
	item.Fields = cloneMap(payload.Fields)
	item.TypeSettings = cloneMap(payload.TypeSettings)
	return item, nil
}

func (s *Service) validateFileReferences(ctx context.Context, actor security.Actor, references []field.FileReference, trusted map[string]file.ID) error {
	if len(references) == 0 {
		return nil
	}
	if s.files == nil {
		return errors.New("resource file service is unavailable")
	}
	for _, reference := range references {
		if trusted[reference.Key] == file.ID(reference.ID) {
			continue
		}
		item, err := s.files.GetFile(ctx, actor, file.ID(reference.ID))
		if err != nil {
			return fmt.Errorf("file field %q: %w", reference.Key, err)
		}
		if !field.FileMatches(reference.Options, item.Storage, item.MIMEType) {
			return fmt.Errorf("file field %q rejects selected file", reference.Key)
		}
	}
	return nil
}

func resourceFileReferenceMap(references []field.FileReference) map[string]file.ID {
	if len(references) == 0 {
		return nil
	}
	result := make(map[string]file.ID, len(references))
	for _, reference := range references {
		result[reference.Key] = file.ID(reference.ID)
	}
	return result
}

func normalizeWidgetBindings(
	profileRuntime interface {
		Widget(widget.Code) (*widget.Runtime, bool)
	},
	templateRuntime interface {
		AllowsResourceArea(widget.AreaCode) bool
		FieldSchema() *field.Schema
	},
	source []widget.Binding,
) ([]widget.Binding, error) {
	if profileRuntime == nil {
		return nil, errors.New("resource widget profile runtime is nil")
	}
	if templateRuntime == nil {
		return nil, errors.New("resource widget template runtime is nil")
	}
	if source == nil {
		return nil, nil
	}

	result := make([]widget.Binding, len(source))
	positions := map[widget.AreaCode]int{widget.AreaBody: 0, widget.AreaSidebar: 0}
	ids := make(map[widget.BindingID]struct{}, len(source))
	for index, binding := range source {
		if binding.ID <= 0 {
			return nil, fmt.Errorf("resource widget at index %d has invalid id %d", index, binding.ID)
		}
		if _, exists := ids[binding.ID]; exists {
			return nil, fmt.Errorf("resource widget id %d is duplicated", binding.ID)
		}
		ids[binding.ID] = struct{}{}
		if !widget.ValidArea(binding.Area) || !templateRuntime.AllowsResourceArea(binding.Area) {
			return nil, fmt.Errorf("resource widget %d uses unsupported area %q", binding.ID, binding.Area)
		}
		if binding.Position != positions[binding.Area] {
			return nil, fmt.Errorf("resource widget %d in %q has position %d instead of %d", binding.ID, binding.Area, binding.Position, positions[binding.Area])
		}
		positions[binding.Area]++

		runtime, exists := profileRuntime.Widget(binding.Code)
		if !exists {
			return nil, fmt.Errorf(
				"resource references unknown widget %q",
				binding.Code,
			)
		}
		presentation := binding.Presentation
		presentation.View = widget.NormalizeView(presentation.View)
		if err := runtime.ValidatePresentation(presentation); err != nil {
			return nil, fmt.Errorf("validate resource widget %q presentation: %w", binding.Code, err)
		}
		params, err := runtime.NormalizeConfiguration(binding.Params, binding.ParamBindings, templateRuntime.FieldSchema())
		if err != nil {
			return nil, fmt.Errorf(
				"validate resource widget %q params: %w",
				binding.Code,
				err,
			)
		}

		result[index] = widget.CloneBinding(binding)
		result[index].Presentation = presentation
		result[index].Params = params
	}
	return result, nil
}

func (s *Service) validateImageMedia(
	ctx context.Context,
	id media.ID,
) error {
	if id <= 0 {
		return fmt.Errorf(
			"%w: resource image media id is invalid",
			ErrInvalidReference,
		)
	}

	resolved, err := s.media.Resolve(
		ctx,
		security.System(),
		id,
	)
	if err != nil {
		return fmt.Errorf(
			"%w: resolve resource image media %d: %v",
			ErrInvalidReference,
			id,
			err,
		)
	}
	return ValidateImageMediaFile(
		ctx,
		resolved.File,
		media.Usage{
			Kind: ImageMediaUsage,
		},
	)
}

func ValidateImageMediaFile(
	ctx context.Context,
	linkedFile file.File,
	_ media.Usage,
) error {
	if err := validateContext(ctx, "validate resource image media"); err != nil {
		return err
	}
	if !strings.HasPrefix(
		strings.ToLower(linkedFile.MIMEType),
		"image/",
	) {
		return fmt.Errorf(
			"%w: file %d has MIME type %q instead of image/*",
			ErrInvalidReference,
			linkedFile.ID,
			linkedFile.MIMEType,
		)
	}
	return nil
}

func (s *Service) relatedResource(
	ctx context.Context,
	id *ID,
	siteID site.ID,
	known map[ID]Resource,
	role string,
) (*Resource, error) {
	if id == nil {
		return nil, nil
	}
	if *id <= 0 {
		return nil, fmt.Errorf("resource %s id is invalid", role)
	}

	var (
		item Resource
		err  error
	)
	if known != nil {
		var exists bool
		item, exists = known[*id]
		if !exists {
			return nil, fmt.Errorf(
				"resource %s %d not found",
				role,
				*id,
			)
		}
	} else {
		item, err = s.repository.ByID(ctx, *id)
		if err != nil {
			return nil, fmt.Errorf(
				"%w: get resource %s %d: %w",
				errPersistence,
				role,
				*id,
				err,
			)
		}
	}
	if item.SiteID != siteID {
		return nil, fmt.Errorf(
			"resource %s %d belongs to another site",
			role,
			*id,
		)
	}

	item = Clone(item)
	return &item, nil
}

func (s *Service) ensureNoParentCycle(
	ctx context.Context,
	item Resource,
) error {
	if item.ParentID == nil {
		return nil
	}

	visited := map[ID]struct{}{item.ID: {}}
	currentID := cloneID(item.ParentID)
	for currentID != nil {
		if _, exists := visited[*currentID]; exists {
			return ErrInvalidTree
		}
		visited[*currentID] = struct{}{}

		current, err := s.repository.ByID(ctx, *currentID)
		if err != nil {
			return fmt.Errorf(
				"walk resource parent %d: %w",
				*currentID,
				err,
			)
		}
		if current.SiteID != item.SiteID {
			return errors.New("resource parent belongs to another site")
		}
		currentID = cloneID(current.ParentID)
	}

	return nil
}

func (s *Service) ensureNoRouteDescendants(
	ctx context.Context,
	item Resource,
	siteRuntime *site.Runtime,
) error {
	items, err := s.repository.ListBySite(ctx, item.SiteID)
	if err != nil {
		return fmt.Errorf(
			"list descendants of resource %d: %w",
			item.ID,
			err,
		)
	}

	byID := make(map[ID]Resource, len(items))
	for _, candidate := range items {
		byID[candidate.ID] = candidate
	}

	for _, candidate := range items {
		if candidate.ID == item.ID {
			continue
		}

		visited := make(map[ID]struct{})
		parentID := cloneID(candidate.ParentID)
		isDescendant := false
		for parentID != nil {
			if *parentID == item.ID {
				isDescendant = true
				break
			}
			if _, exists := visited[*parentID]; exists {
				return ErrInvalidTree
			}
			visited[*parentID] = struct{}{}

			parent, exists := byID[*parentID]
			if !exists {
				return fmt.Errorf(
					"resource %d references missing parent %d",
					candidate.ID,
					*parentID,
				)
			}
			parentID = cloneID(parent.ParentID)
		}
		if !isDescendant {
			continue
		}

		resourceType, exists := siteRuntime.Profile().
			Registry().
			ResourceType(candidate.Type)
		if !exists {
			return fmt.Errorf(
				"resource descendant %d references unknown type %q",
				candidate.ID,
				candidate.Type,
			)
		}
		if resourceType.PathMode() == resourcetype.PathRoute {
			return errors.New(
				"resource with route descendants cannot use no_path type",
			)
		}
	}

	return nil
}

func buildTree(items []Resource) ([]Node, error) {
	type mutableNode struct {
		resource Resource
		children []*mutableNode
	}

	nodes := make(map[ID]*mutableNode, len(items))
	for _, item := range items {
		if _, exists := nodes[item.ID]; exists {
			return nil, fmt.Errorf("duplicate resource id %d", item.ID)
		}
		nodes[item.ID] = &mutableNode{resource: Clone(item)}
	}

	roots := make([]*mutableNode, 0)
	for _, item := range items {
		node := nodes[item.ID]
		if item.ParentID == nil {
			roots = append(roots, node)
			continue
		}

		parent, exists := nodes[*item.ParentID]
		if !exists {
			return nil, fmt.Errorf(
				"resource %d references missing parent %d",
				item.ID,
				*item.ParentID,
			)
		}
		parent.children = append(parent.children, node)
	}

	sortNodes := func(nodes []*mutableNode) {
		sort.Slice(nodes, func(left, right int) bool {
			if nodes[left].resource.Sort != nodes[right].resource.Sort {
				return nodes[left].resource.Sort <
					nodes[right].resource.Sort
			}
			return nodes[left].resource.ID < nodes[right].resource.ID
		})
	}
	sortNodes(roots)
	for _, node := range nodes {
		sortNodes(node.children)
	}

	state := make(map[ID]uint8, len(nodes))
	visited := 0
	var convert func(*mutableNode) (Node, error)
	convert = func(current *mutableNode) (Node, error) {
		switch state[current.resource.ID] {
		case 1:
			return Node{}, ErrInvalidTree
		case 2:
			return Node{}, ErrInvalidTree
		}

		state[current.resource.ID] = 1
		result := Node{Resource: Clone(current.resource)}
		for _, child := range current.children {
			converted, err := convert(child)
			if err != nil {
				return Node{}, err
			}
			result.Children = append(result.Children, converted)
		}
		state[current.resource.ID] = 2
		visited++
		return result, nil
	}

	result := make([]Node, 0, len(roots))
	for _, root := range roots {
		converted, err := convert(root)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	if visited != len(nodes) {
		return nil, ErrInvalidTree
	}

	return result, nil
}

func validateContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s context is nil", operation)
	}
	return ctx.Err()
}

func boolDefault(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

func resourceTypeID(value *ID) *int64 {
	if value == nil {
		return nil
	}
	result := int64(*value)
	return &result
}

func resourceID(value *int64) *ID {
	if value == nil {
		return nil
	}
	result := ID(*value)
	return &result
}

func validLookupPath(path string) bool {
	_, err := NormalizeLookupPath(path)
	return err == nil
}

func equalStrings(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// Bound parameters are validated when their current resource values are resolved.
func validateLiteralWidgetInstance(runtime *widget.Runtime, params map[string]any, bindings widget.ParamBindings) error {
	if len(bindings) != 0 {
		return nil
	}
	_, err := runtime.New(params)
	return err
}
