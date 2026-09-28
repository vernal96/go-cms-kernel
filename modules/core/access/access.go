package access

import (
	"context"
	"errors"
	"time"

	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

var (
	ErrNotPrivileged = errors.New("privileged access required")
)

type Subject struct {
	Exists    bool
	Active    bool
	HasGroups bool
	IsSuper   bool
}

type Grant struct {
	Permission permission.Code
	CreatedAt  time.Time
	UpdatedAt  time.Time
	CreatedBy  *security.UserID
	UpdatedBy  *security.UserID
}

// Authorization contains facts read together for one authorization operation.
// The service owns the policy for applying group and guest permissions.
type Authorization struct {
	Subject          Subject
	GroupPermissions []permission.Code
	GuestPermissions []permission.Code
}

type Repository interface {
	Subject(context.Context, security.UserID) (Subject, error)
	// A nil user ID requests guest permissions only.
	Authorization(context.Context, *security.UserID, []permission.Code) (Authorization, error)
	GuestPermissions(context.Context) ([]Grant, error)
	GrantGuest(
		context.Context,
		*security.UserID,
		permission.Code,
	) (Grant, error)
	RevokeGuest(context.Context, permission.Code) error
}

type AdministratorRepository interface {
	IsAdministrator(context.Context, security.UserID) (bool, error)
}

type Service interface {
	security.Authorizer
	Codes() []permission.Code
	IsPrivileged(context.Context, security.Actor) (bool, error)
	IsAdministrator(context.Context, security.Actor) (bool, error)
	IsGuestSubject(context.Context, security.Actor) (bool, error)
	GuestPermissions(context.Context, security.Actor) ([]Grant, error)
	GrantGuest(
		context.Context,
		security.Actor,
		permission.Code,
	) (Grant, error)
	RevokeGuest(
		context.Context,
		security.Actor,
		permission.Code,
	) error
}
