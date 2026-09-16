package access

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type memoryRepository struct {
	subjects       map[security.UserID]Subject
	group          map[security.UserID]map[permission.Code]bool
	guest          map[permission.Code]Grant
	administrators map[security.UserID]bool
	subjectCall    atomic.Int32
}

func (r *memoryRepository) IsAdministrator(_ context.Context, id security.UserID) (bool, error) {
	return r.administrators[id], nil
}

func (r *memoryRepository) Subject(
	_ context.Context,
	id security.UserID,
) (Subject, error) {
	r.subjectCall.Add(1)
	return r.subjects[id], nil
}

func (r *memoryRepository) GroupAllowed(
	_ context.Context,
	id security.UserID,
	code permission.Code,
) (bool, error) {
	return r.group[id][code], nil
}

func (r *memoryRepository) GuestAllowed(
	_ context.Context,
	code permission.Code,
) (bool, error) {
	_, exists := r.guest[code]
	return exists, nil
}

func (r *memoryRepository) GuestPermissions(
	context.Context,
) ([]Grant, error) {
	result := make([]Grant, 0, len(r.guest))
	for _, grant := range r.guest {
		result = append(result, grant)
	}
	return result, nil
}

func (r *memoryRepository) GrantGuest(
	_ context.Context,
	actorID *security.UserID,
	code permission.Code,
) (Grant, error) {
	grant := Grant{
		Permission: code,
		CreatedBy:  actorID,
		UpdatedBy:  actorID,
	}
	r.guest[code] = grant
	return grant, nil
}

func (r *memoryRepository) RevokeGuest(
	_ context.Context,
	code permission.Code,
) error {
	delete(r.guest, code)
	return nil
}

func newTestService(
	t *testing.T,
	repository *memoryRepository,
) (*ApplicationService, permission.Code) {
	t.Helper()
	definitions, err := permission.Definitions(
		"core",
		[]permission.Entity{{Code: "site"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := permission.NewCatalog(definitions)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repository, catalog)
	if err != nil {
		t.Fatal(err)
	}
	return service, permission.MustCode(
		"core",
		"site",
		permission.Read,
	)
}

func TestAuthorizationSubjects(t *testing.T) {
	t.Parallel()

	repository := &memoryRepository{
		subjects: map[security.UserID]Subject{
			1: {Exists: true, Active: true},
			2: {Exists: true, Active: true, HasGroups: true},
			3: {
				Exists:    true,
				Active:    true,
				HasGroups: true,
				IsSuper:   true,
			},
			4: {Exists: true, Active: false},
		},
		group: map[security.UserID]map[permission.Code]bool{},
		guest: map[permission.Code]Grant{},
	}
	service, code := newTestService(t, repository)
	repository.guest[code] = Grant{Permission: code}
	repository.group[2] = map[permission.Code]bool{code: true}

	tests := []struct {
		name  string
		actor security.Actor
		err   error
	}{
		{name: "system", actor: security.System()},
		{name: "guest", actor: security.Guest()},
		{name: "no groups inherits guest", actor: security.User(1)},
		{name: "group grant", actor: security.User(2)},
		{name: "super", actor: security.User(3)},
		{
			name:  "deleted",
			actor: security.User(4),
			err:   security.ErrUnauthenticated,
		},
		{
			name:  "unknown",
			actor: security.User(999),
			err:   security.ErrUnauthenticated,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := service.Check(
				context.Background(),
				test.actor,
				code,
			)
			if !errors.Is(err, test.err) {
				t.Fatalf("Check error = %v, want %v", err, test.err)
			}
		})
	}
}

func TestAdministratorRequiresExactMembership(t *testing.T) {
	repository := &memoryRepository{subjects: map[security.UserID]Subject{1: {Exists: true, Active: true, IsSuper: true}, 2: {Exists: true, Active: true, HasGroups: true}}, administrators: map[security.UserID]bool{2: true}}
	service, _ := newTestService(t, repository)
	if allowed, err := service.IsAdministrator(context.Background(), security.User(1)); err != nil || allowed {
		t.Fatalf("non-admin super allowed=%v err=%v", allowed, err)
	}
	if allowed, err := service.IsAdministrator(context.Background(), security.User(2)); err != nil || !allowed {
		t.Fatalf("admin allowed=%v err=%v", allowed, err)
	}
	if allowed, err := service.IsAdministrator(context.Background(), security.System()); err != nil || !allowed {
		t.Fatalf("system allowed=%v err=%v", allowed, err)
	}
}

func TestGroupedUserDoesNotInheritGuestAndCatalogWins(t *testing.T) {
	t.Parallel()

	repository := &memoryRepository{
		subjects: map[security.UserID]Subject{
			2: {Exists: true, Active: true, HasGroups: true},
		},
		group: map[security.UserID]map[permission.Code]bool{
			2: {},
		},
		guest: map[permission.Code]Grant{},
	}
	service, code := newTestService(t, repository)
	repository.guest[code] = Grant{Permission: code}

	if err := service.Check(
		context.Background(),
		security.User(2),
		code,
	); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("grouped user error = %v", err)
	}
	calls := repository.subjectCall.Load()
	if err := service.Check(
		context.Background(),
		security.System(),
		"core.site.publish",
	); !errors.Is(err, permission.ErrUnknown) {
		t.Fatalf("unknown permission error = %v", err)
	}
	if repository.subjectCall.Load() != calls {
		t.Fatal("unknown permission reached repository")
	}
}

func TestGuestGrantManagementRequiresPrivilege(t *testing.T) {
	t.Parallel()

	repository := &memoryRepository{
		subjects: map[security.UserID]Subject{
			1: {Exists: true, Active: true, HasGroups: true},
			2: {
				Exists:    true,
				Active:    true,
				HasGroups: true,
				IsSuper:   true,
			},
		},
		group: map[security.UserID]map[permission.Code]bool{},
		guest: map[permission.Code]Grant{},
	}
	service, code := newTestService(t, repository)
	if _, err := service.GrantGuest(
		context.Background(),
		security.User(1),
		code,
	); !errors.Is(err, ErrNotPrivileged) {
		t.Fatalf("non-super grant error = %v", err)
	}
	grant, err := service.GrantGuest(
		context.Background(),
		security.User(2),
		code,
	)
	if err != nil {
		t.Fatal(err)
	}
	if grant.CreatedBy == nil || *grant.CreatedBy != 2 {
		t.Fatalf("grant audit = %#v", grant)
	}
	if err := service.RevokeGuest(
		context.Background(),
		security.System(),
		code,
	); err != nil {
		t.Fatal(err)
	}
}

var _ Repository = (*memoryRepository)(nil)
