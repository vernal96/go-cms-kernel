package resource

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type RevisionKind string

const (
	RevisionCreated  RevisionKind = "created"
	RevisionUpdated  RevisionKind = "updated"
	RevisionRestored RevisionKind = "restored"
)

var ErrRevisionNotFound = errors.New("resource revision not found")

type WidgetSnapshot struct {
	Code          widget.Code          `json:"code"`
	Area          widget.AreaCode      `json:"area"`
	Position      int                  `json:"position"`
	View          widget.ViewCode      `json:"view"`
	Columns       int                  `json:"columns"`
	MarginTop     int                  `json:"margin_top"`
	MarginBottom  int                  `json:"margin_bottom"`
	Enabled       bool                 `json:"enabled"`
	Params        map[string]any       `json:"params"`
	ParamBindings widget.ParamBindings `json:"param_bindings"`
}

// Snapshot is the core-owned logical state. It deliberately excludes paths,
// persistence keys and lifecycle/audit metadata.
type Snapshot struct {
	StorageKind      StorageKind       `json:"storage_kind"`
	ParentID         *ID               `json:"parent_id,omitempty"`
	LibraryID        *ID               `json:"library_id,omitempty"`
	Type             resourcetype.Code `json:"type,omitempty"`
	Template         *template.Code    `json:"template_code,omitempty"`
	ContentType      *string           `json:"content_type,omitempty"`
	Title            string            `json:"title"`
	MenuTitle        string            `json:"menu_title,omitempty"`
	Slug             string            `json:"slug"`
	Annotation       string            `json:"annotation,omitempty"`
	Content          string            `json:"content,omitempty"`
	ImageMediaID     *media.ID         `json:"image_media_id,omitempty"`
	TargetResourceID *ID               `json:"target_resource_id,omitempty"`
	ExternalURL      *string           `json:"external_url,omitempty"`
	IsPublic         bool              `json:"is_public"`
	IsSearchable     bool              `json:"is_searchable"`
	InMenu           bool              `json:"in_menu"`
	InSitemap        bool              `json:"in_sitemap"`
	Sort             int               `json:"sort"`
	PublishedAt      *time.Time        `json:"published_at,omitempty"`
	UnpublishedAt    *time.Time        `json:"unpublished_at,omitempty"`
	Fields           map[string]any    `json:"fields"`
	TypeSettings     map[string]any    `json:"type_settings"`
	Widgets          []WidgetSnapshot  `json:"widgets"`
}

