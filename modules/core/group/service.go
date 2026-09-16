package group

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

var (
	readPermission   = permission.MustCode("core", "group", permission.Read)
	createPermission = permission.MustCode("core", "group", permission.Create)
	updatePermission = permission.MustCode("core", "group", permission.Update)
	deletePermission = permission.MustCode("core", "group", permission.Delete)
)

type ApplicationService struct {
	hooks      *entityhooks.Registry
	repository Repository
	access     access.Service
}

func NewService(
	repository Repository,
	accessService access.Service,
	hooks *entityhooks.Registry,
) (*ApplicationService, error) {
	if repository == nil {
		return nil, errors.New("group repository is nil")
	}
	if accessService == nil {
		return nil, errors.New("group access service is nil")
	}
	if hooks == nil {
		return nil, errors.New("entity hook registry is nil")
	}
	return &ApplicationService{
		hooks:      hooks,
		repository: repository,
		access:     accessService,
	}, nil
}

func (s *ApplicationService) Create(
	ctx context.Context,
	actor security.Actor,
	input CreateInput,
) (Group, error) {
	if err := s.access.Check(ctx, actor, createPermission); err != nil {
		return Group{}, err
	}

	item, err := normalize(Group{
		Code:    input.Code,
		Name:    input.Name,
		IsSuper: input.IsSuper,
	})
	if err != nil {
		return Group{}, err
	}
	if item.IsSuper {
		if err := s.requirePrivileged(ctx, actor); err != nil {
			return Group{}, err
		}
	}
	permissions, err := s.normalizePermissions(input.PermissionCodes)
	if err != nil {
		return Group{}, err
	}
	if len(permissions) > 0 {
		if err := s.requirePrivileged(ctx, actor); err != nil {
			return Group{}, err
		}
	}
	siteAccesses, err := normalizeSiteAccesses(input.SiteAccesses)
	if err != nil {
		return Group{}, err
	}
	if len(siteAccesses) > 0 {
		if err := s.requirePrivileged(ctx, actor); err != nil {
			return Group{}, err
		}
	}

	created, err := s.repository.Create(
		ctx,
		actor.AuditUserID(),
		item,
		permissions,
		siteAccesses,
	)
	if err != nil {
		return Group{}, fmt.Errorf("create group: %w", err)
	}
	return Clone(created), nil
}

func (s *ApplicationService) Get(
	ctx context.Context,
	actor security.Actor,
	id ID,
) (Group, error) {
	if err := s.access.Check(ctx, actor, readPermission); err != nil {
		return Group{}, err
	}
	return s.byID(ctx, id)
}

