package group

import (
	"context"
	"errors"

	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/security"
)

type assignmentPolicyKey struct{}
type assignmentPolicy struct {
	service *ApplicationService
	actor   security.Actor
}

func (s *ApplicationService) beginUserMutation(ctx context.Context, actor security.Actor) (context.Context, func(), error) {
	ctx, release, err := entityhooks.Begin(ctx, s.hooks, actor, "groups")
	if err != nil {
		return nil, nil, err
	}
	return context.WithValue(ctx, assignmentPolicyKey{}, assignmentPolicy{s, actor}), release, nil
}

// ValidateMutationAssignments validates both additions and protected removals
// after user hooks have changed a membership candidate.
func ValidateMutationAssignments(ctx context.Context, before, after []ID) error {
	policy, ok := ctx.Value(assignmentPolicyKey{}).(assignmentPolicy)
	if !ok {
		return errors.New("group mutation policy is unavailable")
	}
	if _, err := policy.service.ValidateUserAssignment(ctx, policy.actor, after); err != nil {
		return err
	}
	remaining := map[ID]bool{}
	for _, id := range after {
		remaining[id] = true
	}
	for _, id := range before {
		if remaining[id] {
			continue
		}
		item, err := policy.service.byID(ctx, id)
		if err != nil {
			return err
		}
		if item.IsSuper {
			if err := policy.service.requirePrivileged(ctx, policy.actor); err != nil {
				return err
			}
		}
	}
	return nil
}
