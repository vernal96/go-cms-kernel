package resource

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

type ID int64

type StorageKind string

const (
	StorageTree        StorageKind = "tree"
	StorageLibraryItem StorageKind = "library_item"
)

// Ref is the stable identity shared by tree resources and LibraryItems.
type Ref struct {
	ID          ID
	SiteID      site.ID
	StorageKind StorageKind
}

const ImageMediaUsage media.UsageKind = "resource.image"

var (
	ErrNotFound                         = errors.New("resource not found")
	ErrConflict                         = errors.New("resource conflict")
	ErrRouteConflict                    = errors.New("resource route conflict")
	ErrRouteMutationRequiresMaintenance = errors.New("resource route mutation requires maintenance")
	ErrInvalid                          = errors.New("invalid resource")
	ErrInvalidReference                 = errors.New("invalid resource reference")
	ErrInvalidTree                      = errors.New("invalid resource tree")
	ErrReferenced                       = errors.New("resource is referenced")
	ErrIncompatibleTargetSite           = errors.New("resource target site is incompatible")
	ErrCrossSiteReference               = errors.New("resource transfer has cross-site references")
)

type Resource struct {
	ID               ID
	SiteID           site.ID
	Version          int64
	ParentID         *ID
	Type             resourcetype.Code
	Template         *template.Code
	ContentType      *string
	Title            string
	MenuTitle        string
	Slug             string
	Path             *string
	Annotation       string
	Content          string
	ImageMediaID     *media.ID
	TargetResourceID *ID
	ExternalURL      *string
	IsPublic         bool
	IsSearchable     bool
	InMenu           bool
	InSitemap        bool
	Sort             int
	PublishedAt      *time.Time
	UnpublishedAt    *time.Time
	Fields           map[string]any
	TypeSettings     map[string]any
	FieldValues      []field.StoredValue
	Widgets          []widget.Binding
	CreatedAt        time.Time
	UpdatedAt        time.Time
	CreatedBy        *security.UserID
	UpdatedBy        *security.UserID
	DeletedAt        *time.Time
	DeletedBy        *security.UserID
	FileReferences   map[string]file.ID
}

type CreateInput struct {
	SiteID           site.ID
	ParentID         *ID
	Type             resourcetype.Code
	Template         *template.Code
	ContentType      *string
	Title            string
	MenuTitle        string
	Slug             string
	Annotation       string
	Content          string
	ImageMediaID     *media.ID
	TargetResourceID *ID
	ExternalURL      *string
	IsPublic         *bool
	IsSearchable     *bool
	InMenu           *bool
	InSitemap        *bool
	Sort             int
	PublishedAt      *time.Time
	UnpublishedAt    *time.Time
	Fields           map[string]any
	TypeSettings     map[string]any
}

type UpdateInput struct {
	ID               ID
	ExpectedVersion  int64
	ParentID         *ID
	Type             resourcetype.Code
	Template         *template.Code
	ContentType      *string
	Title            string
	MenuTitle        string
	Slug             string
	Annotation       string
	Content          string
	ImageMediaID     *media.ID
	TargetResourceID *ID
	ExternalURL      *string
	IsPublic         bool
	IsSearchable     bool
	InMenu           bool
	InSitemap        bool
	Sort             int
	PublishedAt      *time.Time
	UnpublishedAt    *time.Time
	Fields           map[string]any
	TypeSettings     map[string]any
}

type CreateWidgetInput struct {
	ExpectedVersion int64
	Code            widget.Code
	Area            widget.AreaCode
	View            widget.ViewCode
	Columns         int
	MarginTop       int
	MarginBottom    int
	Enabled         *bool
	Params          map[string]any
	ParamBindings   widget.ParamBindings
}

type UpdateWidgetInput struct {
	ExpectedVersion int64
	View            widget.ViewCode
	Columns         int
	MarginTop       int
	MarginBottom    int
	Enabled         *bool
	Params          map[string]any
	ParamBindings   widget.ParamBindings
}

type Node struct {
	Resource Resource
	Children []Node
}

type Child struct {
	ID              ID
	Version         int64
	SiteID          site.ID
	ParentID        *ID
	Type            resourcetype.Code
	Template        *template.Code
	Title           string
	MenuTitle       string
	Sort            int
	IsPublic        bool
	InMenu          bool
	PublishedAt     *time.Time
	UnpublishedAt   *time.Time
	DeletedAt       *time.Time
	HasChildren     bool
	CanTransferSite bool
}

type SiteTransferResult struct {
	Resource    Resource
	ResourceIDs []ID
}

type SiteTransferCompatibility func(
	context.Context,
	[]Resource,
	*site.Runtime,
	*site.Runtime,
) error

type ValidateImageMedia func(context.Context, media.ID) error