func (s *ApplicationService) GetByCode(
	ctx context.Context,
	actor security.Actor,
	code string,
) (Group, error) {
	if err := s.access.Check(ctx, actor, readPermission); err != nil {
		return Group{}, err
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if !codePattern.MatchString(code) {
		return Group{}, errors.New("invalid group code")
	}
	item, err := s.repository.ByCode(ctx, code)
	if err != nil {
		return Group{}, fmt.Errorf("get group by code: %w", err)
	}
	return Clone(item), nil
}

func (s *ApplicationService) List(
	ctx context.Context,
	actor security.Actor,
) ([]Group, error) {
	if err := s.access.Check(ctx, actor, readPermission); err != nil {
		return nil, err
	}
	items, err := s.repository.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	result := make([]Group, len(items))
	for index, item := range items {
		result[index] = Clone(item)
	}
	return result, nil
}

func (s *ApplicationService) ValidateUserAssignment(
	ctx context.Context,
	actor security.Actor,
	ids []ID,
) ([]Group, error) {
	if err := s.access.Check(ctx, actor, updatePermission); err != nil {
		return nil, err
	}

	result := make([]Group, 0, len(ids))
	seen := make(map[ID]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, errors.New("invalid group id")
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("duplicate group id %d", id)
		}
		seen[id] = struct{}{}

		item, err := s.byID(ctx, id)
		if err != nil {
			return nil, err
		}
		if item.IsSuper {
			if err := s.requirePrivileged(ctx, actor); err != nil {
				return nil, err
			}
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *ApplicationService) Update(
	ctx context.Context,
	actor security.Actor,
	input UpdateInput,
) (Group, error) {
	if err := s.access.Check(ctx, actor, updatePermission); err != nil {
		return Group{}, err
	}
	current, err := s.byID(ctx, input.ID)
	if err != nil {
		return Group{}, err
	}
	if current.IsSuper != input.IsSuper {
		if err := s.requirePrivileged(ctx, actor); err != nil {
			return Group{}, err
		}
	}
	if current.Code == AdminCode && !input.IsSuper {
		return Group{}, ErrProtected
	}
	var permissions *[]permission.Code
	if input.PermissionCodes != nil {
		if current.IsSuper {
			return Group{}, ErrProtected
		}
		if err := s.requirePrivileged(ctx, actor); err != nil {
			return Group{}, err
		}
		normalized, err := s.normalizePermissions(*input.PermissionCodes)
		if err != nil {
			return Group{}, err
		}
		permissions = &normalized
	}
	var siteAccesses *[]SiteAccess
	if input.SiteAccesses != nil {
		if current.IsSuper {
			return Group{}, ErrProtected
		}
		if err := s.requirePrivileged(ctx, actor); err != nil {
			return Group{}, err
		}
		normalized, err := normalizeSiteAccesses(*input.SiteAccesses)
		if err != nil {
			return Group{}, err
		}
		siteAccesses = &normalized
	}
	current.Name = input.Name
	current.IsSuper = input.IsSuper
	current, err = normalize(current)
	if err != nil {
		return Group{}, err
	}
	updated, err := s.repository.Update(
		ctx,
		actor.AuditUserID(),
		current,
		permissions,
		siteAccesses,
	)
	if err != nil {
		return Group{}, fmt.Errorf("update group: %w", err)
	}
	return Clone(updated), nil
}

func (s *ApplicationService) Delete(
	ctx context.Context,
	actor security.Actor,
	id ID,
) error {
	ctx, finishHooks, hookErr := s.beginUserMutation(ctx, actor)
	if hookErr != nil {
		return hookErr
	}
	defer finishHooks()

	if err := s.access.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	item, err := s.byID(ctx, id)
	if err != nil {
		return err
	}
	if item.Code == AdminCode {
		return ErrProtected
	}
	if item.IsSuper {
		if err := s.requirePrivileged(ctx, actor); err != nil {
			return err
		}
	}
	if err := s.repository.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	return nil
}

func (s *ApplicationService) AddUser(
	ctx context.Context,
	actor security.Actor,
	groupID ID,
	userID security.UserID,
) (Membership, error) {
	ctx, finishHooks, hookErr := s.beginUserMutation(ctx, actor)
	if hookErr != nil {
		return Membership{}, hookErr
	}
	defer finishHooks()

	if err := s.access.Check(ctx, actor, updatePermission); err != nil {
		return Membership{}, err
	}
	item, err := s.byID(ctx, groupID)
	if err != nil {
		return Membership{}, err
	}
	if item.IsSuper {
		if err := s.requirePrivileged(ctx, actor); err != nil {
			return Membership{}, err
		}
	}
	membership, err := s.repository.AddUser(
		ctx,
		actor.AuditUserID(),
		groupID,
		userID,
	)
	if err != nil {
		return Membership{}, fmt.Errorf("add user to group: %w", err)
	}
	return cloneMembership(membership), nil
}

func (s *ApplicationService) RemoveUser(
	ctx context.Context,
	actor security.Actor,
	groupID ID,
	userID security.UserID,
) error {
	ctx, finishHooks, hookErr := s.beginUserMutation(ctx, actor)
	if hookErr != nil {
		return hookErr
	}
	defer finishHooks()

	if err := s.access.Check(ctx, actor, updatePermission); err != nil {
		return err
	}
	item, err := s.byID(ctx, groupID)
	if err != nil {
		return err
	}
	if item.IsSuper {
		if err := s.requirePrivileged(ctx, actor); err != nil {
			return err
		}
	}
	if err := s.repository.RemoveUser(ctx, groupID, userID); err != nil {
		return fmt.Errorf("remove user from group: %w", err)
	}
	return nil
}

func (s *ApplicationService) Members(
	ctx context.Context,
	actor security.Actor,
	id ID,
) ([]Membership, error) {
	if err := s.access.Check(ctx, actor, readPermission); err != nil {
		return nil, err
	}
	if _, err := s.byID(ctx, id); err != nil {
		return nil, err
	}
	items, err := s.repository.Members(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list group members: %w", err)
	}
	result := make([]Membership, len(items))
	for index, item := range items {
		result[index] = cloneMembership(item)
	}
	return result, nil
}

func (s *ApplicationService) GroupsForUser(
	ctx context.Context,
	actor security.Actor,
	userID security.UserID,
) ([]Group, error) {
	if err := s.access.Check(ctx, actor, readPermission); err != nil {
		return nil, err
	}
	items, err := s.repository.GroupsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list user groups: %w", err)
	}
	result := make([]Group, len(items))
	for index, item := range items {
		result[index] = Clone(item)
	}
	return result, nil
}

func (s *ApplicationService) ReplaceUserGroups(
	ctx context.Context,
	actor security.Actor,
	userID security.UserID,
	ids []ID,
) error {
	ctx, finishHooks, hookErr := s.beginUserMutation(ctx, actor)
	if hookErr != nil {
		return hookErr
	}
	defer finishHooks()

	if userID <= 0 {
		return errors.New("invalid user id")
	}
	requested, err := s.ValidateUserAssignment(ctx, actor, ids)
	if err != nil {
		return err
	}
	current, err := s.repository.GroupsForUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("list current user groups: %w", err)
	}
	if hasGroupCode(current, AdminCode) != hasGroupCode(requested, AdminCode) {
		if err := s.requirePrivileged(ctx, actor); err != nil {
			return err
		}
	}
	if err := s.repository.ReplaceUserGroups(
		ctx,
		actor.AuditUserID(),
		userID,
		ids,
	); err != nil {
		return fmt.Errorf("replace user groups: %w", err)
	}
	return nil
}

