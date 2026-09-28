package access

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type memoryRepository struct {
	authorizationErr error
	subjects         map[security.UserID]Subject
	group            map[security.UserID]map[permission.Code]bool
	guest            map[permission.Code]Grant
	administrators   map[security.UserID]bool
	subjectCall      atomic.Int32
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

func (r *memoryRepository) Authorization(_ context.Context, id *security.UserID, codes []permission.Code) (Authorization, error) {
	if r.authorizationErr != nil {
		return Authorization{}, r.authorizationErr
	}
	r.subjectCall.Add(1)
	var result Authorization
	if id != nil {
		result.Subject = r.subjects[*id]
	}
	for _, code := range codes {
		if id != nil && r.group[*id][code] {
			result.GroupPermissions = append(result.GroupPermissions, code)
		}
		if _, ok := r.guest[code]; ok {
			result.GuestPermissions = append(result.GuestPermissions, code)
		}
	}
	return result, nil
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

func TestAllowedMatchesChecksAndReadsOnce(t *testing.T) {
	repository := &memoryRepository{
		subjects: map[security.UserID]Subject{
			1: {Exists: true, Active: true},
			2: {Exists: true, Active: true, HasGroups: true},
			3: {Exists: true, Active: true, HasGroups: true, IsSuper: true},
			4: {Exists: true, Active: false, IsSuper: true},
		},
		group: map[security.UserID]map[permission.Code]bool{},
		guest: map[permission.Code]Grant{},
	}
	service, read := newTestService(t, repository)
	update := permission.MustCode("core", "site", permission.Update)
	remove := permission.MustCode("core", "site", permission.Delete)
	repository.group[2] = map[permission.Code]bool{read: true, update: true}
	repository.guest[remove] = Grant{Permission: remove}
	codes := []permission.Code{update, read, update, remove}
	for _, test := range []struct {
		name    string
		actor   security.Actor
		want    []permission.Code
		wantErr error
		calls   int32
	}{
		{"system", security.System(), []permission.Code{update, read, remove}, nil, 0},
		{"guest", security.Guest(), []permission.Code{remove}, nil, 1},
		{"ungrouped", security.User(1), []permission.Code{remove}, nil, 1},
		{"grouped", security.User(2), []permission.Code{update, read}, nil, 1},
		{"super", security.User(3), []permission.Code{update, read, remove}, nil, 1},
		{"blocked super", security.User(4), nil, security.ErrUnauthenticated, 1},
		{"missing", security.User(99), nil, security.ErrUnauthenticated, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := repository.subjectCall.Load()
			got, err := service.Allowed(context.Background(), test.actor, codes)
			if !errors.Is(err, test.wantErr) || !slices.Equal(got, test.want) {
				t.Fatalf("Allowed=%v, %v; want %v, %v", got, err, test.want, test.wantErr)
			}
			if calls := repository.subjectCall.Load() - before; calls != test.calls {
				t.Fatalf("reads=%d, want %d", calls, test.calls)
			}
			for _, code := range codes {
				err := service.Check(context.Background(), test.actor, code)
				wantErr := test.wantErr
				if wantErr == nil && !slices.Contains(test.want, code) {
					wantErr = security.ErrForbidden
				}
				if !errors.Is(err, wantErr) {
					t.Fatalf("Check(%s)=%v, want %v", code, err, wantErr)
				}
			}
		})
	}
	// Each operation observes the latest grants and user state.
	delete(repository.group[2], read)
	if got, err := service.Allowed(context.Background(), security.User(2), []permission.Code{read}); err != nil || len(got) != 0 {
		t.Fatalf("revoked=%v, %v", got, err)
	}
	repository.subjects[2] = Subject{Exists: true, Active: false, HasGroups: true}
	if _, err := service.Allowed(context.Background(), security.User(2), []permission.Code{update}); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatal(err)
	}
	before := repository.subjectCall.Load()
	for _, actor := range []security.Actor{security.System(), security.User(3), security.Guest()} {
		if _, err := service.Allowed(context.Background(), actor, []permission.Code{read, "missing.permission.code"}); !errors.Is(err, permission.ErrUnknown) {
			t.Fatal(err)
		}
	}
	if got, err := service.Allowed(context.Background(), security.Guest(), nil); err != nil || len(got) != 0 {
		t.Fatalf("empty=%v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Allowed(ctx, security.System(), []permission.Code{read}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := service.Allowed(nil, security.System(), []permission.Code{read}); err == nil {
		t.Fatal("nil context accepted")
	}
	if repository.subjectCall.Load() != before {
		t.Fatal("invalid or empty request reached repository")
	}
}

func TestAllowedDoesNotHideRepositoryErrors(t *testing.T) {
	boom := errors.New("authorization unavailable")
	service, code := newTestService(t, &memoryRepository{authorizationErr: boom})
	got, err := service.Allowed(context.Background(), security.User(1), []permission.Code{code})
	if got != nil || !errors.Is(err, boom) {
		t.Fatalf("allowed=%v, %v", got, err)
	}
}
