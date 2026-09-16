package group

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type testAccess struct {
	privileged bool
	codes      []permission.Code
	checks     map[permission.Code]error
}

func (a testAccess) Check(
	_ context.Context,
	_ security.Actor,
	code permission.Code,
) error {
	return a.checks[code]
}
func (a testAccess) Codes() []permission.Code {
	return append([]permission.Code(nil), a.codes...)
}
func (a testAccess) IsPrivileged(
	_ context.Context,
	actor security.Actor,
) (bool, error) {
	return actor.IsSystem() || a.privileged, nil
}
func (a testAccess) IsAdministrator(context.Context, security.Actor) (bool, error) {
	return a.privileged, nil
}
func (testAccess) IsGuestSubject(
	context.Context,
	security.Actor,
) (bool, error) {
	return false, nil
}
func (testAccess) GuestPermissions(
	context.Context,
	security.Actor,
) ([]access.Grant, error) {
	return nil, nil
}
func (testAccess) GrantGuest(
	context.Context,
	security.Actor,
	permission.Code,
) (access.Grant, error) {
	return access.Grant{}, nil
}
func (testAccess) RevokeGuest(
	context.Context,
	security.Actor,
	permission.Code,
) error {
	return nil
}

type memoryRepository struct {
	nextID      ID
	groups      map[ID]Group
	memberships map[ID]map[security.UserID]Membership
	permissions map[ID]map[permission.Code]PermissionGrant
	siteAccess  map[ID]map[site.ID]SiteAccess
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		nextID:      1,
		groups:      make(map[ID]Group),
		memberships: make(map[ID]map[security.UserID]Membership),
		permissions: make(map[ID]map[permission.Code]PermissionGrant),
		siteAccess:  make(map[ID]map[site.ID]SiteAccess),
	}
}

func (r *memoryRepository) Create(
	_ context.Context,
	actorID *security.UserID,
	item Group,
	permissions []permission.Code,
	siteAccesses []SiteAccess,
) (Group, error) {
	for _, existing := range r.groups {
		if existing.Code == item.Code {
			return Group{}, ErrConflict
		}
	}
	item.ID = r.nextID
	r.nextID++
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	item.CreatedBy = cloneUserID(actorID)
	item.UpdatedBy = cloneUserID(actorID)
	r.groups[item.ID] = Clone(item)
	r.permissions[item.ID] = make(map[permission.Code]PermissionGrant)
	for _, code := range permissions {
		r.permissions[item.ID][code] = PermissionGrant{GroupID: item.ID, Permission: code}
	}
	r.siteAccess[item.ID] = make(map[site.ID]SiteAccess)
	for _, access := range siteAccesses {
		access.GroupID = item.ID
		r.siteAccess[item.ID][access.SiteID] = access
	}
	return Clone(item), nil
}

func (r *memoryRepository) ByID(
	_ context.Context,
	id ID,
) (Group, error) {
	item, exists := r.groups[id]
	if !exists {
		return Group{}, ErrNotFound
	}
	return Clone(item), nil
}

func (r *memoryRepository) ByCode(
	_ context.Context,
	code string,
) (Group, error) {
	for _, item := range r.groups {
		if item.Code == code {
			return Clone(item), nil
		}
	}
	return Group{}, ErrNotFound
}

func (r *memoryRepository) List(context.Context) ([]Group, error) {
	result := make([]Group, 0, len(r.groups))
	for _, item := range r.groups {
		result = append(result, Clone(item))
	}
	return result, nil
}

func (r *memoryRepository) Update(
	_ context.Context,
	actorID *security.UserID,
	item Group,
	permissions *[]permission.Code,
	siteAccesses *[]SiteAccess,
) (Group, error) {
	if _, exists := r.groups[item.ID]; !exists {
		return Group{}, ErrNotFound
	}
	item.UpdatedAt = time.Now().UTC()
	item.UpdatedBy = cloneUserID(actorID)
	r.groups[item.ID] = Clone(item)
	if permissions != nil {
		r.permissions[item.ID] = make(map[permission.Code]PermissionGrant)
		for _, code := range *permissions {
			r.permissions[item.ID][code] = PermissionGrant{GroupID: item.ID, Permission: code}
		}
	}
	if siteAccesses != nil {
		r.siteAccess[item.ID] = make(map[site.ID]SiteAccess)
		for _, access := range *siteAccesses {
			access.GroupID = item.ID
			r.siteAccess[item.ID][access.SiteID] = access
		}
	}
	return Clone(item), nil
}

