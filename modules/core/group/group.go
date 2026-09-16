package group

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type ID int64

const AdminCode = "admin"

var (
	ErrNotFound          = errors.New("group not found")
	ErrConflict          = errors.New("group conflict")
	ErrInvalidReference  = errors.New("invalid group reference")
	ErrProtected         = errors.New("protected group")
	ErrLastAdministrator = errors.New("cannot remove last active administrator")

	codePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
)

type Group struct {
	ID        ID
	Code      string
	Name      string
	IsSuper   bool
	CreatedAt time.Time
	UpdatedAt time.Time
	CreatedBy *security.UserID
	UpdatedBy *security.UserID
}

type Membership struct {
	UserID    security.UserID
	GroupID   ID
	CreatedAt time.Time
	UpdatedAt time.Time
	CreatedBy *security.UserID
	UpdatedBy *security.UserID
}

type PermissionGrant struct {
	GroupID    ID
	Permission permission.Code
	CreatedAt  time.Time
	UpdatedAt  time.Time
	CreatedBy  *security.UserID
	UpdatedBy  *security.UserID
}

type SiteAccessAction string

const (
	SiteAccessView   SiteAccessAction = "view"
	SiteAccessEdit   SiteAccessAction = "edit"
	SiteAccessDelete SiteAccessAction = "delete"
)

type SiteAccess struct {
	GroupID   ID
	SiteID    site.ID
	CanView   bool
	CanEdit   bool
	CanDelete bool
	CreatedAt time.Time
	UpdatedAt time.Time
	CreatedBy *security.UserID
	UpdatedBy *security.UserID
}

type CreateInput struct {
	Code            string
	Name            string
	IsSuper         bool
	PermissionCodes []permission.Code
	SiteAccesses    []SiteAccess
}

type UpdateInput struct {
	ID              ID
	Name            string
	IsSuper         bool
	PermissionCodes *[]permission.Code
	SiteAccesses    *[]SiteAccess
}

type ListQuery struct {
	Search  string
	Page    int
	PerPage int
}

type Page struct {
	Items []Group
	Total int
}

type AssignmentValidator interface {
	ValidateUserAssignment(
		context.Context,
		security.Actor,
		[]ID,
	) ([]Group, error)
}

type Repository interface {
	Create(context.Context, *security.UserID, Group, []permission.Code, []SiteAccess) (Group, error)
	ByID(context.Context, ID) (Group, error)
	ByCode(context.Context, string) (Group, error)
	List(context.Context) ([]Group, error)
	Update(context.Context, *security.UserID, Group, *[]permission.Code, *[]SiteAccess) (Group, error)
	Delete(context.Context, ID) error
	AddUser(
		context.Context,
		*security.UserID,
		ID,
		security.UserID,
	) (Membership, error)
	RemoveUser(context.Context, ID, security.UserID) error
	Members(context.Context, ID) ([]Membership, error)
	GroupsForUser(
		context.Context,
		security.UserID,
	) ([]Group, error)
	ReplaceUserGroups(
		context.Context,
		*security.UserID,
		security.UserID,
		[]ID,
	) error
	GrantPermission(
		context.Context,
		*security.UserID,
		ID,
		permission.Code,
	) (PermissionGrant, error)
	RevokePermission(context.Context, ID, permission.Code) error
	Permissions(context.Context, ID) ([]PermissionGrant, error)
	SiteAccesses(context.Context, ID) ([]SiteAccess, error)
	EffectiveSiteIDs(context.Context, security.UserID, SiteAccessAction) ([]site.ID, error)
	UserHasSiteAccess(context.Context, security.UserID, site.ID, SiteAccessAction) (bool, error)
}

type ManagementRepository interface {
	Repository
	ListPage(context.Context, ListQuery) (Page, error)
}

type StatisticsRepository interface {
	Count(context.Context) (int, error)
}

type Service interface {
	AssignmentValidator
	Create(context.Context, security.Actor, CreateInput) (Group, error)
	Get(context.Context, security.Actor, ID) (Group, error)
	GetByCode(context.Context, security.Actor, string) (Group, error)
	List(context.Context, security.Actor) ([]Group, error)
	Update(context.Context, security.Actor, UpdateInput) (Group, error)
	Delete(context.Context, security.Actor, ID) error
	AddUser(
		context.Context,
		security.Actor,
		ID,
		security.UserID,
	) (Membership, error)
	RemoveUser(
		context.Context,
		security.Actor,
		ID,
		security.UserID,
	) error
	Members(
		context.Context,
		security.Actor,
		ID,
	) ([]Membership, error)
	GroupsForUser(
		context.Context,
		security.Actor,
		security.UserID,
	) ([]Group, error)
	ReplaceUserGroups(
		context.Context,
		security.Actor,
		security.UserID,
		[]ID,
	) error
	GrantPermission(
		context.Context,
		security.Actor,
		ID,
		permission.Code,
	) (PermissionGrant, error)
	RevokePermission(
		context.Context,
		security.Actor,
		ID,
		permission.Code,
	) error
	Permissions(
		context.Context,
		security.Actor,
		ID,
	) ([]PermissionGrant, error)
	SiteAccesses(context.Context, security.Actor, ID) ([]SiteAccess, error)
}

func Clone(item Group) Group {
	item.CreatedBy = cloneUserID(item.CreatedBy)
	item.UpdatedBy = cloneUserID(item.UpdatedBy)
	return item
}

func cloneUserID(value *security.UserID) *security.UserID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
