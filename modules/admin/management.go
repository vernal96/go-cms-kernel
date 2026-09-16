package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/adminui"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/management"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

var ErrValidation = errors.New("admin validation failed")

const (
	SiteReadPermission       = management.SiteReadPermission
	SiteCreatePermission     = management.SiteCreatePermission
	SiteUpdatePermission     = management.SiteUpdatePermission
	SiteDeletePermission     = management.SiteDeletePermission
	ResourceReadPermission   = management.ResourceReadPermission
	ResourceCreatePermission = management.ResourceCreatePermission
	ResourceUpdatePermission = management.ResourceUpdatePermission
	ResourceDeletePermission = management.ResourceDeletePermission
	FileReadPermission       = management.FileReadPermission
	FileCreatePermission     = management.FileCreatePermission
	FileUpdatePermission     = management.FileUpdatePermission
	FileDeletePermission     = management.FileDeletePermission
)

var (
	ResourceHistoryReadPermission   = resource.HistoryReadPermission
	ResourceHistoryDeletePermission = resource.HistoryDeletePermission
)

var AdminPermissionCodes = []permission.Code{
	AccessPermission,
	SiteReadPermission, SiteCreatePermission, SiteUpdatePermission, SiteDeletePermission,
	ResourceReadPermission, ResourceCreatePermission, ResourceUpdatePermission, ResourceDeletePermission,
	ResourceHistoryReadPermission, ResourceHistoryDeletePermission,
	UserReadPermission, UserCreatePermission, UserUpdatePermission, UserBlockPermission,
	GroupReadPermission, GroupCreatePermission, GroupUpdatePermission, GroupDeletePermission,
	FileReadPermission, FileCreatePermission, FileUpdatePermission, FileDeletePermission,
}

type SiteAccessPolicy = management.SiteAccessPolicy
type SiteAccessScope = management.SiteAccessScope
type SiteAccessAction = management.SiteAccessAction
type AllowAllSitesPolicy = management.AllowAllSitesPolicy

const (
	SiteAccessView   = management.SiteAccessView
	SiteAccessEdit   = management.SiteAccessEdit
	SiteAccessDelete = management.SiteAccessDelete
)

type Pagination struct {
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
}