func (r *memoryRepository) Delete(
	_ context.Context,
	id ID,
) error {
	if _, exists := r.groups[id]; !exists {
		return ErrNotFound
	}
	delete(r.groups, id)
	delete(r.memberships, id)
	delete(r.permissions, id)
	delete(r.siteAccess, id)
	return nil
}

func (r *memoryRepository) SiteAccesses(_ context.Context, groupID ID) ([]SiteAccess, error) {
	result := make([]SiteAccess, 0, len(r.siteAccess[groupID]))
	for _, item := range r.siteAccess[groupID] {
		result = append(result, item)
	}
	return result, nil
}

func (r *memoryRepository) EffectiveSiteIDs(_ context.Context, userID security.UserID, action SiteAccessAction) ([]site.ID, error) {
	seen := make(map[site.ID]struct{})
	for groupID, members := range r.memberships {
		if _, exists := members[userID]; !exists {
			continue
		}
		for siteID, item := range r.siteAccess[groupID] {
			if siteAccessAllows(item, action) {
				seen[siteID] = struct{}{}
			}
		}
	}
	result := make([]site.ID, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	return result, nil
}

func (r *memoryRepository) UserHasSiteAccess(ctx context.Context, userID security.UserID, siteID site.ID, action SiteAccessAction) (bool, error) {
	ids, err := r.EffectiveSiteIDs(ctx, userID, action)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == siteID {
			return true, nil
		}
	}
	return false, nil
}

func siteAccessAllows(item SiteAccess, action SiteAccessAction) bool {
	switch action {
	case SiteAccessView:
		return item.CanView
	case SiteAccessEdit:
		return item.CanEdit
	case SiteAccessDelete:
		return item.CanDelete
	default:
		return false
	}
}

func (r *memoryRepository) AddUser(
	_ context.Context,
	actorID *security.UserID,
	groupID ID,
	userID security.UserID,
) (Membership, error) {
	if _, exists := r.groups[groupID]; !exists {
		return Membership{}, ErrNotFound
	}
	if r.memberships[groupID] == nil {
		r.memberships[groupID] = make(map[security.UserID]Membership)
	}
	item := Membership{
		UserID:    userID,
		GroupID:   groupID,
		CreatedAt: time.Now().UTC(),
		CreatedBy: cloneUserID(actorID),
		UpdatedBy: cloneUserID(actorID),
	}
	item.UpdatedAt = item.CreatedAt
	r.memberships[groupID][userID] = item
	return item, nil
}

func (r *memoryRepository) RemoveUser(
	_ context.Context,
	groupID ID,
	userID security.UserID,
) error {
	delete(r.memberships[groupID], userID)
	return nil
}

func (r *memoryRepository) Members(
	_ context.Context,
	groupID ID,
) ([]Membership, error) {
	result := make([]Membership, 0, len(r.memberships[groupID]))
	for _, item := range r.memberships[groupID] {
		result = append(result, item)
	}
	return result, nil
}

func (r *memoryRepository) GroupsForUser(
	_ context.Context,
	userID security.UserID,
) ([]Group, error) {
	var result []Group
	for groupID, members := range r.memberships {
		if _, exists := members[userID]; exists {
			result = append(result, Clone(r.groups[groupID]))
		}
	}
	return result, nil
}

