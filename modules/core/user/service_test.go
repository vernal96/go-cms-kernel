package user

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type testAccess struct{}

func (testAccess) Check(
	context.Context,
	security.Actor,
	permission.Code,
) error {
	return nil
}
func (testAccess) Codes() []permission.Code { return nil }
func (testAccess) IsPrivileged(
	context.Context,
	security.Actor,
) (bool, error) {
	return true, nil
}
func (testAccess) IsAdministrator(context.Context, security.Actor) (bool, error) { return true, nil }
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

type testMedia struct {
	media.Service
}

func (testMedia) Resolve(
	_ context.Context,
	_ security.Actor,
	id media.ID,
) (media.ResolvedMedia, error) {
	if id != 1 {
		return media.ResolvedMedia{}, media.ErrNotFound
	}
	return media.ResolvedMedia{
		Media: media.Media{ID: id, FileID: 7},
		File: file.File{
			ID:       7,
			MIMEType: "image/png",
		},
	}, nil
}

type testGroupAssignments struct {
	validated []group.ID
	err       error
}

func (v *testGroupAssignments) ValidateUserAssignment(
	_ context.Context,
	_ security.Actor,
	ids []group.ID,
) ([]group.Group, error) {
	v.validated = append([]group.ID(nil), ids...)
	if v.err != nil {
		return nil, v.err
	}
	result := make([]group.Group, len(ids))
	for index, id := range ids {
		result[index] = group.Group{ID: id}
	}
	return result, nil
}

type testHasher struct {
	verifyCalls int
	hashCalls   int
}

func (h *testHasher) Hash(password string) (string, error) {
	h.hashCalls++
	return "hash:" + password, nil
}

func (h *testHasher) Verify(
	password string,
	encoded string,
) (bool, bool, error) {
	h.verifyCalls++
	valid := encoded == "hash:"+password ||
		encoded == "old:"+password
	return valid, strings.HasPrefix(encoded, "old:"), nil
}

func (*testHasher) DummyHash() string { return "hash:dummy-password" }

type memoryRepository struct {
	nextID   ID
	records  map[ID]Record
	lastHash *string
	groupIDs []group.ID
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		nextID:  1,
		records: make(map[ID]Record),
	}
}

func (r *memoryRepository) Create(
	ctx context.Context,
	actorID *security.UserID,
	record Record,
	groupIDs []group.ID,
	validate ValidateAvatarMedia,
) (Record, error) {
	for _, existing := range r.records {
		if existing.Login == record.Login {
			return Record{}, ErrLoginExists
		}
		if existing.Email == record.Email {
			return Record{}, ErrEmailExists
		}
	}
	if record.AvatarMediaID != nil {
		if err := validate(ctx, *record.AvatarMediaID); err != nil {
			return Record{}, err
		}
	}
	record.ID = r.nextID
	r.nextID++
	record.CreatedAt = time.Now().UTC()
	record.UpdatedAt = record.CreatedAt
	record.CreatedBy = cloneUserID(actorID)
	record.UpdatedBy = cloneUserID(actorID)
	r.records[record.ID] = cloneRecord(record)
	r.groupIDs = append([]group.ID(nil), groupIDs...)
	return cloneRecord(record), nil
}