func (s *ApplicationService) GrantPermission(
	ctx context.Context,
	actor security.Actor,
	id ID,
	code permission.Code,
) (PermissionGrant, error) {
	if err := s.access.Check(ctx, actor, updatePermission); err != nil {
		return PermissionGrant{}, err
	}
	if err := s.requirePrivileged(ctx, actor); err != nil {
		return PermissionGrant{}, err
	}
	if !containsCode(s.access.Codes(), code) {
		return PermissionGrant{}, fmt.Errorf(
			"%w: %s",
			permission.ErrUnknown,
			code,
		)
	}
	item, err := s.byID(ctx, id)
	if err != nil {
		return PermissionGrant{}, err
	}
	if item.Code == AdminCode {
		return PermissionGrant{}, ErrProtected
	}
	grant, err := s.repository.GrantPermission(
		ctx,
		actor.AuditUserID(),
		id,
		code,
	)
	if err != nil {
		return PermissionGrant{}, fmt.Errorf("grant group permission: %w", err)
	}
	return clonePermission(grant), nil
}

func (s *ApplicationService) RevokePermission(
	ctx context.Context,
	actor security.Actor,
	id ID,
	code permission.Code,
) error {
	if err := s.access.Check(ctx, actor, updatePermission); err != nil {
		return err
	}
	if err := s.requirePrivileged(ctx, actor); err != nil {
		return err
	}
	if !containsCode(s.access.Codes(), code) {
		return fmt.Errorf("%w: %s", permission.ErrUnknown, code)
	}
	item, err := s.byID(ctx, id)
	if err != nil {
		return err
	}
	if item.Code == AdminCode {
		return ErrProtected
	}
	if err := s.repository.RevokePermission(ctx, id, code); err != nil {
		return fmt.Errorf("revoke group permission: %w", err)
	}
	return nil
}

func (s *ApplicationService) Permissions(
	ctx context.Context,
	actor security.Actor,
	id ID,
) ([]PermissionGrant, error) {
	if err := s.access.Check(ctx, actor, readPermission); err != nil {
		return nil, err
	}
	if _, err := s.byID(ctx, id); err != nil {
		return nil, err
	}
	items, err := s.repository.Permissions(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list group permissions: %w", err)
	}
	result := make([]PermissionGrant, len(items))
	for index, item := range items {
		result[index] = clonePermission(item)
	}
	return result, nil
}

