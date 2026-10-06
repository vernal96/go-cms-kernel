package site

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	"golang.org/x/text/language"
)

type ID int64

var (
	ErrNotFound    = errors.New("site not found")
	ErrConflict    = errors.New("site conflict")
	ErrInvalid     = errors.New("invalid site")
	ErrUnavailable = errors.New("site runtime is stale or unavailable")

	readPermission = permission.MustCode(
		"core",
		"site",
		permission.Read,
	)
	updatePermission = permission.MustCode(
		"core",
		"site",
		permission.Update,
	)
	createPermission = permission.MustCode(
		"core",
		"site",
		permission.Create,
	)
	deletePermission = permission.MustCode(
		"core",
		"site",
		permission.Delete,
	)
)

type Site struct {
	Version        int64
	ID             ID
	ProfileCode    kernel.ProfileCode
	Domain         string
	Locale         string
	Settings       map[string]any
	IsPublic       bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CreatedBy      *security.UserID
	UpdatedBy      *security.UserID
	FileReferences map[string]file.ID
}

type Repository interface {
	List(context.Context) ([]Site, error)
	Update(
		context.Context,
		*security.UserID,
		Site,
	) (Site, error)
}

type ManagementRepository interface {
	Repository
	FindByID(context.Context, ID) (Site, error)
	FindByDomain(context.Context, string) (Site, error)
	ListPage(context.Context, ListQuery) (Page, error)
	Create(context.Context, *security.UserID, Site) (Site, error)
	Delete(context.Context, ID) error
}

type StatisticsRepository interface {
	Statistics(context.Context, StatisticsQuery) (Statistics, error)
}

type Scope struct {
	All     bool
	SiteIDs []ID
}

type ListQuery struct {
	Search    string
	Page      int
	PerPage   int
	Scope     Scope
	ExcludeID *ID
}

type Page struct {
	Items []Site
	Total int
}

type StatisticsQuery struct {
	Scope Scope
	Limit int
}

type Statistics struct {
	Items   []Site
	Total   int
	Public  int
	Private int
}

type Access interface {
	security.Authorizer
	IsGuestSubject(context.Context, security.Actor) (bool, error)
}

type UpdateInput struct {
	ID          ID
	ProfileCode kernel.ProfileCode
	Domain      string
	Locale      string
	Settings    map[string]any
	IsPublic    bool
}

type CreateInput struct {
	ProfileCode kernel.ProfileCode
	Domain      string
	Locale      string
	Settings    map[string]any
	IsPublic    bool
}

type Runtime struct {
	site           Site
	profileRuntime *kernel.ProfileRuntime
	fileReferences []field.FileReference
}

func NewRuntimeFromBlueprint(
	ctx context.Context,
	item Site,
	blueprint *kernel.ProfileBlueprint,
) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("site runtime context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if blueprint == nil {
		return nil, errors.New("profile blueprint is nil")
	}
	item, fileReferences, err := normalizeRuntimeSite(
		item,
		blueprint.Profile().Code,
		blueprint.ParamSchema(),
	)
	if err != nil {
		return nil, err
	}
	profileRuntime, err := blueprint.Build(
		ctx,
		kernel.NewSiteRuntimeScope(
			fmt.Sprint(item.ID),
			item.ProfileCode,
			item.Domain,
			item.Locale,
			item.IsPublic,
			item.Settings,
		),
	)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		site:           item,
		profileRuntime: profileRuntime,
		fileReferences: fileReferences,
	}, nil
}

func normalizeRuntimeSite(
	item Site,
	profileCode kernel.ProfileCode,
	paramSchema *field.Schema,
) (Site, []field.FileReference, error) {
	if item.ID <= 0 {
		return Site{}, nil, errors.New("invalid site id")
	}
	if item.ProfileCode == "" {
		return Site{}, nil, errors.New("site profile code is empty")
	}
	if item.ProfileCode != profileCode {
		return Site{}, nil, fmt.Errorf(
			"site profile %q does not match blueprint profile %q",
			item.ProfileCode,
			profileCode,
		)
	}
	domain, err := NormalizeDomain(item.Domain)
	if err != nil {
		return Site{}, nil, err
	}
	item.Domain = domain
	item.Locale = strings.TrimSpace(item.Locale)
	if item.Locale == "" {
		return Site{}, nil, errors.New("site locale is empty")
	}
	if _, err := language.Parse(item.Locale); err != nil {
		return Site{}, nil, fmt.Errorf("site locale %q is invalid: %w", item.Locale, err)
	}
	if paramSchema == nil {
		return Site{}, nil, errors.New("profile param schema is nil")
	}
	settings, err := paramSchema.Validate(item.Settings)
	if err != nil {
		return Site{}, nil, fmt.Errorf("validate site settings: %w", err)
	}
	item.Settings = cloneSettings(settings)
	fileReferences, err := paramSchema.FileReferences(settings)
	if err != nil {
		return Site{}, nil, fmt.Errorf("collect site file references: %w", err)
	}
	item.FileReferences = fileReferenceMap(fileReferences)
	return item, fileReferences, nil
}

func (r *Runtime) Site() Site {
	result := r.site
	result.Settings = cloneSettings(result.Settings)
	result.CreatedBy = cloneUserID(result.CreatedBy)
	result.UpdatedBy = cloneUserID(result.UpdatedBy)
	result.FileReferences = cloneFileReferences(result.FileReferences)
	return result
}

func (r *Runtime) Profile() *kernel.ProfileRuntime {
	return r.profileRuntime
}

type ProfileResolver interface {
	ProfileBlueprint(
		kernel.ProfileCode,
	) (*kernel.ProfileBlueprint, bool)
}
