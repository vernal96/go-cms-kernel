package user

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/vernal96/go-cms-kernel/domainevent"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

const (
	EventCreated          = "user.created"
	EventUpdated          = "user.updated"
	OperationCreate       = "create"
	OperationUpdate       = "update"
	OperationProfile      = "profile"
	OperationPreferences  = "preferences"
	OperationAvatar       = "avatar"
	OperationMediaCascade = "media_cascade"
	OperationPassword     = "password"
	OperationBlock        = "block"
	OperationUnblock      = "unblock"
	OperationGroups       = "groups"
)

type ProfileData struct {
	Login         string      `json:"login"`
	Email         string      `json:"email"`
	Name          string      `json:"name"`
	LastName      *string     `json:"last_name,omitempty"`
	MiddleName    *string     `json:"middle_name,omitempty"`
	Phone         *string     `json:"phone,omitempty"`
	AvatarMediaID *media.ID   `json:"avatar_media_id,omitempty"`
	ColorScheme   ColorScheme `json:"color_scheme"`
	AccentColor   AccentColor `json:"accent_color"`
}
type EventState struct {
	ID        ID          `json:"id"`
	Data      ProfileData `json:"data"`
	GroupIDs  []group.ID  `json:"group_ids"`
	BlockedAt *time.Time  `json:"blocked_at,omitempty"`
	UpdatedAt time.Time   `json:"updated_at"`
}
type EventPayload struct {
	UserID    ID               `json:"user_id"`
	Operation string           `json:"operation"`
	ActorID   *security.UserID `json:"actor_id,omitempty"`
	Before    *EventState      `json:"before,omitempty"`
	After     EventState       `json:"after"`
}
type Change struct {
	Operation string
	Actor     security.Actor
	Before    *EventState
	ID        ID
	Data      ProfileData
	GroupIDs  []group.ID
	// Password exists only during create/change-password preparation. It is
	// never included in an event, log, snapshot or delivery record.
	Password string
}

var (
	BeforeCreate = entityhooks.NewKey[Change]("core", "user.before_create", entityhooks.Application)
	BeforeUpdate = entityhooks.NewKey[Change]("core", "user.before_update", entityhooks.Application)
	AfterCreate  = entityhooks.NewKey[EventPayload]("core", EventCreated, entityhooks.Application)
	AfterUpdate  = entityhooks.NewKey[EventPayload]("core", EventUpdated, entityhooks.Application)
)

func profileData(item User) ProfileData {
	item = Clone(item)
	return ProfileData{Login: item.Login, Email: item.Email, Name: item.Name, LastName: item.LastName, MiddleName: item.MiddleName, Phone: item.Phone, AvatarMediaID: item.AvatarMediaID, ColorScheme: item.ColorScheme, AccentColor: item.AccentColor}
}
func EventSnapshot(item User, groups []group.ID) EventState {
	return EventState{ID: item.ID, Data: profileData(item), GroupIDs: append([]group.ID(nil), groups...), BlockedAt: cloneTime(item.BlockedAt), UpdatedAt: item.UpdatedAt}
}
func applyData(item Record, data ProfileData) Record {
	item.Login = data.Login
	item.Email = data.Email
	item.Name = data.Name
	item.LastName = data.LastName
	item.MiddleName = data.MiddleName
	item.Phone = data.Phone
	item.AvatarMediaID = data.AvatarMediaID
	item.ColorScheme = data.ColorScheme
	item.AccentColor = data.AccentColor
	return item
}

type policyKey struct{}
type mutationPolicy struct {
	service  *ApplicationService
	password string
}

func (s *ApplicationService) beginMutation(ctx context.Context, actor security.Actor, operation, password string) (context.Context, func(), error) {
	ctx, release, err := entityhooks.Begin(ctx, s.hooks, actor, operation)
	if err != nil {
		return nil, nil, err
	}
	return context.WithValue(ctx, policyKey{}, &mutationPolicy{service: s, password: password}), release, nil
}