func (s *ApplicationService) SiteAccesses(
	ctx context.Context,
	actor security.Actor,
	id ID,
) ([]SiteAccess, error) {
	if err := s.access.Check(ctx, actor, readPermission); err != nil {
		return nil, err
	}
	if _, err := s.byID(ctx, id); err != nil {
		return nil, err
	}
	items, err := s.repository.SiteAccesses(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list group site access: %w", err)
	}
	return cloneSiteAccesses(items), nil
}

func normalizeSiteAccesses(source []SiteAccess) ([]SiteAccess, error) {
	result := make([]SiteAccess, 0, len(source))
	seen := make(map[site.ID]struct{}, len(source))
	for _, item := range source {
		if item.SiteID <= 0 {
			return nil, fmt.Errorf("%w: invalid site id", ErrInvalidReference)
		}
		if _, exists := seen[item.SiteID]; exists {
			return nil, fmt.Errorf("%w: duplicate site id %d", ErrInvalidReference, item.SiteID)
		}
		seen[item.SiteID] = struct{}{}
		if item.CanDelete {
			item.CanEdit = true
		}
		if item.CanEdit {
			item.CanView = true
		}
		if !item.CanView {
			continue
		}
		item.GroupID = 0
		item.CreatedAt = time.Time{}
		item.UpdatedAt = time.Time{}
		item.CreatedBy = nil
		item.UpdatedBy = nil
		result = append(result, item)
	}
	return result, nil
}

func cloneSiteAccesses(source []SiteAccess) []SiteAccess {
	result := make([]SiteAccess, len(source))
	for index, item := range source {
		item.CreatedBy = cloneUserID(item.CreatedBy)
		item.UpdatedBy = cloneUserID(item.UpdatedBy)
		result[index] = item
	}
	return result
}

func (s *ApplicationService) byID(
	ctx context.Context,
	id ID,
) (Group, error) {
	if id <= 0 {
		return Group{}, errors.New("invalid group id")
	}
	item, err := s.repository.ByID(ctx, id)
	if err != nil {
		return Group{}, fmt.Errorf("get group: %w", err)
	}
	return Clone(item), nil
}

func (s *ApplicationService) requirePrivileged(
	ctx context.Context,
	actor security.Actor,
) error {
	privileged, err := s.access.IsPrivileged(ctx, actor)
	if err != nil {
		return err
	}
	if !privileged {
		return access.ErrNotPrivileged
	}
	return nil
}

func normalize(item Group) (Group, error) {
	item.Code = strings.ToLower(strings.TrimSpace(item.Code))
	item.Name = strings.TrimSpace(item.Name)
	if !codePattern.MatchString(item.Code) {
		return Group{}, errors.New("invalid group code")
	}
	if item.Name == "" {
		return Group{}, errors.New("group name is empty")
	}
	return item, nil
}

func containsCode(codes []permission.Code, wanted permission.Code) bool {
	for _, code := range codes {
		if code == wanted {
			return true
		}
	}
	return false
}

func (s *ApplicationService) normalizePermissions(
	codes []permission.Code,
) ([]permission.Code, error) {
	result := make([]permission.Code, 0, len(codes))
	seen := make(map[permission.Code]struct{}, len(codes))
	for _, code := range codes {
		if !containsCode(s.access.Codes(), code) {
			return nil, fmt.Errorf("%w: %s", permission.ErrUnknown, code)
		}
		if _, exists := seen[code]; exists {
			return nil, fmt.Errorf("duplicate permission %s", code)
		}
		seen[code] = struct{}{}
		result = append(result, code)
	}
	return result, nil
}

func hasGroupCode(items []Group, code string) bool {
	for _, item := range items {
		if item.Code == code {
			return true
		}
	}
	return false
}

func cloneMembership(item Membership) Membership {
	item.CreatedBy = cloneUserID(item.CreatedBy)
	item.UpdatedBy = cloneUserID(item.UpdatedBy)
	return item
}

func clonePermission(item PermissionGrant) PermissionGrant {
	item.CreatedBy = cloneUserID(item.CreatedBy)
	item.UpdatedBy = cloneUserID(item.UpdatedBy)
	return item
}

var _ Service = (*ApplicationService)(nil)