func (r *memoryRepository) ReplaceUserGroups(
	_ context.Context,
	actorID *security.UserID,
	userID security.UserID,
	groupIDs []ID,
) error {
	for groupID, members := range r.memberships {
		delete(members, userID)
		r.memberships[groupID] = members
	}
	for _, groupID := range groupIDs {
		if _, exists := r.groups[groupID]; !exists {
			return ErrInvalidReference
		}
		if r.memberships[groupID] == nil {
			r.memberships[groupID] = make(map[security.UserID]Membership)
		}
		r.memberships[groupID][userID] = Membership{UserID: userID, GroupID: groupID, CreatedBy: cloneUserID(actorID)}
	}
	return nil
}

func (r *memoryRepository) GrantPermission(
	_ context.Context,
	actorID *security.UserID,
	groupID ID,
	code permission.Code,
) (PermissionGrant, error) {
	if r.permissions[groupID] == nil {
		r.permissions[groupID] = make(
			map[permission.Code]PermissionGrant,
		)
	}
	item := PermissionGrant{
		GroupID:    groupID,
		Permission: code,
		CreatedAt:  time.Now().UTC(),
		CreatedBy:  cloneUserID(actorID),
		UpdatedBy:  cloneUserID(actorID),
	}
	item.UpdatedAt = item.CreatedAt
	r.permissions[groupID][code] = item
	return item, nil
}

func (r *memoryRepository) RevokePermission(
	_ context.Context,
	groupID ID,
	code permission.Code,
) error {
	delete(r.permissions[groupID], code)
	return nil
}

func (r *memoryRepository) Permissions(
	_ context.Context,
	groupID ID,
) ([]PermissionGrant, error) {
	result := make(
		[]PermissionGrant,
		0,
		len(r.permissions[groupID]),
	)
	for _, item := range r.permissions[groupID] {
		result = append(result, item)
	}
	return result, nil
}