type Revision struct {
	ID            int64            `json:"id"`
	ResourceID    ID               `json:"resource_id"`
	SiteID        site.ID          `json:"site_id"`
	Version       int64            `json:"version"`
	Kind          RevisionKind     `json:"kind"`
	SourceVersion *int64           `json:"source_version,omitempty"`
	Snapshot      *Snapshot        `json:"snapshot,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	CreatedBy     *security.UserID `json:"created_by,omitempty"`
	CreatedByName string           `json:"created_by_name"`
}

type RevisionPage struct {
	Items   []Revision `json:"items"`
	Page    int        `json:"page"`
	PerPage int        `json:"per_page"`
	Total   int        `json:"total"`
}

type RevisionPolicy struct {
	LibraryItems bool
}

func DefaultRevisionPolicy() RevisionPolicy {
	return RevisionPolicy{LibraryItems: false}
}

type revisionPolicyProvider interface {
	ResourceRevisionPolicy() RevisionPolicy
}

func revisionPolicyFor(runtime *site.Runtime) RevisionPolicy {
	policy := DefaultRevisionPolicy()
	if runtime == nil || runtime.Profile() == nil {
		return policy
	}
	module, exists := runtime.Profile().Registry().Module(kernel.ModuleCode("core"))
	if !exists {
		return policy
	}
	provider, ok := module.(revisionPolicyProvider)
	if !ok {
		return policy
	}
	return provider.ResourceRevisionPolicy()
}

type RevisionRepository interface {
	ListRevisions(context.Context, site.ID, ID, int, int) (RevisionPage, error)
	Revision(context.Context, site.ID, ID, int64) (Revision, error)
	PurgeRevisions(context.Context, site.ID, ID) (int64, error)
	CountRevisions(context.Context) (int64, error)
	PurgeAllRevisions(context.Context) (int64, error)
	RestoreRevision(context.Context, *security.UserID, Resource, Resource, int64) (Resource, error)
	RestoreLibraryItemRevision(context.Context, *security.UserID, LibraryItem, LibraryItem, int64) (LibraryItem, error)
}

var (
	HistoryReadPermission   = permission.MustCode("core", "resource_history", permission.Read)
	HistoryDeletePermission = permission.MustCode("core", "resource_history", permission.Delete)
)

type RevisionService struct {
	repository RevisionRepository
	resources  *Service
	library    *LibraryService
	authorizer security.Authorizer
}

func NewRevisionService(repository RevisionRepository, resources *Service, library *LibraryService, authorizer security.Authorizer) (*RevisionService, error) {
	if repository == nil || resources == nil || library == nil || authorizer == nil {
		return nil, errors.New("resource revision dependencies are nil")
	}
	return &RevisionService{repository: repository, resources: resources, library: library, authorizer: authorizer}, nil
}

func (s *RevisionService) List(ctx context.Context, actor security.Actor, siteID site.ID, resourceID ID, page, perPage int) (RevisionPage, error) {
	if err := s.authorizer.Check(ctx, actor, HistoryReadPermission); err != nil {
		return RevisionPage{}, err
	}
	return s.repository.ListRevisions(ctx, siteID, resourceID, page, perPage)
}

func (s *RevisionService) Get(ctx context.Context, actor security.Actor, siteID site.ID, resourceID ID, version int64) (Revision, error) {
	if err := s.authorizer.Check(ctx, actor, HistoryReadPermission); err != nil {
		return Revision{}, err
	}
	return s.repository.Revision(ctx, siteID, resourceID, version)
}

func (s *RevisionService) Purge(ctx context.Context, actor security.Actor, siteID site.ID, resourceID ID) (int64, error) {
	if err := s.authorizer.Check(ctx, actor, HistoryDeletePermission); err != nil {
		return 0, err
	}
	return s.repository.PurgeRevisions(ctx, siteID, resourceID)
}

func (s *RevisionService) CountAll(ctx context.Context) (int64, error) {
	return s.repository.CountRevisions(ctx)
}
func (s *RevisionService) PurgeAll(ctx context.Context) (int64, error) {
	return s.repository.PurgeAllRevisions(ctx)
}

func (s *RevisionService) LibraryHistoryEnabled(siteID site.ID) bool {
	runtime, exists := s.resources.sites.RuntimeByID(siteID)
	return exists && revisionPolicyFor(runtime).LibraryItems
}

func (s *RevisionService) Restore(ctx context.Context, actor security.Actor, siteID site.ID, resourceID ID, version, expectedVersion int64) (Resource, error) {
	ctx, finishHooks, hookErr := s.resources.beginMutation(ctx, actor, OperationRevision)
	if hookErr != nil {
		return Resource{}, hookErr
	}
	defer finishHooks()

	if err := s.authorizer.Check(ctx, actor, HistoryReadPermission); err != nil {
		return Resource{}, err
	}
	if err := s.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return Resource{}, err
	}
	revision, err := s.repository.Revision(ctx, siteID, resourceID, version)
	if err != nil {
		return Resource{}, err
	}
	if revision.Snapshot == nil {
		return Resource{}, fmt.Errorf("%w: revision snapshot is unavailable", ErrInvalid)
	}
	if revision.Snapshot.StorageKind == StorageLibraryItem {
		return s.restoreLibraryItem(ctx, actor, siteID, resourceID, version, expectedVersion, *revision.Snapshot)
	}
	if revision.Snapshot.StorageKind != StorageTree {
		return Resource{}, fmt.Errorf("%w: unsupported historical storage kind", ErrInvalid)
	}
	current, err := s.resources.repository.ByID(ctx, resourceID)
	if err != nil {
		return Resource{}, err
	}
	if current.SiteID != siteID {
		return Resource{}, ErrNotFound
	}
	if expectedVersion <= 0 || current.Version != expectedVersion {
		return Resource{}, ErrConflict
	}
	candidate := resourceFromSnapshot(current, *revision.Snapshot)
	runtime, exists := s.resources.runtime(ctx, siteID)
	if !exists {
		return Resource{}, ErrNotFound
	}
	if err := s.resources.ensureNoParentCycle(ctx, candidate); err != nil {
		return Resource{}, fmt.Errorf("%w: historical parent is invalid: %w", ErrInvalid, err)
	}
	historicalWidgets := candidate.Widgets
	candidate.Widgets = nil
	candidate, err = s.resources.normalize(ctx, actor, candidate, runtime, nil, current.FileReferences)
	candidate.Widgets = historicalWidgets
	if err != nil {
		return Resource{}, fmt.Errorf("%w: historical state is invalid: %w", ErrInvalid, err)
	}
	if current.Path != nil && candidate.Path == nil {
		if err := s.resources.ensureNoRouteDescendants(ctx, current, runtime); err != nil {
			return Resource{}, fmt.Errorf("%w: historical route state is invalid: %w", ErrInvalid, err)
		}
	}
	if err := validateSnapshotWidgets(runtime, &candidate); err != nil {
		return Resource{}, err
	}
	return s.repository.RestoreRevision(ctx, actor.AuditUserID(), current, candidate, version)
}

func (s *RevisionService) restoreLibraryItem(ctx context.Context, actor security.Actor, siteID site.ID, resourceID ID, version, expectedVersion int64, snapshot Snapshot) (Resource, error) {
	current, err := s.library.repository.LibraryItemByID(ctx, resourceID)
	if err != nil {
		return Resource{}, err
	}
	if current.SiteID != siteID {
		return Resource{}, ErrNotFound
	}
	if expectedVersion <= 0 || current.Version != expectedVersion {
		return Resource{}, ErrConflict
	}
	if snapshot.LibraryID == nil {
		return Resource{}, fmt.Errorf("%w: historical library is missing", ErrInvalid)
	}
	_, runtime, err := s.library.library(ctx, siteID, *snapshot.LibraryID)
	if err != nil {
		return Resource{}, fmt.Errorf("%w: historical library is invalid: %w", ErrInvalid, err)
	}
	candidate := libraryItemFromSnapshot(current, snapshot)
	candidate, err = s.library.normalize(ctx, actor, candidate, runtime, current.FileReferences)
	if err != nil {
		return Resource{}, fmt.Errorf("%w: historical state is invalid: %w", ErrInvalid, err)
	}
	widgetCandidate := Resource{Template: candidate.Template, Widgets: candidate.Widgets}
	if err := validateSnapshotWidgets(runtime, &widgetCandidate); err != nil {
		return Resource{}, err
	}
	candidate.Widgets = widgetCandidate.Widgets
	restored, err := s.repository.RestoreLibraryItemRevision(ctx, actor.AuditUserID(), current, candidate, version)
	if err != nil {
		return Resource{}, err
	}
	return resourceProjection(restored), nil
}

func libraryItemFromSnapshot(current LibraryItem, snapshot Snapshot) LibraryItem {
	return LibraryItem{ID: current.ID, SiteID: current.SiteID, Version: current.Version,
		LibraryID: *snapshot.LibraryID, Template: cloneTemplateCode(snapshot.Template), ContentType: cloneString(snapshot.ContentType),
		Title: snapshot.Title, Slug: snapshot.Slug, Annotation: snapshot.Annotation, Content: snapshot.Content,
		ImageMediaID: cloneMediaID(snapshot.ImageMediaID), IsPublic: snapshot.IsPublic, IsSearchable: snapshot.IsSearchable,
		PublishedAt: cloneTime(snapshot.PublishedAt), UnpublishedAt: cloneTime(snapshot.UnpublishedAt), Fields: cloneMap(snapshot.Fields),
		Widgets: revisionWidgets(snapshot.Widgets), CreatedAt: current.CreatedAt, UpdatedAt: current.UpdatedAt,
		CreatedBy: cloneUserID(current.CreatedBy), UpdatedBy: cloneUserID(current.UpdatedBy),
		DeletedAt: cloneTime(current.DeletedAt), DeletedBy: cloneUserID(current.DeletedBy)}
}

func resourceProjection(item LibraryItem) Resource {
	return Resource{ID: item.ID, SiteID: item.SiteID, Version: item.Version, Type: resourcetype.Page,
		Template: item.Template, ContentType: item.ContentType, Title: item.Title, Slug: item.Slug,
		Annotation: item.Annotation, Content: item.Content, ImageMediaID: item.ImageMediaID,
		IsPublic: item.IsPublic, IsSearchable: item.IsSearchable, PublishedAt: item.PublishedAt,
		UnpublishedAt: item.UnpublishedAt, Fields: item.Fields, FieldValues: item.FieldValues,
		Widgets: item.Widgets, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		CreatedBy: item.CreatedBy, UpdatedBy: item.UpdatedBy, DeletedAt: item.DeletedAt, DeletedBy: item.DeletedBy}
}

func resourceFromSnapshot(current Resource, snapshot Snapshot) Resource {
	return Resource{ID: current.ID, SiteID: current.SiteID, Version: current.Version,
		ParentID: cloneID(snapshot.ParentID), Type: snapshot.Type, Template: cloneTemplateCode(snapshot.Template),
		ContentType: cloneString(snapshot.ContentType), Title: snapshot.Title, MenuTitle: snapshot.MenuTitle,
		Slug: snapshot.Slug, Annotation: snapshot.Annotation, Content: snapshot.Content,
		ImageMediaID: cloneMediaID(snapshot.ImageMediaID), TargetResourceID: cloneID(snapshot.TargetResourceID),
		ExternalURL: cloneString(snapshot.ExternalURL), IsPublic: snapshot.IsPublic, IsSearchable: snapshot.IsSearchable,
		InMenu: snapshot.InMenu, InSitemap: snapshot.InSitemap, Sort: snapshot.Sort,
		PublishedAt: cloneTime(snapshot.PublishedAt), UnpublishedAt: cloneTime(snapshot.UnpublishedAt),
		Fields: cloneMap(snapshot.Fields), TypeSettings: cloneMap(snapshot.TypeSettings), Widgets: revisionWidgets(snapshot.Widgets),
		CreatedAt: current.CreatedAt, CreatedBy: cloneUserID(current.CreatedBy), UpdatedBy: cloneUserID(current.UpdatedBy),
		DeletedAt: cloneTime(current.DeletedAt), DeletedBy: cloneUserID(current.DeletedBy)}
}

func revisionWidgets(items []WidgetSnapshot) []widget.Binding {
	result := make([]widget.Binding, len(items))
	for index, item := range items {
		result[index] = widget.Binding{Code: item.Code, Area: item.Area, Position: item.Position,
			Presentation: widget.Presentation{View: item.View, Columns: item.Columns, MarginTop: item.MarginTop, MarginBottom: item.MarginBottom, Enabled: item.Enabled}, Params: cloneMap(item.Params), ParamBindings: widget.CloneParamBindings(item.ParamBindings)}
	}
	return result
}

func validateSnapshotWidgets(runtime *site.Runtime, candidate *Resource) error {
	if len(candidate.Widgets) == 0 {
		return nil
	}
	if candidate.Template == nil {
		return fmt.Errorf("%w: historical widgets require a template", ErrInvalid)
	}
	templateRuntime, exists := runtime.Profile().Template(*candidate.Template)
	if !exists || !templateRuntime.SupportsResourceWidgets() {
		return fmt.Errorf("%w: historical template does not support widgets", ErrInvalid)
	}
	positions := make(map[widget.AreaCode]map[int]bool)
	for index := range candidate.Widgets {
		binding := &candidate.Widgets[index]
		if positions[binding.Area] == nil {
			positions[binding.Area] = make(map[int]bool)
		}
		if binding.Position < 0 || positions[binding.Area][binding.Position] {
			return fmt.Errorf("%w: duplicate or negative widget position", ErrInvalid)
		}
		positions[binding.Area][binding.Position] = true
		widgetRuntime, exists := runtime.Profile().Widget(binding.Code)
		if !exists || !templateRuntime.AllowsResourceArea(binding.Area) {
			return fmt.Errorf("%w: historical widget %q is unavailable", ErrInvalid, binding.Code)
		}
		if err := widgetRuntime.ValidatePresentation(binding.Presentation); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		params, err := widgetRuntime.NormalizeConfiguration(binding.Params, binding.ParamBindings, templateRuntime.FieldSchema())
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		binding.Params = params
		if err := validateLiteralWidgetInstance(widgetRuntime, params, binding.ParamBindings); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	return nil
}
