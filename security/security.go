package security

import (
	"context"
	"errors"

	"github.com/vernal96/go-cms-kernel/permission"
)

type UserID int64
type ActorKind uint8

const (
	ActorGuest ActorKind = iota
	ActorUser
	ActorSystem
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
)

type Actor struct {
	sessionVersion int64
	kind           ActorKind
	userID         UserID
}

func Guest() Actor {
	return Actor{kind: ActorGuest}
}

func User(id UserID) Actor {
	return Actor{
		kind:   ActorUser,
		userID: id,
	}
}

func System() Actor {
	return Actor{kind: ActorSystem}
}

func (a Actor) Kind() ActorKind {
	return a.kind
}

func (a Actor) IsGuest() bool {
	return a.kind == ActorGuest
}

func (a Actor) IsUser() bool {
	return a.kind == ActorUser
}

func (a Actor) IsSystem() bool {
	return a.kind == ActorSystem
}

func (a Actor) UserID() (UserID, bool) {
	if !a.IsUser() || a.userID <= 0 {
		return 0, false
	}
	return a.userID, true
}

func (a Actor) AuditUserID() *UserID {
	id, exists := a.UserID()
	if !exists {
		return nil
	}
	result := id
	return &result
}

type Authorizer interface {
	Check(context.Context, Actor, permission.Code) error
}
