package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

const (
	UserReadPermission    permission.Code = "core.user.read"
	UserCreatePermission  permission.Code = "core.user.create"
	UserUpdatePermission  permission.Code = "core.user.update"
	UserBlockPermission   permission.Code = "core.user.delete"
	GroupReadPermission   permission.Code = "core.group.read"
	GroupCreatePermission permission.Code = "core.group.create"
	GroupUpdatePermission permission.Code = "core.group.update"
	GroupDeletePermission permission.Code = "core.group.delete"
)

type UserDTO struct {
	ID           user.ID          `json:"id"`
	Login        string           `json:"login"`
	Email        string           `json:"email"`
	Name         string           `json:"name"`
	LastName     *string          `json:"last_name"`
	MiddleName   *string          `json:"middle_name"`
	Phone        *string          `json:"phone"`
	LastLoginAt  *time.Time       `json:"last_login_at"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	Blocked      bool             `json:"blocked"`
	BlockedAt    *time.Time       `json:"blocked_at"`
	Capabilities UserCapabilities `json:"capabilities"`
}

type UserCapabilities struct {
	Update         bool `json:"update"`
	ChangePassword bool `json:"change_password"`
	EditGroups     bool `json:"edit_groups"`
	Block          bool `json:"block"`
	Unblock        bool `json:"unblock"`
}

type UserPermissionSet struct {
	Read   bool `json:"read"`
	Create bool `json:"create"`
	Update bool `json:"update"`
	Block  bool `json:"block"`
}

type UserList struct {
	Items       []UserDTO         `json:"items"`
	Pagination  Pagination        `json:"pagination"`
	Permissions UserPermissionSet `json:"permissions"`
}

type UserDetails struct {
	User UserDTO `json:"user"`
}

type UserCreateInput struct {
	Login      string
	Email      string
	Password   string
	Name       string
	LastName   *string
	MiddleName *string
	Phone      *string
	GroupIDs   []group.ID
}

type UserUpdateInput struct {
	Login      string
	Email      string
	Name       string
	LastName   *string
	MiddleName *string
	Phone      *string
}

type GroupDTO struct {
	ID                   group.ID `json:"id"`
	Code                 string   `json:"code"`
	Name                 string   `json:"name"`
	System               bool     `json:"system"`
	Super                bool     `json:"super"`
	CanUpdate            bool     `json:"can_update"`
	CanDelete            bool     `json:"can_delete"`
	CanManagePermissions bool     `json:"can_manage_permissions"`
}

type GroupList struct {
	Items       []GroupDTO    `json:"items"`
	Pagination  Pagination    `json:"pagination"`
	Permissions PermissionSet `json:"permissions"`
}

type GroupOptions struct {
	Items      []GroupDTO `json:"items"`
	Pagination Pagination `json:"pagination"`
}

type GroupDetails struct {
	Group           GroupDTO          `json:"group"`
	PermissionCodes []permission.Code `json:"permission_codes"`
	SiteAccess      []GroupSiteAccess `json:"site_access"`
}

type GroupSiteAccess struct {
	SiteID    site.ID `json:"site_id"`
	CanView   bool    `json:"can_view"`
	CanEdit   bool    `json:"can_edit"`
	CanDelete bool    `json:"can_delete"`
}

type GroupCreateInput struct {
	Code            string
	Name            string
	PermissionCodes []permission.Code
	SiteAccess      []GroupSiteAccess
}

type GroupUpdateInput struct {
	Name            string
	PermissionCodes *[]permission.Code
	SiteAccess      *[]GroupSiteAccess
}

type PermissionDefinition struct {
	Code   permission.Code   `json:"code"`
	Module string            `json:"module"`
	Entity string            `json:"entity"`
	Action permission.Action `json:"action"`
}

type PermissionCatalog struct {
	Items     []PermissionDefinition `json:"items"`
	CanManage bool                   `json:"can_manage"`
}

func (m *Management) ListUsers(ctx context.Context, actor security.Actor, search string, status user.ListStatus, page, perPage int) (UserList, error) {
	if err := m.authorizer.Check(ctx, actor, UserReadPermission); err != nil {
		return UserList{}, err
	}
	page, perPage, err := normalizePagination(page, perPage)
	if err != nil {
		return UserList{}, err
	}
	if status == "" {
		status = user.ListAll
	}
	if status != user.ListAll && status != user.ListActive && status != user.ListBlocked {
		return UserList{}, fmt.Errorf("%w: invalid user status", ErrValidation)
	}
	result, err := m.userRepo.ListPage(ctx, user.ListQuery{Search: strings.TrimSpace(search), Status: status, Page: page, PerPage: perPage})
	if err != nil {
		return UserList{}, fmt.Errorf("list admin users: %w", err)
	}
	permissions, err := m.userPermissions(ctx, actor)
	if err != nil {
		return UserList{}, err
	}
	items := make([]UserDTO, len(result.Items))
	for index, item := range result.Items {
		items[index] = userDTO(item, actor, permissions, false)
	}
	return UserList{Items: items, Pagination: Pagination{Page: page, PerPage: perPage, Total: result.Total}, Permissions: permissions}, nil
}

func (m *Management) User(ctx context.Context, actor security.Actor, id user.ID) (UserDetails, error) {
	item, err := m.users.Get(ctx, actor, id)
	if err != nil {
		return UserDetails{}, err
	}
	permissions, err := m.userPermissions(ctx, actor)
	if err != nil {
		return UserDetails{}, err
	}
	editGroups, err := m.allowed(ctx, actor, GroupUpdatePermission)
	if err != nil {
		return UserDetails{}, err
	}
	return UserDetails{User: userDTO(item, actor, permissions, editGroups)}, nil
}

func (m *Management) CreateUser(ctx context.Context, actor security.Actor, input UserCreateInput) (UserDetails, error) {
	item, err := m.users.Create(ctx, actor, user.CreateInput{Login: input.Login, Email: input.Email, Password: input.Password, Name: input.Name, LastName: input.LastName, MiddleName: input.MiddleName, Phone: input.Phone, GroupIDs: input.GroupIDs})
	if err != nil {
		return UserDetails{}, identityValidationError(err)
	}
	permissions, err := m.userPermissions(ctx, actor)
	if err != nil {
		return UserDetails{}, err
	}
	editGroups, err := m.allowed(ctx, actor, GroupUpdatePermission)
	if err != nil {
		return UserDetails{}, err
	}
	return UserDetails{User: userDTO(item, actor, permissions, editGroups)}, nil
}

func (m *Management) UpdateUser(ctx context.Context, actor security.Actor, id user.ID, input UserUpdateInput) (UserDetails, error) {
	item, err := m.users.Update(ctx, actor, user.UpdateInput{ID: id, Login: input.Login, Email: input.Email, Name: input.Name, LastName: input.LastName, MiddleName: input.MiddleName, Phone: input.Phone})
	if err != nil {
		return UserDetails{}, identityValidationError(err)
	}
	permissions, err := m.userPermissions(ctx, actor)
	if err != nil {
		return UserDetails{}, err
	}
	editGroups, err := m.allowed(ctx, actor, GroupUpdatePermission)
	if err != nil {
		return UserDetails{}, err
	}
	return UserDetails{User: userDTO(item, actor, permissions, editGroups)}, nil
}

func (m *Management) ChangeUserPassword(ctx context.Context, actor security.Actor, id user.ID, password string) error {
	_, err := m.users.ChangePassword(ctx, actor, id, password)
	return identityValidationError(err)
}

func (m *Management) UserGroups(ctx context.Context, actor security.Actor, id user.ID) (GroupOptions, error) {
	items, err := m.groups.GroupsForUser(ctx, actor, id)
	if err != nil {
		return GroupOptions{}, err
	}
	privileged, err := m.access.IsPrivileged(ctx, actor)
	if err != nil {
		return GroupOptions{}, err
	}
	result := make([]GroupDTO, len(items))
	for index, item := range items {
		result[index] = groupDTO(item, false, false, privileged)
	}
	return GroupOptions{Items: result, Pagination: Pagination{Page: 1, PerPage: len(result), Total: len(result)}}, nil
}

func (m *Management) ReplaceUserGroups(ctx context.Context, actor security.Actor, id user.ID, ids []group.ID) error {
	return m.groups.ReplaceUserGroups(ctx, actor, id, ids)
}

func (m *Management) BlockUser(ctx context.Context, actor security.Actor, id user.ID) error {
	_, err := m.users.Block(ctx, actor, id)
	return err
}

func (m *Management) UnblockUser(ctx context.Context, actor security.Actor, id user.ID) error {
	_, err := m.users.Unblock(ctx, actor, id)
	return err
}

func (m *Management) ListGroups(ctx context.Context, actor security.Actor, search string, page, perPage int) (GroupList, error) {
	if err := m.authorizer.Check(ctx, actor, GroupReadPermission); err != nil {
		return GroupList{}, err
	}
	page, perPage, err := normalizePagination(page, perPage)
	if err != nil {
		return GroupList{}, err
	}
	result, err := m.groupRepo.ListPage(ctx, group.ListQuery{Search: strings.TrimSpace(search), Page: page, PerPage: perPage})
	if err != nil {
		return GroupList{}, fmt.Errorf("list admin groups: %w", err)
	}
	permissions, err := m.groupPermissions(ctx, actor)
	if err != nil {
		return GroupList{}, err
	}
	privileged, err := m.access.IsPrivileged(ctx, actor)
	if err != nil {
		return GroupList{}, err
	}
	items := make([]GroupDTO, len(result.Items))
	for index, item := range result.Items {
		items[index] = groupDTO(item, permissions.Update, permissions.Delete, privileged)
	}
	return GroupList{Items: items, Pagination: Pagination{Page: page, PerPage: perPage, Total: result.Total}, Permissions: permissions}, nil
}

func (m *Management) ListGroupOptions(ctx context.Context, actor security.Actor, search string, page, perPage int) (GroupOptions, error) {
	result, err := m.ListGroups(ctx, actor, search, page, perPage)
	if err != nil {
		return GroupOptions{}, err
	}
	return GroupOptions{Items: result.Items, Pagination: result.Pagination}, nil
}

func (m *Management) Group(ctx context.Context, actor security.Actor, id group.ID) (GroupDetails, error) {
	item, err := m.groups.Get(ctx, actor, id)
	if err != nil {
		return GroupDetails{}, err
	}
	permissions, err := m.groupPermissions(ctx, actor)
	if err != nil {
		return GroupDetails{}, err
	}
	privileged, err := m.access.IsPrivileged(ctx, actor)
	if err != nil {
		return GroupDetails{}, err
	}
	codes := m.access.Codes()
	siteAccess := make([]GroupSiteAccess, 0)
	if !item.IsSuper {
		grants, err := m.groups.Permissions(ctx, actor, id)
		if err != nil {
			return GroupDetails{}, err
		}
		codes = make([]permission.Code, len(grants))
		for index, grant := range grants {
			codes[index] = grant.Permission
		}
		siteGrants, err := m.groups.SiteAccesses(ctx, actor, id)
		if err != nil {
			return GroupDetails{}, err
		}
		siteAccess = make([]GroupSiteAccess, len(siteGrants))
		for index, grant := range siteGrants {
			siteAccess[index] = GroupSiteAccess{SiteID: grant.SiteID, CanView: grant.CanView, CanEdit: grant.CanEdit, CanDelete: grant.CanDelete}
		}
	}
	return GroupDetails{Group: groupDTO(item, permissions.Update, permissions.Delete, privileged), PermissionCodes: codes, SiteAccess: siteAccess}, nil
}

func (m *Management) CreateGroup(ctx context.Context, actor security.Actor, input GroupCreateInput) (GroupDetails, error) {
	item, err := m.groups.Create(ctx, actor, group.CreateInput{Code: input.Code, Name: input.Name, PermissionCodes: input.PermissionCodes, SiteAccesses: groupSiteAccess(input.SiteAccess)})
	if err != nil {
		return GroupDetails{}, identityValidationError(err)
	}
	return m.Group(ctx, actor, item.ID)
}

func (m *Management) UpdateGroup(ctx context.Context, actor security.Actor, id group.ID, input GroupUpdateInput) (GroupDetails, error) {
	current, err := m.groups.Get(ctx, actor, id)
	if err != nil {
		return GroupDetails{}, err
	}
	var siteAccesses *[]group.SiteAccess
	if input.SiteAccess != nil {
		items := groupSiteAccess(*input.SiteAccess)
		siteAccesses = &items
	}
	_, err = m.groups.Update(ctx, actor, group.UpdateInput{ID: id, Name: input.Name, IsSuper: current.IsSuper, PermissionCodes: input.PermissionCodes, SiteAccesses: siteAccesses})
	if err != nil {
		return GroupDetails{}, identityValidationError(err)
	}
	return m.Group(ctx, actor, id)
}

func (m *Management) DeleteGroup(ctx context.Context, actor security.Actor, id group.ID) error {
	return m.groups.Delete(ctx, actor, id)
}

func (m *Management) PermissionCatalog(ctx context.Context, actor security.Actor) (PermissionCatalog, error) {
	if err := m.authorizer.Check(ctx, actor, GroupReadPermission); err != nil {
		return PermissionCatalog{}, err
	}
	privileged, err := m.access.IsPrivileged(ctx, actor)
	if err != nil {
		return PermissionCatalog{}, err
	}
	codes := m.access.Codes()
	items := make([]PermissionDefinition, len(codes))
	for index, code := range codes {
		definition, err := permission.Parse(code)
		if err != nil {
			return PermissionCatalog{}, err
		}
		items[index] = PermissionDefinition{Code: code, Module: definition.Module, Entity: definition.Entity, Action: definition.Action}
	}
	return PermissionCatalog{Items: items, CanManage: privileged}, nil
}

func (m *Management) userPermissions(ctx context.Context, actor security.Actor) (UserPermissionSet, error) {
	codes := []permission.Code{UserReadPermission, UserCreatePermission, UserUpdatePermission, UserBlockPermission}
	values := make([]bool, len(codes))
	for index, code := range codes {
		allowed, err := m.allowed(ctx, actor, code)
		if err != nil {
			return UserPermissionSet{}, err
		}
		values[index] = allowed
	}
	return UserPermissionSet{Read: values[0], Create: values[1], Update: values[2], Block: values[3]}, nil
}

func (m *Management) groupPermissions(ctx context.Context, actor security.Actor) (PermissionSet, error) {
	codes := []permission.Code{GroupReadPermission, GroupCreatePermission, GroupUpdatePermission, GroupDeletePermission}
	values := make([]bool, len(codes))
	for index, code := range codes {
		allowed, err := m.allowed(ctx, actor, code)
		if err != nil {
			return PermissionSet{}, err
		}
		values[index] = allowed
	}
	return PermissionSet{Read: values[0], Create: values[1], Update: values[2], Delete: values[3]}, nil
}

func userDTO(item user.User, actor security.Actor, permissions UserPermissionSet, editGroups bool) UserDTO {
	actorID, isUser := actor.UserID()
	isSelf := isUser && actorID == item.ID
	return UserDTO{ID: item.ID, Login: item.Login, Email: item.Email, Name: item.Name, LastName: item.LastName, MiddleName: item.MiddleName, Phone: item.Phone, LastLoginAt: item.LastLoginAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, Blocked: item.BlockedAt != nil, BlockedAt: item.BlockedAt, Capabilities: UserCapabilities{Update: permissions.Update, ChangePassword: permissions.Update, EditGroups: editGroups, Block: permissions.Block && item.BlockedAt == nil && !isSelf, Unblock: permissions.Update && item.BlockedAt != nil}}
}

func groupDTO(item group.Group, canUpdate, canDelete, privileged bool) GroupDTO {
	system := item.Code == group.AdminCode
	return GroupDTO{ID: item.ID, Code: item.Code, Name: item.Name, System: system, Super: item.IsSuper, CanUpdate: canUpdate, CanDelete: canDelete && !system, CanManagePermissions: privileged && !item.IsSuper}
}

func groupSiteAccess(items []GroupSiteAccess) []group.SiteAccess {
	result := make([]group.SiteAccess, len(items))
	for index, item := range items {
		result[index] = group.SiteAccess{SiteID: item.SiteID, CanView: item.CanView, CanEdit: item.CanEdit, CanDelete: item.CanDelete}
	}
	return result
}

func identityValidationError(err error) error {
	if err == nil || errors.Is(err, user.ErrNotFound) || errors.Is(err, user.ErrConflict) || errors.Is(err, group.ErrNotFound) || errors.Is(err, group.ErrConflict) || errors.Is(err, group.ErrProtected) || errors.Is(err, group.ErrLastAdministrator) || errors.Is(err, user.ErrSelfBlock) || errors.Is(err, user.ErrLastAdministrator) || errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrUnauthenticated) || errors.Is(err, access.ErrNotPrivileged) {
		return err
	}
	return fmt.Errorf("%w: request data is invalid", ErrValidation)
}