func (r *memoryRepository) ByID(
	_ context.Context,
	id ID,
) (Record, error) {
	record, exists := r.records[id]
	if !exists {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

func (r *memoryRepository) ByIdentifier(
	_ context.Context,
	identifier string,
) (Record, error) {
	for _, record := range r.records {
		if record.Login == identifier ||
			record.Email == identifier {
			return cloneRecord(record), nil
		}
	}
	return Record{}, ErrNotFound
}

func (r *memoryRepository) List(context.Context) ([]Record, error) {
	result := make([]Record, 0, len(r.records))
	for _, record := range r.records {
		result = append(result, cloneRecord(record))
	}
	return result, nil
}

func (r *memoryRepository) Update(
	ctx context.Context,
	actorID *security.UserID,
	_ Record,
	record Record,
	validate ValidateAvatarMedia,
) (Record, error) {
	if record.AvatarMediaID != nil {
		if err := validate(ctx, *record.AvatarMediaID); err != nil {
			return Record{}, err
		}
	}
	record.UpdatedBy = cloneUserID(actorID)
	record.UpdatedAt = time.Now().UTC()
	r.records[record.ID] = cloneRecord(record)
	return cloneRecord(record), nil
}

func (r *memoryRepository) ChangePassword(
	_ context.Context,
	actorID *security.UserID,
	id ID,
	passwordHash string,
) (Record, error) {
	record, exists := r.records[id]
	if !exists {
		return Record{}, ErrNotFound
	}
	record.PasswordHash = passwordHash
	record.UpdatedBy = cloneUserID(actorID)
	record.UpdatedAt = time.Now().UTC()
	r.records[id] = record
	return cloneRecord(record), nil
}

func (r *memoryRepository) RecordLogin(
	_ context.Context,
	id ID,
	passwordHash *string,
) (Record, error) {
	record, exists := r.records[id]
	if !exists {
		return Record{}, ErrNotFound
	}
	now := time.Now().UTC()
	record.LastLoginAt = &now
	if passwordHash != nil {
		record.PasswordHash = *passwordHash
		hash := *passwordHash
		r.lastHash = &hash
	}
	r.records[id] = record
	return cloneRecord(record), nil
}

func (r *memoryRepository) Block(
	_ context.Context,
	actorID *security.UserID,
	id ID,
) (Record, error) {
	record, exists := r.records[id]
	if !exists {
		return Record{}, ErrNotFound
	}
	now := time.Now().UTC()
	record.BlockedAt = &now
	record.BlockedBy = cloneUserID(actorID)
	r.records[id] = record
	return cloneRecord(record), nil
}

func (r *memoryRepository) Unblock(
	_ context.Context,
	actorID *security.UserID,
	id ID,
) (Record, error) {
	record, exists := r.records[id]
	if !exists {
		return Record{}, ErrNotFound
	}
	record.BlockedAt = nil
	record.BlockedBy = nil
	record.UpdatedBy = cloneUserID(actorID)
	r.records[id] = record
	return cloneRecord(record), nil
}

func newService(
	t *testing.T,
) (*ApplicationService, *memoryRepository, *testHasher) {
	t.Helper()
	repository := newMemoryRepository()
	hasher := &testHasher{}
	groupAssignments := &testGroupAssignments{}
	service, err := NewService(
		repository,
		hasher,
		testMedia{},
		groupAssignments,
		testAccess{},
		entityhooks.EmptyRegistry(entityhooks.Application, ""),
	)
	if err != nil {
		t.Fatal(err)
	}
	return service, repository, hasher
}

func TestCurrentReturnsOnlyAnActiveAuthenticatedUser(t *testing.T) {
	t.Parallel()

	service, repository, _ := newService(t)
	repository.records[1] = Record{
		User: User{
			ID:    1,
			Login: "active",
			Email: "active@example.test",
			Name:  "Active",
		},
		PasswordHash: "hash:a-valid-password",
	}
	deletedAt := time.Now().UTC()
	repository.records[2] = Record{
		User: User{
			ID:        2,
			Login:     "deleted",
			Email:     "deleted@example.test",
			Name:      "Deleted",
			BlockedAt: &deletedAt,
		},
		PasswordHash: "hash:a-valid-password",
	}

	current, err := service.Current(
		context.Background(),
		security.User(1),
	)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != 1 || current.Login != "active" {
		t.Fatalf("current user = %#v", current)
	}

	for _, actor := range []security.Actor{
		security.Guest(),
		security.System(),
		security.User(2),
		security.User(999),
	} {
		if _, err := service.Current(
			context.Background(),
			actor,
		); !errors.Is(err, security.ErrUnauthenticated) {
			t.Fatalf("actor %#v error = %v", actor, err)
		}
	}
}

func TestCurrentUserCanUpdateProfilePreferencesAndPassword(t *testing.T) {
	t.Parallel()

	service, repository, _ := newService(t)
	avatar := media.ID(1)
	repository.records[1] = Record{
		User: User{
			ID: 1, Login: "profile", Email: "profile@example.test",
			Name: "Before", AvatarMediaID: &avatar,
			ColorScheme: ColorSchemeSystem,
		},
		PasswordHash: "hash:current-password",
	}
	phone := " +7 900 "

	updated, err := service.UpdateCurrent(
		context.Background(),
		security.User(1),
		UpdateCurrentInput{Name: " After ", Phone: &phone},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Login != "profile" || updated.Email != "profile@example.test" ||
		updated.Name != "After" || updated.Phone == nil || *updated.Phone != "+7 900" ||
		updated.AvatarMediaID == nil || *updated.AvatarMediaID != avatar {
		t.Fatalf("updated profile = %#v", updated)
	}

	updated, err = service.Update(
		context.Background(),
		security.System(),
		UpdateInput{
			ID: updated.ID, Login: updated.Login, Email: updated.Email,
			Name: "Admin edit", Phone: updated.Phone,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.AvatarMediaID == nil || *updated.AvatarMediaID != avatar {
		t.Fatalf("admin profile edit cleared avatar: %#v", updated)
	}

	updated, err = service.UpdateCurrentPreferences(
		context.Background(), security.User(1), Preferences{
			ColorScheme: ColorSchemeDark,
			AccentColor: AccentColorViolet,
		},
	)
	if err != nil || updated.ColorScheme != ColorSchemeDark ||
		updated.AccentColor != AccentColorViolet {
		t.Fatalf("updated preferences = %#v, %v", updated, err)
	}
	if _, err := service.UpdateCurrentPreferences(
		context.Background(), security.User(1), Preferences{
			ColorScheme: ColorScheme("sepia"),
			AccentColor: AccentColorViolet,
		},
	); err == nil {
		t.Fatal("invalid color scheme was accepted")
	}
	if _, err := service.UpdateCurrentPreferences(
		context.Background(), security.User(1), Preferences{
			ColorScheme: ColorSchemeDark,
			AccentColor: AccentColor("turquoise"),
		},
	); err == nil {
		t.Fatal("invalid accent color was accepted")
	}

	if _, err := service.ChangeCurrentPassword(
		context.Background(), security.User(1), "wrong-password", "new-valid-password",
	); !errors.Is(err, ErrInvalidCurrentPassword) {
		t.Fatalf("wrong current password error = %v", err)
	}
	if _, err := service.ChangeCurrentPassword(
		context.Background(), security.User(1), "current-password", "new-valid-password",
	); err != nil {
		t.Fatal(err)
	}
	if repository.records[1].PasswordHash != "hash:new-valid-password" {
		t.Fatalf("password hash = %q", repository.records[1].PasswordHash)
	}
}

func TestCreateNormalizesIdentityAndReservesBlockedValues(
	t *testing.T,
) {
	t.Parallel()

	service, _, _ := newService(t)
	avatar := media.ID(1)
	created, err := service.Create(
		context.Background(),
		security.User(99),
		CreateInput{
			Login:         "  Admin.User ",
			Email:         " ADMIN@EXAMPLE.TEST ",
			Password:      "a-valid-password",
			Name:          " Administrator ",
			AvatarMediaID: &avatar,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.Login != "admin.user" ||
		created.Email != "admin@example.test" ||
		created.Name != "Administrator" ||
		created.ColorScheme != ColorSchemeSystem ||
		created.AccentColor != AccentColorBlue ||
		created.CreatedBy == nil ||
		*created.CreatedBy != 99 {
		t.Fatalf("created user = %#v", created)
	}
	if _, err := service.Block(
		context.Background(),
		security.System(),
		created.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(
		context.Background(),
		security.System(),
		CreateInput{
			Login:    "admin.user",
			Email:    "another@example.test",
			Password: "another-password",
			Name:     "Replacement",
		},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("reserved login error = %v", err)
	}
}

func TestBlockRejectsCurrentUser(t *testing.T) {
	t.Parallel()
	service, _, _ := newService(t)
	created, err := service.Create(context.Background(), security.System(), CreateInput{
		Login: "self-block", Email: "self-block@example.test", Password: "a-valid-password", Name: "Self Block",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Block(context.Background(), security.User(created.ID), created.ID); !errors.Is(err, ErrSelfBlock) {
		t.Fatalf("self block error = %v", err)
	}
}

func TestCreateValidatesAndPersistsGroupAssignments(t *testing.T) {
	t.Parallel()

	repository := newMemoryRepository()
	hasher := &testHasher{}
	assignments := &testGroupAssignments{}
	service, err := NewService(
		repository,
		hasher,
		testMedia{},
		assignments,
		testAccess{},
		entityhooks.EmptyRegistry(entityhooks.Application, ""),
	)
	if err != nil {
		t.Fatal(err)
	}

	groupIDs := []group.ID{2, 7}
	if _, err := service.Create(
		context.Background(),
		security.System(),
		CreateInput{
			Login:    "grouped-user",
			Email:    "grouped@example.test",
			Password: "a-valid-password",
			Name:     "Grouped User",
			GroupIDs: groupIDs,
		},
	); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(assignments.validated, groupIDs) {
		t.Fatalf("validated groups = %#v", assignments.validated)
	}
	if !slices.Equal(repository.groupIDs, groupIDs) {
		t.Fatalf("persisted groups = %#v", repository.groupIDs)
	}

	assignments.err = group.ErrNotFound
	if _, err := service.Create(
		context.Background(),
		security.System(),
		CreateInput{
			Login:    "missing-group",
			Email:    "missing-group@example.test",
			Password: "a-valid-password",
			Name:     "Missing Group",
			GroupIDs: []group.ID{99},
		},
	); !errors.Is(err, group.ErrNotFound) {
		t.Fatalf("missing group error = %v", err)
	}
}

func TestCreateDistinguishesLoginAndEmailConflicts(t *testing.T) {
	t.Parallel()

	service, _, _ := newService(t)
	input := CreateInput{
		Login:    "existing-user",
		Email:    "existing@example.test",
		Password: "a-valid-password",
		Name:     "Existing User",
	}
	if _, err := service.Create(
		context.Background(),
		security.System(),
		input,
	); err != nil {
		t.Fatal(err)
	}

	input.Email = "other@example.test"
	if _, err := service.Create(
		context.Background(),
		security.System(),
		input,
	); !errors.Is(err, ErrLoginExists) ||
		!errors.Is(err, ErrConflict) {
		t.Fatalf("login conflict = %v", err)
	}

	input.Login = "other-user"
	input.Email = "existing@example.test"
	if _, err := service.Create(
		context.Background(),
		security.System(),
		input,
	); !errors.Is(err, ErrEmailExists) ||
		!errors.Is(err, ErrConflict) {
		t.Fatalf("email conflict = %v", err)
	}
}

func TestCreateRejectsInvalidIdentityAndPassword(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		input CreateInput
		want  string
	}{
		{
			name: "login",
			input: CreateInput{
				Login:    "1invalid",
				Email:    "user@example.test",
				Password: "a-valid-password",
				Name:     "User",
			},
			want: "invalid user login",
		},
		{
			name: "email",
			input: CreateInput{
				Login:    "valid-user",
				Email:    "not-an-email",
				Password: "a-valid-password",
				Name:     "User",
			},
			want: "invalid user email",
		},
		{
			name: "password",
			input: CreateInput{
				Login:    "valid-user",
				Email:    "user@example.test",
				Password: "too-short",
				Name:     "User",
			},
			want: "password must contain between 12 and 1024 bytes",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service, _, _ := newService(t)
			if _, err := service.Create(
				context.Background(),
				security.System(),
				test.input,
			); err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAuthenticateUsesGenericErrorsDummyHashAndRehashes(
	t *testing.T,
) {
	t.Parallel()

	service, repository, hasher := newService(t)
	repository.records[1] = Record{
		User: User{
			ID:    1,
			Login: "active",
			Email: "active@example.test",
			Name:  "Active",
		},
		PasswordHash: "old:a-valid-password",
	}
	deletedAt := time.Now().UTC()
	repository.records[2] = Record{
		User: User{
			ID:        2,
			Login:     "deleted",
			Email:     "deleted@example.test",
			Name:      "Deleted",
			BlockedAt: &deletedAt,
		},
		PasswordHash: "hash:a-valid-password",
	}

	unknownErr := authenticateError(
		t,
		service,
		"missing",
		"a-valid-password",
	)
	wrongErr := authenticateError(
		t,
		service,
		"active",
		"wrong-password",
	)
	deletedErr := authenticateError(
		t,
		service,
		"deleted",
		"a-valid-password",
	)
	for _, err := range []error{unknownErr, wrongErr, deletedErr} {
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("credential error = %v", err)
		}
	}
	if hasher.verifyCalls < 3 {
		t.Fatalf("verify calls = %d", hasher.verifyCalls)
	}

	authenticated, err := service.Authenticate(
		context.Background(),
		AuthenticateInput{
			Identifier: " ACTIVE@EXAMPLE.TEST ",
			Password:   "a-valid-password",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.LastLoginAt == nil ||
		repository.lastHash == nil ||
		*repository.lastHash != "hash:a-valid-password" {
		t.Fatalf(
			"authentication result = %#v, rehash = %#v",
			authenticated,
			repository.lastHash,
		)
	}
}

func authenticateError(
	t *testing.T,
	service *ApplicationService,
	identifier string,
	password string,
) error {
	t.Helper()
	_, err := service.Authenticate(
		context.Background(),
		AuthenticateInput{
			Identifier: identifier,
			Password:   password,
		},
	)
	return err
}

var _ access.Service = testAccess{}
var _ Repository = (*memoryRepository)(nil)
