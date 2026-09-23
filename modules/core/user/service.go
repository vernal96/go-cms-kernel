package user

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

const (
	minPasswordBytes = 12
	maxPasswordBytes = 1024
)

var (
	loginPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{2,63}$`)

	readPermission   = permission.MustCode("core", "user", permission.Read)
	createPermission = permission.MustCode("core", "user", permission.Create)
	updatePermission = permission.MustCode("core", "user", permission.Update)
	blockPermission  = permission.MustCode("core", "user", permission.Delete)
)

type ApplicationService struct {
	hooks      *entityhooks.Registry
	repository Repository
	hasher     PasswordHasher
	media      MediaService
	groups     group.AssignmentValidator
	access     access.Service
}

func NewService(
	repository Repository,
	hasher PasswordHasher,
	mediaService MediaService,
	groupAssignments group.AssignmentValidator,
	accessService access.Service,
	hooks *entityhooks.Registry,
) (*ApplicationService, error) {
	switch {
	case repository == nil:
		return nil, errors.New("user repository is nil")
	case hasher == nil:
		return nil, errors.New("user password hasher is nil")
	case mediaService == nil:
		return nil, errors.New("user media service is nil")
	case groupAssignments == nil:
		return nil, errors.New("user group assignment validator is nil")
	case accessService == nil:
		return nil, errors.New("user access service is nil")
	}

	if hooks == nil {
		return nil, errors.New("entity hook registry is nil")
	}
	return &ApplicationService{
		hooks:      hooks,
		repository: repository,
		hasher:     hasher,
		media:      mediaService,
		groups:     groupAssignments,
		access:     accessService,
	}, nil
}

func (s *ApplicationService) Create(
	ctx context.Context,
	actor security.Actor,
	input CreateInput,
) (User, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationCreate, input.Password)
	if hookErr != nil {
		return User{}, hookErr
	}
	defer finishHooks()

	if err := s.access.Check(ctx, actor, createPermission); err != nil {
		return User{}, err
	}
	if err := validatePassword(input.Password); err != nil {
		return User{}, err
	}

	item, err := normalize(Record{User: User{
		Login:         input.Login,
		Email:         input.Email,
		Name:          input.Name,
		LastName:      input.LastName,
		MiddleName:    input.MiddleName,
		Phone:         input.Phone,
		AvatarMediaID: input.AvatarMediaID,
	}})
	if err != nil {
		return User{}, err
	}

	groupIDs := append([]group.ID(nil), input.GroupIDs...)
	if len(groupIDs) > 0 {
		if _, err := s.groups.ValidateUserAssignment(
			ctx,
			actor,
			groupIDs,
		); err != nil {
			return User{}, fmt.Errorf("validate user groups: %w", err)
		}
	}

	item.PasswordHash, err = s.hasher.Hash(input.Password)
	if err != nil {
		return User{}, fmt.Errorf("hash user password: %w", err)
	}

	created, err := s.repository.Create(
		ctx,
		actor.AuditUserID(),
		item,
		groupIDs,
		s.validateAvatar,
	)
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return Clone(created.User), nil
}

func (s *ApplicationService) Get(
	ctx context.Context,
	actor security.Actor,
	id ID,
) (User, error) {
	if err := s.access.Check(ctx, actor, readPermission); err != nil {
		return User{}, err
	}
	record, err := s.byID(ctx, id)
	if err != nil {
		return User{}, err
	}
	return Clone(record.User), nil
}

func (s *ApplicationService) Current(
	ctx context.Context,
	actor security.Actor,
) (User, error) {
	if ctx == nil {
		return User{}, errors.New("current user context is nil")
	}
	if err := ctx.Err(); err != nil {
		return User{}, err
	}

	id, exists := actor.UserID()
	if !exists {
		return User{}, security.ErrUnauthenticated
	}
	record, err := s.byID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, security.ErrUnauthenticated
		}
		return User{}, err
	}
	if record.BlockedAt != nil {
		return User{}, security.ErrUnauthenticated
	}
	return Clone(record.User), nil
}

func (s *ApplicationService) List(
	ctx context.Context,
	actor security.Actor,
) ([]User, error) {
	if err := s.access.Check(ctx, actor, readPermission); err != nil {
		return nil, err
	}
	records, err := s.repository.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	result := make([]User, len(records))
	for index, record := range records {
		result[index] = Clone(record.User)
	}
	return result, nil
}

func (s *ApplicationService) Update(
	ctx context.Context,
	actor security.Actor,
	input UpdateInput,
) (User, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationUpdate, "")
	if hookErr != nil {
		return User{}, hookErr
	}
	defer finishHooks()

	if err := s.access.Check(ctx, actor, updatePermission); err != nil {
		return User{}, err
	}
	current, err := s.byID(ctx, input.ID)
	if err != nil {
		return User{}, err
	}

	next := cloneRecord(current)
	next.Login = input.Login
	next.Email = input.Email
	next.Name = input.Name
	next.LastName = input.LastName
	next.MiddleName = input.MiddleName
	next.Phone = input.Phone
	if input.UpdateAvatar {
		next.AvatarMediaID = input.AvatarMediaID
	}
	next, err = normalize(next)
	if err != nil {
		return User{}, err
	}

	updated, err := s.repository.Update(
		ctx,
		actor.AuditUserID(),
		current,
		next,
		s.validateAvatar,
	)
	if err != nil {
		return User{}, fmt.Errorf("update user: %w", err)
	}
	return Clone(updated.User), nil
}

func (s *ApplicationService) UpdateCurrent(
	ctx context.Context,
	actor security.Actor,
	input UpdateCurrentInput,
) (User, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationProfile, "")
	if hookErr != nil {
		return User{}, hookErr
	}
	defer finishHooks()

	current, err := s.currentRecord(ctx, actor)
	if err != nil {
		return User{}, err
	}
	next := cloneRecord(current)
	next.Name = input.Name
	next.LastName = input.LastName
	next.MiddleName = input.MiddleName
	next.Phone = input.Phone
	return s.updateRecord(ctx, actor, current, next)
}

func (s *ApplicationService) UpdateCurrentPreferences(
	ctx context.Context,
	actor security.Actor,
	preferences Preferences,
) (User, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationPreferences, "")
	if hookErr != nil {
		return User{}, hookErr
	}
	defer finishHooks()

	current, err := s.currentRecord(ctx, actor)
	if err != nil {
		return User{}, err
	}
	next := cloneRecord(current)
	next.ColorScheme = preferences.ColorScheme
	next.AccentColor = preferences.AccentColor
	return s.updateRecord(ctx, actor, current, next)
}

func (s *ApplicationService) UpdateCurrentAvatar(
	ctx context.Context,
	actor security.Actor,
	avatarMediaID *media.ID,
) (User, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationAvatar, "")
	if hookErr != nil {
		return User{}, hookErr
	}
	defer finishHooks()

	current, err := s.currentRecord(ctx, actor)
	if err != nil {
		return User{}, err
	}
	next := cloneRecord(current)
	next.AvatarMediaID = cloneMediaID(avatarMediaID)
	return s.updateRecord(ctx, actor, current, next)
}

func (s *ApplicationService) ChangePassword(
	ctx context.Context,
	actor security.Actor,
	id ID,
	password string,
) (User, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationPassword, password)
	if hookErr != nil {
		return User{}, hookErr
	}
	defer finishHooks()

	if err := s.access.Check(ctx, actor, updatePermission); err != nil {
		return User{}, err
	}
	if id <= 0 {
		return User{}, errors.New("invalid user id")
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	passwordHash, err := s.hasher.Hash(password)
	if err != nil {
		return User{}, fmt.Errorf("hash user password: %w", err)
	}
	updated, err := s.repository.ChangePassword(
		ctx,
		actor.AuditUserID(),
		id,
		passwordHash,
	)
	if err != nil {
		return User{}, fmt.Errorf("change user password: %w", err)
	}
	return Clone(updated.User), nil
}

func (s *ApplicationService) ChangeCurrentPassword(
	ctx context.Context,
	actor security.Actor,
	currentPassword string,
	newPassword string,
) (User, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationPassword, newPassword)
	if hookErr != nil {
		return User{}, hookErr
	}
	defer finishHooks()

	current, err := s.currentRecord(ctx, actor)
	if err != nil {
		return User{}, err
	}
	valid, _, err := s.hasher.Verify(currentPassword, current.PasswordHash)
	if err != nil {
		return User{}, fmt.Errorf("verify current user password: %w", err)
	}
	if !valid {
		return User{}, ErrInvalidCurrentPassword
	}
	if err := validatePassword(newPassword); err != nil {
		return User{}, err
	}
	passwordHash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return User{}, fmt.Errorf("hash current user password: %w", err)
	}
	updated, err := s.repository.ChangePassword(
		ctx,
		actor.AuditUserID(),
		current.ID,
		passwordHash,
	)
	if err != nil {
		return User{}, fmt.Errorf("change current user password: %w", err)
	}
	return Clone(updated.User), nil
}

func (s *ApplicationService) Block(
	ctx context.Context,
	actor security.Actor,
	id ID,
) (User, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationBlock, "")
	if hookErr != nil {
		return User{}, hookErr
	}
	defer finishHooks()

	if err := s.access.Check(ctx, actor, blockPermission); err != nil {
		return User{}, err
	}
	if id <= 0 {
		return User{}, errors.New("invalid user id")
	}
	if actorID, exists := actor.UserID(); exists && actorID == id {
		return User{}, ErrSelfBlock
	}
	blocked, err := s.repository.Block(
		ctx,
		actor.AuditUserID(),
		id,
	)
	if err != nil {
		return User{}, fmt.Errorf("block user: %w", err)
	}
	return Clone(blocked.User), nil
}

func (s *ApplicationService) Unblock(
	ctx context.Context,
	actor security.Actor,
	id ID,
) (User, error) {
	ctx, finishHooks, hookErr := s.beginMutation(ctx, actor, OperationUnblock, "")
	if hookErr != nil {
		return User{}, hookErr
	}
	defer finishHooks()

	if err := s.access.Check(ctx, actor, updatePermission); err != nil {
		return User{}, err
	}
	if id <= 0 {
		return User{}, errors.New("invalid user id")
	}
	unblocked, err := s.repository.Unblock(
		ctx,
		actor.AuditUserID(),
		id,
	)
	if err != nil {
		return User{}, fmt.Errorf("unblock user: %w", err)
	}
	return Clone(unblocked.User), nil
}

func (s *ApplicationService) Authenticate(
	ctx context.Context,
	input AuthenticateInput,
) (User, error) {
	if ctx == nil {
		return User{}, errors.New("authenticate context is nil")
	}
	if err := ctx.Err(); err != nil {
		return User{}, err
	}

	identifier := strings.ToLower(strings.TrimSpace(input.Identifier))
	if identifier == "" || len(input.Password) > maxPasswordBytes {
		dummyPassword := input.Password
		if len(dummyPassword) > maxPasswordBytes {
			dummyPassword = "invalid-password-too-long"
		}
		_, _, verifyErr := s.hasher.Verify(
			dummyPassword,
			s.hasher.DummyHash(),
		)
		if verifyErr != nil {
			return User{}, fmt.Errorf(
				"verify dummy password: %w",
				verifyErr,
			)
		}
		return User{}, ErrInvalidCredentials
	}

	record, err := s.repository.ByIdentifier(ctx, identifier)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return User{}, fmt.Errorf("load authentication user: %w", err)
		}
		_, _, verifyErr := s.hasher.Verify(
			input.Password,
			s.hasher.DummyHash(),
		)
		if verifyErr != nil {
			return User{}, fmt.Errorf(
				"verify dummy password: %w",
				verifyErr,
			)
		}
		return User{}, ErrInvalidCredentials
	}

	valid, needsRehash, err := s.hasher.Verify(
		input.Password,
		record.PasswordHash,
	)
	if err != nil {
		return User{}, fmt.Errorf("verify user password: %w", err)
	}
	if !valid || record.BlockedAt != nil {
		return User{}, ErrInvalidCredentials
	}

	var passwordHash *string
	if needsRehash {
		hash, err := s.hasher.Hash(input.Password)
		if err != nil {
			return User{}, fmt.Errorf("rehash user password: %w", err)
		}
		passwordHash = &hash
	}

	authenticated, err := s.repository.RecordLogin(
		ctx,
		record.ID,
		record.PasswordHash,
		passwordHash,
	)
	if err != nil {
		return User{}, fmt.Errorf("record user login: %w", err)
	}
	return Clone(authenticated.User), nil
}

func (s *ApplicationService) byID(
	ctx context.Context,
	id ID,
) (Record, error) {
	if id <= 0 {
		return Record{}, errors.New("invalid user id")
	}
	record, err := s.repository.ByID(ctx, id)
	if err != nil {
		return Record{}, fmt.Errorf("get user: %w", err)
	}
	return cloneRecord(record), nil
}

func (s *ApplicationService) currentRecord(
	ctx context.Context,
	actor security.Actor,
) (Record, error) {
	if ctx == nil {
		return Record{}, errors.New("current user context is nil")
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	id, exists := actor.UserID()
	if !exists {
		return Record{}, security.ErrUnauthenticated
	}
	record, err := s.byID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Record{}, security.ErrUnauthenticated
		}
		return Record{}, err
	}
	if record.BlockedAt != nil {
		return Record{}, security.ErrUnauthenticated
	}
	return record, nil
}

func (s *ApplicationService) updateRecord(
	ctx context.Context,
	actor security.Actor,
	current Record,
	next Record,
) (User, error) {
	normalized, err := normalize(next)
	if err != nil {
		return User{}, err
	}
	updated, err := s.repository.Update(
		ctx,
		actor.AuditUserID(),
		current,
		normalized,
		s.validateAvatar,
	)
	if err != nil {
		return User{}, fmt.Errorf("update current user: %w", err)
	}
	return Clone(updated.User), nil
}

func (s *ApplicationService) validateAvatar(
	ctx context.Context,
	id media.ID,
) error {
	resolved, err := s.media.Resolve(ctx, security.System(), id)
	if err != nil {
		if errors.Is(err, media.ErrNotFound) {
			return ErrInvalidReference
		}
		return err
	}
	return ValidateAvatarMediaFile(
		ctx,
		resolved.File,
		media.Usage{Kind: AvatarMediaUsage},
	)
}

func normalize(record Record) (Record, error) {
	record.Login = strings.ToLower(strings.TrimSpace(record.Login))
	record.Email = strings.ToLower(strings.TrimSpace(record.Email))
	record.Name = strings.TrimSpace(record.Name)
	record.LastName = normalizeOptional(record.LastName)
	record.MiddleName = normalizeOptional(record.MiddleName)
	record.Phone = normalizeOptional(record.Phone)
	if record.ColorScheme == "" {
		record.ColorScheme = ColorSchemeSystem
	}
	if record.AccentColor == "" {
		record.AccentColor = AccentColorBlue
	}

	if !loginPattern.MatchString(record.Login) {
		return Record{}, errors.New("invalid user login")
	}
	address, err := mail.ParseAddress(record.Email)
	if err != nil || address.Address != record.Email ||
		len(record.Email) > 254 {
		return Record{}, errors.New("invalid user email")
	}
	if record.Name == "" || utf8.RuneCountInString(record.Name) > 200 {
		return Record{}, errors.New("invalid user name")
	}
	if record.ColorScheme != ColorSchemeLight &&
		record.ColorScheme != ColorSchemeDark &&
		record.ColorScheme != ColorSchemeSystem {
		return Record{}, errors.New("invalid user color scheme")
	}
	if record.AccentColor != AccentColorBlue &&
		record.AccentColor != AccentColorViolet &&
		record.AccentColor != AccentColorIndigo &&
		record.AccentColor != AccentColorEmerald &&
		record.AccentColor != AccentColorAmber &&
		record.AccentColor != AccentColorRose {
		return Record{}, errors.New("invalid user accent color")
	}
	return record, nil
}

func normalizeOptional(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func validatePassword(password string) error {
	size := len(password)
	if size < minPasswordBytes || size > maxPasswordBytes {
		return fmt.Errorf(
			"password must contain between %d and %d bytes",
			minPasswordBytes,
			maxPasswordBytes,
		)
	}
	return nil
}

var _ Service = (*ApplicationService)(nil)
