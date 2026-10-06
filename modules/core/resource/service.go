package resource

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
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
			if !sourceExists || !targetExists {
				return fmt.Errorf("%w: library template %q is unavailable or incompatible", ErrIncompatibleTargetSite, code)
			}
			sourceDefinition, targetDefinition := sourceTemplate.Definition(), targetTemplate.Definition()
			// Layout is destination presentation. Persisted area codes remain
			// recoverable through default when its declarations differ.
			sourceDefinition.Layout, targetDefinition.Layout = nil, nil
			if !reflect.DeepEqual(sourceDefinition, targetDefinition) {
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