type PermissionSet struct {
	Read   bool `json:"read"`
	Create bool `json:"create"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

type Management struct {
	images        *media.ImageService
	imageFiles    *management.Files
	repository    site.ManagementRepository
	sites         management.SiteCatalog
	resourceRepo  resource.ManagementRepository
	authorizer    security.Authorizer
	policy        management.SiteAccessPolicy
	users         user.Service
	userRepo      user.ManagementRepository
	groups        group.Service
	groupRepo     group.ManagementRepository
	access        access.Service
	files         file.ManagementService
	media         media.Service
	uploadTimeout time.Duration
	avatarStorage filesystem.Code
	avatarMaxSize int64
	navigation    *navigationComposer
}

type ManagementDependencies struct {
	Images             *media.ImageService
	ImageFiles         *management.Files
	Profiles           []kernel.Profile
	SiteRepository     site.ManagementRepository
	Sites              management.SiteCatalog
	ResourceRepository resource.ManagementRepository
	Authorizer         security.Authorizer
	SiteAccessPolicy   management.SiteAccessPolicy
	Users              user.Service
	UserRepository     user.ManagementRepository
	Groups             group.Service
	GroupRepository    group.ManagementRepository
	Access             access.Service
	Files              file.ManagementService
	Media              media.Service
	UploadTimeout      time.Duration
	AvatarStorage      filesystem.Code
	AvatarMaxSize      int64
	Permissions        adminui.PermissionValidator
}

func NewManagement(dependencies ManagementDependencies) (*Management, error) {
	if dependencies.SiteRepository == nil || dependencies.Sites == nil || dependencies.ResourceRepository == nil {
		return nil, errors.New("admin dashboard dependencies are nil")
	}
	if dependencies.Authorizer == nil || dependencies.Permissions == nil || dependencies.SiteAccessPolicy == nil {
		return nil, errors.New("admin authorization dependencies are nil")
	}
	if dependencies.Users == nil || dependencies.UserRepository == nil || dependencies.Groups == nil || dependencies.GroupRepository == nil {
		return nil, errors.New("admin identity dependencies are nil")
	}
	if dependencies.Access == nil || dependencies.Files == nil || dependencies.Media == nil {
		return nil, errors.New("admin profile dependencies are nil")
	}
	if dependencies.UploadTimeout <= 0 {
		dependencies.UploadTimeout = 10 * time.Minute
	}
	if dependencies.AvatarStorage == "" {
		dependencies.AvatarStorage = "private"
	}
	if dependencies.AvatarMaxSize <= 0 {
		dependencies.AvatarMaxSize = 5 << 20
	}
	navigation, err := newNavigationComposer(dependencies.Profiles, dependencies.Authorizer, dependencies.Permissions, dependencies.Access)
	if err != nil {
		return nil, err
	}
	return &Management{
		images: dependencies.Images, imageFiles: dependencies.ImageFiles,
		repository: dependencies.SiteRepository, sites: dependencies.Sites,
		resourceRepo: dependencies.ResourceRepository, authorizer: dependencies.Authorizer,
		policy: dependencies.SiteAccessPolicy, users: dependencies.Users,
		userRepo: dependencies.UserRepository, groups: dependencies.Groups,
		groupRepo: dependencies.GroupRepository, access: dependencies.Access,
		files: dependencies.Files, media: dependencies.Media,
		uploadTimeout: dependencies.UploadTimeout, avatarStorage: dependencies.AvatarStorage,
		avatarMaxSize: dependencies.AvatarMaxSize, navigation: navigation,
	}, nil
}

func (m *Management) Navigation(ctx context.Context, actor security.Actor, selectedSiteID *site.ID) (Navigation, error) {
	if m == nil || m.navigation == nil {
		return Navigation{}, errors.New("admin navigation is unavailable")
	}
	var runtime *site.Runtime
	if selectedSiteID != nil {
		if err := requireSite(ctx, m.authorizer, m.policy, actor, *selectedSiteID, SiteReadPermission, SiteAccessEdit); err != nil {
			return Navigation{}, err
		}
		var exists bool
		runtime, exists = m.sites.RuntimeByID(*selectedSiteID)
		if !exists {
			return Navigation{}, site.ErrNotFound
		}
	}
	items, err := m.navigation.compose(ctx, actor, runtime)
	if err != nil {
		return Navigation{}, err
	}
	return Navigation{Items: navigationDTO(items)}, nil
}

func requireSite(ctx context.Context, authorizer security.Authorizer, policy management.SiteAccessPolicy, actor security.Actor, id site.ID, code permission.Code, action management.SiteAccessAction) error {
	if id <= 0 {
		return fmt.Errorf("%w: invalid site id", ErrValidation)
	}
	if err := authorizer.Check(ctx, actor, code); err != nil {
		return err
	}
	return policy.Check(ctx, actor, id, action)
}

func (m *Management) allowed(ctx context.Context, actor security.Actor, code permission.Code) (bool, error) {
	err := m.authorizer.Check(ctx, actor, code)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrUnauthenticated) {
		return false, nil
	}
	return false, err
}

func normalizePagination(page, perPage int) (int, int, error) {
	if page == 0 {
		page = 1
	}
	if perPage == 0 {
		perPage = 10
	}
	if page < 1 || perPage < 1 || perPage > 100 {
		return 0, 0, fmt.Errorf("%w: invalid pagination", ErrValidation)
	}
	return page, perPage, nil
}

func fileValidationError(err error) error {
	switch {
	case errors.Is(err, security.ErrUnauthenticated), errors.Is(err, security.ErrForbidden),
		errors.Is(err, file.ErrNotFound), errors.Is(err, file.ErrFolderNotFound),
		errors.Is(err, file.ErrStorageNotFound), errors.Is(err, file.ErrConflict),
		errors.Is(err, file.ErrStorageMismatch), errors.Is(err, file.ErrInvalidTree), errors.Is(err, file.ErrInUse):
		return err
	default:
		return fmt.Errorf("%w: request data is invalid", ErrValidation)
	}
}
