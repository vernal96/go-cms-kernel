package core

import (
	"context"

	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
)

// Core composes the owner lifecycles; the filesystem service knows neither
// resources nor users. The adapter keeps all writes in one physical transaction.
type cascadeFiles struct {
	file.ManagementService
	cascade   file.CascadeService
	resources *resource.Service
	hooks     *entityhooks.Registry
}

func (s *cascadeFiles) DeleteImpact(ctx context.Context, actor security.Actor, items []file.ItemReference) (file.DeleteImpact, error) {
	return s.cascade.DeleteImpact(ctx, actor, items)
}

func (s *cascadeFiles) DeleteConfirmed(ctx context.Context, actor security.Actor, items []file.ItemReference, token string) error {
	ctx, release, err := entityhooks.Begin(ctx, s.hooks, actor, user.OperationMediaCascade)
	if err != nil {
		return err
	}
	defer release()
	return s.resources.WithMediaCascade(ctx, actor, func(ctx context.Context) error {
		return s.cascade.DeleteConfirmed(ctx, actor, items, token)
	})
}
