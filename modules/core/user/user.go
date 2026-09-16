package user

import (
	"context"
	"errors"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

type ID = security.UserID

type ColorScheme string

const (
	ColorSchemeLight  ColorScheme = "light"
	ColorSchemeDark   ColorScheme = "dark"
	ColorSchemeSystem ColorScheme = "system"
)

type AccentColor string

const (
	AccentColorBlue    AccentColor = "blue"
	AccentColorViolet  AccentColor = "violet"
	AccentColorIndigo  AccentColor = "indigo"
	AccentColorEmerald AccentColor = "emerald"
	AccentColorAmber   AccentColor = "amber"
	AccentColorRose    AccentColor = "rose"
)

const AvatarMediaUsage media.UsageKind = "user.avatar"

var (
	ErrNotFound               = errors.New("user not found")
	ErrConflict               = errors.New("user conflict")
	ErrLoginExists            = &identityConflictError{field: "login"}
	ErrEmailExists            = &identityConflictError{field: "email"}
	ErrInvalidReference       = errors.New("invalid user reference")
	ErrInvalidCredentials     = errors.New("invalid credentials")
	ErrInvalidCurrentPassword = errors.New("invalid current password")
	ErrSelfBlock              = errors.New("cannot block current user")
	ErrLastAdministrator      = errors.New("cannot block last active administrator")
)

type identityConflictError struct {
	field string
}

func (e *identityConflictError) Error() string {
	return "user " + e.field + " already exists"
}

func (*identityConflictError) Unwrap() error {
	return ErrConflict
}

type User struct {
	ID            ID
	Login         string
	Email         string
	Name          string
	LastName      *string
	MiddleName    *string
	Phone         *string
	AvatarMediaID *media.ID
	ColorScheme   ColorScheme
	AccentColor   AccentColor
	LastLoginAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	BlockedAt     *time.Time
	CreatedBy     *security.UserID
	UpdatedBy     *security.UserID
	BlockedBy     *security.UserID
}

type Record struct {
	User
	PasswordHash string
}

type CreateInput struct {
	Login         string
	Email         string
	Password      string
	Name          string
	LastName      *string
	MiddleName    *string
	Phone         *string
	AvatarMediaID *media.ID
	GroupIDs      []group.ID
}

type UpdateInput struct {
	ID            ID
	Login         string
	Email         string
	Name          string
	LastName      *string
	MiddleName    *string
	Phone         *string
	UpdateAvatar  bool
	AvatarMediaID *media.ID
}

type UpdateCurrentInput struct {
	Name       string
	LastName   *string
	MiddleName *string
	Phone      *string
}

type Preferences struct {
	ColorScheme ColorScheme
	AccentColor AccentColor
}

type AuthenticateInput struct {
	Identifier string
	Password   string
}

type ListStatus string

const (
	ListAll     ListStatus = "all"
	ListActive  ListStatus = "active"
	ListBlocked ListStatus = "blocked"
)

type ListQuery struct {
	Search  string
	Status  ListStatus
	Page    int
	PerPage int
}

type Page struct {
	Items []User
	Total int
}

type ValidateAvatarMedia func(context.Context, media.ID) error

type Repository interface {
	Create(
		context.Context,
		*security.UserID,
		Record,
		[]group.ID,
		ValidateAvatarMedia,
	) (Record, error)
	ByID(context.Context, ID) (Record, error)
	ByIdentifier(context.Context, string) (Record, error)
	List(context.Context) ([]Record, error)
	Update(
		context.Context,
		*security.UserID,
		Record,
		Record,
		ValidateAvatarMedia,
	) (Record, error)
	ChangePassword(
		context.Context,
		*security.UserID,
		ID,
		string,
	) (Record, error)
	RecordLogin(context.Context, ID, *string) (Record, error)
	Block(context.Context, *security.UserID, ID) (Record, error)
	Unblock(context.Context, *security.UserID, ID) (Record, error)
}

type ManagementRepository interface {
	Repository
	ListPage(context.Context, ListQuery) (Page, error)
}

type StatisticsRepository interface {
	Statistics(context.Context) (Statistics, error)
}

type Statistics struct {
	Total   int
	Active  int
	Blocked int
}

type PasswordHasher interface {
	Hash(string) (string, error)
	Verify(string, string) (valid bool, needsRehash bool, err error)
	DummyHash() string
}

type PasswordHasherFactory interface {
	Open() (PasswordHasher, error)
}

type MediaService interface {
	Resolve(
		context.Context,
		security.Actor,
		media.ID,
	) (media.ResolvedMedia, error)
}

type Service interface {
	Create(
		context.Context,
		security.Actor,
		CreateInput,
	) (User, error)
	Get(context.Context, security.Actor, ID) (User, error)
	Current(context.Context, security.Actor) (User, error)
	List(context.Context, security.Actor) ([]User, error)
	Update(
		context.Context,
		security.Actor,
		UpdateInput,
	) (User, error)
	UpdateCurrent(context.Context, security.Actor, UpdateCurrentInput) (User, error)
	UpdateCurrentPreferences(context.Context, security.Actor, Preferences) (User, error)
	UpdateCurrentAvatar(context.Context, security.Actor, *media.ID) (User, error)
	ChangePassword(
		context.Context,
		security.Actor,
		ID,
		string,
	) (User, error)
	ChangeCurrentPassword(context.Context, security.Actor, string, string) (User, error)
	Block(context.Context, security.Actor, ID) (User, error)
	Unblock(context.Context, security.Actor, ID) (User, error)
	Authenticate(context.Context, AuthenticateInput) (User, error)
}

func ValidateAvatarMediaFile(
	_ context.Context,
	item file.File,
	_ media.Usage,
) error {
	if len(item.MIMEType) < len("image/") ||
		item.MIMEType[:len("image/")] != "image/" {
		return ErrInvalidReference
	}
	return nil
}

func Clone(item User) User {
	item.LastName = cloneString(item.LastName)
	item.MiddleName = cloneString(item.MiddleName)
	item.Phone = cloneString(item.Phone)
	item.AvatarMediaID = cloneMediaID(item.AvatarMediaID)
	item.LastLoginAt = cloneTime(item.LastLoginAt)
	item.BlockedAt = cloneTime(item.BlockedAt)
	item.CreatedBy = cloneUserID(item.CreatedBy)
	item.UpdatedBy = cloneUserID(item.UpdatedBy)
	item.BlockedBy = cloneUserID(item.BlockedBy)
	return item
}

func cloneRecord(record Record) Record {
	record.User = Clone(record.User)
	return record
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneMediaID(value *media.ID) *media.ID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneUserID(value *security.UserID) *security.UserID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