type Repository interface {
	Create(
		context.Context,
		*security.UserID,
		Resource,
		ValidateImageMedia,
	) (Resource, error)
	ByID(context.Context, ID) (Resource, error)
	ByPath(context.Context, site.ID, string) (Resource, error)
	ListBySite(context.Context, site.ID) ([]Resource, error)
	Update(
		context.Context,
		*security.UserID,
		Resource,
		Resource,
		ValidateImageMedia,
	) (Resource, error)
	Delete(context.Context, ID) error
}

type WidgetRepository interface {
	CreateWidget(context.Context, *security.UserID, ID, int64, widget.Binding, bool) (widget.Binding, error)
	UpdateWidget(context.Context, *security.UserID, ID, int64, widget.Binding, bool) (widget.Binding, error)
	DeleteWidget(context.Context, *security.UserID, ID, int64, widget.BindingID, bool) error
	ReorderWidgets(context.Context, *security.UserID, ID, int64, []widget.Order, bool) ([]widget.Binding, error)
}

type LifecycleRepository interface {
	Repository
	SoftDelete(context.Context, *security.UserID, ID) error
	Restore(context.Context, *security.UserID, ID, bool) error
}

type ManagementRepository interface {
	Repository
	ExistsInSite(context.Context, site.ID, ID) (bool, error)
	ListChildren(context.Context, site.ID, *ID) ([]Child, error)
}

type SiteTransferRepository interface {
	TransferToSite(
		context.Context,
		*security.UserID,
		ID,
		site.ID,
		site.ID,
		int64,
		string,
		string,
	) (SiteTransferResult, error)
}

type StatisticsRepository interface {
	Statistics(context.Context, StatisticsQuery) (Statistics, error)
}

type StatisticsQuery struct {
	Scope   site.Scope
	SiteIDs []site.ID
}

type Statistics struct {
	Total  int
	BySite map[site.ID]int
}

var slugPattern = regexp.MustCompile(
	`^[a-z0-9]+(?:-[a-z0-9]+)*$`,
)

func Clone(item Resource) Resource {
	item.ParentID = cloneID(item.ParentID)
	item.Template = cloneTemplateCode(item.Template)
	item.ContentType = cloneString(item.ContentType)
	item.Path = cloneString(item.Path)
	item.ImageMediaID = cloneMediaID(item.ImageMediaID)
	item.TargetResourceID = cloneID(item.TargetResourceID)
	item.ExternalURL = cloneString(item.ExternalURL)
	item.PublishedAt = cloneTime(item.PublishedAt)
	item.UnpublishedAt = cloneTime(item.UnpublishedAt)
	item.Fields = cloneMap(item.Fields)
	item.TypeSettings = cloneMap(item.TypeSettings)
	item.FieldValues = cloneStoredValues(item.FieldValues)
	item.Widgets = widget.CloneBindings(item.Widgets)
	item.CreatedBy = cloneUserID(item.CreatedBy)
	item.UpdatedBy = cloneUserID(item.UpdatedBy)
	item.DeletedAt = cloneTime(item.DeletedAt)
	item.DeletedBy = cloneUserID(item.DeletedBy)
	item.FileReferences = cloneFileReferences(item.FileReferences)
	return item
}

func cloneStoredValues(source []field.StoredValue) []field.StoredValue {
	if source == nil {
		return nil
	}
	result := make([]field.StoredValue, len(source))
	copy(result, source)
	for index := range result {
		result[index].Value = cloneValue(result[index].Value)
		result[index].References = append([]field.Reference(nil), result[index].References...)
		for j := range result[index].References {
			ref := &result[index].References[j]
			ref.Path = append([]string(nil), ref.Path...)
			ref.Options.Storages = append(ref.Options.Storages[:0:0], ref.Options.Storages...)
			ref.Options.MIMETypes = append([]string(nil), ref.Options.MIMETypes...)
		}
	}
	return result
}

func cloneFileReferences(source map[string]file.ID) map[string]file.ID {
	if source == nil {
		return nil
	}
	result := make(map[string]file.ID, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneUserID(value *security.UserID) *security.UserID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func BuildPath(
	parent *Resource,
	slug string,
) (*string, error) {
	if parent == nil {
		path := "/"
		if slug != "" {
			path += slug
		}
		return &path, nil
	}

	if slug == "" {
		return nil, errors.New(
			"child resource slug is empty",
		)
	}
	if parent.Path == nil {
		return nil, errors.New(
			"route resource parent has no path",
		)
	}

	path := *parent.Path
	if path == "/" {
		path += slug
	} else {
		path += "/" + slug
	}
	return &path, nil
}

func validSlug(slug string, parentID *ID) bool {
	if slug == "" {
		return parentID == nil
	}

	return slugPattern.MatchString(slug)
}

func cloneID(value *ID) *ID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneMediaID(value *media.ID) *media.ID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneTemplateCode(value *template.Code) *template.Code {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}

	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneValue(value)
	}
	return result
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}