func TestSuperGroupChangesAndMembershipRequirePrivilege(t *testing.T) {
	t.Parallel()

	repository := newMemoryRepository()
	service, err := NewService(repository, testAccess{}, entityhooks.EmptyRegistry(entityhooks.Application, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(
		context.Background(),
		security.User(1),
		CreateInput{
			Code:    "admin",
			Name:    "Administrator",
			IsSuper: true,
		},
	); !errors.Is(err, access.ErrNotPrivileged) {
		t.Fatalf("non-super create error = %v", err)
	}
	admin, err := service.Create(
		context.Background(),
		security.System(),
		CreateInput{
			Code:    "ADMIN",
			Name:    "Administrator",
			IsSuper: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Code != "admin" {
		t.Fatalf("normalized code = %q", admin.Code)
	}
	if _, err := service.AddUser(
		context.Background(),
		security.User(1),
		admin.ID,
		7,
	); !errors.Is(err, access.ErrNotPrivileged) {
		t.Fatalf("non-super membership error = %v", err)
	}
	membership, err := service.AddUser(
		context.Background(),
		security.System(),
		admin.ID,
		7,
	)
	if err != nil || membership.UserID != 7 {
		t.Fatalf("system membership = %#v, %v", membership, err)
	}
}

func TestValidateUserAssignmentRequiresUpdateAndPrivilege(t *testing.T) {
	t.Parallel()

	repository := newMemoryRepository()
	service, err := NewService(repository, testAccess{}, entityhooks.EmptyRegistry(entityhooks.Application, ""))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := service.Create(
		context.Background(),
		security.System(),
		CreateInput{Code: "manager", Name: "Manager"},
	)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := service.Create(
		context.Background(),
		security.System(),
		CreateInput{
			Code:    "admin",
			Name:    "Administrator",
			IsSuper: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	validated, err := service.ValidateUserAssignment(
		context.Background(),
		security.User(1),
		[]ID{manager.ID},
	)
	if err != nil || len(validated) != 1 ||
		validated[0].ID != manager.ID {
		t.Fatalf("manager assignment = %#v, %v", validated, err)
	}
	if _, err := service.ValidateUserAssignment(
		context.Background(),
		security.User(1),
		[]ID{admin.ID},
	); !errors.Is(err, access.ErrNotPrivileged) {
		t.Fatalf("super assignment error = %v", err)
	}
	if _, err := service.ValidateUserAssignment(
		context.Background(),
		security.System(),
		[]ID{manager.ID, manager.ID},
	); err == nil {
		t.Fatal("duplicate group assignment was accepted")
	}
	if _, err := service.ValidateUserAssignment(
		context.Background(),
		security.System(),
		[]ID{999},
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing group error = %v", err)
	}

	forbidden := errors.New("group update denied")
	deniedService, err := NewService(repository, testAccess{
		checks: map[permission.Code]error{
			updatePermission: forbidden,
		},
	}, entityhooks.EmptyRegistry(entityhooks.Application, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deniedService.ValidateUserAssignment(
		context.Background(),
		security.System(),
		[]ID{manager.ID},
	); !errors.Is(err, forbidden) {
		t.Fatalf("update permission error = %v", err)
	}
}

func TestPermissionGrantsRequirePrivilegeAndKnownCatalogCode(
	t *testing.T,
) {
	t.Parallel()

	code := permission.MustCode("core", "site", permission.Read)
	repository := newMemoryRepository()
	service, err := NewService(repository, testAccess{
		codes: []permission.Code{code},
	}, entityhooks.EmptyRegistry(entityhooks.Application, ""))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := service.Create(
		context.Background(),
		security.System(),
		CreateInput{Code: "manager", Name: "Manager"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GrantPermission(
		context.Background(),
		security.User(1),
		manager.ID,
		code,
	); !errors.Is(err, access.ErrNotPrivileged) {
		t.Fatalf("non-super grant error = %v", err)
	}
	if _, err := service.GrantPermission(
		context.Background(),
		security.System(),
		manager.ID,
		"core.site.publish",
	); !errors.Is(err, permission.ErrUnknown) {
		t.Fatalf("unknown grant error = %v", err)
	}
	grant, err := service.GrantPermission(
		context.Background(),
		security.System(),
		manager.ID,
		code,
	)
	if err != nil || grant.Permission != code {
		t.Fatalf("grant = %#v, %v", grant, err)
	}
}

var _ access.Service = testAccess{}

func TestAdminGroupCannotBeDeletedOrDemoted(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	service, err := NewService(repository, testAccess{}, entityhooks.EmptyRegistry(entityhooks.Application, ""))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := service.Create(context.Background(), security.System(), CreateInput{Code: AdminCode, Name: "Administrator", IsSuper: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), security.System(), admin.ID); !errors.Is(err, ErrProtected) {
		t.Fatalf("delete admin error = %v", err)
	}
	if _, err := service.Update(context.Background(), security.System(), UpdateInput{ID: admin.ID, Name: admin.Name, IsSuper: false}); !errors.Is(err, ErrProtected) {
		t.Fatalf("demote admin error = %v", err)
	}
}

func TestServiceNormalizesAndReplacesSiteAccess(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	service, err := NewService(repository, testAccess{
		privileged: true,
		checks:     map[permission.Code]error{},
	}, entityhooks.EmptyRegistry(entityhooks.Application, ""))
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), security.User(1), CreateInput{
		Code:         "site-manager",
		Name:         "Site manager",
		SiteAccesses: []SiteAccess{{SiteID: 7, CanDelete: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := service.SiteAccesses(context.Background(), security.User(1), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || !grants[0].CanView || !grants[0].CanEdit || !grants[0].CanDelete {
		t.Fatalf("normalized grants = %#v", grants)
	}
	replacement := []SiteAccess{{SiteID: 8, CanEdit: true}}
	if _, err := service.Update(context.Background(), security.User(1), UpdateInput{
		ID: created.ID, Name: created.Name, SiteAccesses: &replacement,
	}); err != nil {
		t.Fatal(err)
	}
	grants, err = service.SiteAccesses(context.Background(), security.User(1), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].SiteID != 8 || !grants[0].CanView || !grants[0].CanEdit || grants[0].CanDelete {
		t.Fatalf("replacement grants = %#v", grants)
	}
}

var _ Repository = (*memoryRepository)(nil)