// PrepareMutation is called by the physical transaction owner with a locked
// before image. It returns only the writable record and membership candidates.
func PrepareMutation(ctx context.Context, before *EventState, next Record, groups []group.ID) (Record, []group.ID, error) {
	invocation := entityhooks.InvocationFrom(ctx)
	if invocation == nil {
		return next, groups, nil
	}
	policy, _ := ctx.Value(policyKey{}).(*mutationPolicy)
	password := ""
	if policy != nil {
		password = policy.password
	}
	change := Change{Operation: invocation.Operation, Actor: invocation.Actor, ID: next.ID, Data: profileData(next.User), GroupIDs: append([]group.ID(nil), groups...), Password: password}
	if before != nil {
		copy := *before
		copy.Data = profileData(applyData(Record{}, before.Data).User)
		copy.GroupIDs = append([]group.ID(nil), before.GroupIDs...)
		copy.BlockedAt = cloneTime(before.BlockedAt)
		change.Before = &copy
	}
	key := BeforeUpdate
	if before == nil {
		key = BeforeCreate
	}
	if err := entityhooks.Before(ctx, invocation.Registry, key, &change, func(value *Change) error {
		if value.Operation != invocation.Operation || value.ID != next.ID || !reflect.DeepEqual(value.Actor, invocation.Actor) || !reflect.DeepEqual(value.Before, before) {
			return fmt.Errorf("user hook changed read-only mutation metadata")
		}
		return nil
	}); err != nil {
		return Record{}, nil, err
	}
	a, b := profileData(next.User), change.Data
	switch invocation.Operation {
	case OperationCreate:
	case OperationUpdate:
		a.Login = b.Login
		a.Email = b.Email
		a.Name = b.Name
		a.LastName = b.LastName
		a.MiddleName = b.MiddleName
		a.Phone = b.Phone
		a.AvatarMediaID = b.AvatarMediaID
	case OperationProfile:
		a.Name = b.Name
		a.LastName = b.LastName
		a.MiddleName = b.MiddleName
		a.Phone = b.Phone
	case OperationPreferences:
		a.ColorScheme = b.ColorScheme
		a.AccentColor = b.AccentColor
	case OperationAvatar:
		a.AvatarMediaID = b.AvatarMediaID
	case OperationPassword, OperationBlock, OperationUnblock, OperationGroups, OperationMediaCascade:
	default:
		return Record{}, nil, fmt.Errorf("unsupported user hook operation %q", invocation.Operation)
	}
	if invocation.Operation != OperationCreate && !reflect.DeepEqual(a, b) {
		return Record{}, nil, fmt.Errorf("user hook changed fields outside %s", invocation.Operation)
	}
	if invocation.Operation != OperationCreate && invocation.Operation != OperationGroups && !reflect.DeepEqual(groups, change.GroupIDs) {
		return Record{}, nil, fmt.Errorf("user hook changed groups outside membership operation")
	}
	if invocation.Operation != OperationCreate && invocation.Operation != OperationPassword && change.Password != password {
		return Record{}, nil, fmt.Errorf("user hook changed password outside password operation")
	}
	next = applyData(next, change.Data)
	var err error
	next, err = normalize(next)
	if err != nil {
		return Record{}, nil, err
	}
	if invocation.Operation == OperationCreate || invocation.Operation == OperationPassword {
		if err := validatePassword(change.Password); err != nil {
			return Record{}, nil, err
		}
		if policy == nil {
			return Record{}, nil, fmt.Errorf("user password mutation policy is unavailable")
		}
		if change.Password != password {
			next.PasswordHash, err = policy.service.hasher.Hash(change.Password)
			if err != nil {
				return Record{}, nil, err
			}
		}
	}
	if invocation.Operation == OperationCreate || invocation.Operation == OperationGroups {
		if policy != nil {
			_, err = policy.service.groups.ValidateUserAssignment(ctx, invocation.Actor, change.GroupIDs)
		} else {
			err = group.ValidateMutationAssignments(ctx, before.GroupIDs, change.GroupIDs)
		}
		if err != nil {
			return Record{}, nil, err
		}
	}
	return next, change.GroupIDs, nil
}

func MutationEvent(ctx context.Context, before *EventState, next EventState) (domainevent.Envelope, []entityhooks.Target, error) {
	name := EventUpdated
	if before == nil {
		name = EventCreated
	}
	payload := EventPayload{UserID: next.ID, Before: before, After: next}
	var targets []entityhooks.Target
	if invocation := entityhooks.InvocationFrom(ctx); invocation != nil {
		payload.Operation = invocation.Operation
		payload.ActorID = invocation.Actor.AuditUserID()
		targets = invocation.Registry.Targets(name)
	}
	event, err := domainevent.New(name, 1, time.Now().UTC(), payload)
	return event, targets, err
}
