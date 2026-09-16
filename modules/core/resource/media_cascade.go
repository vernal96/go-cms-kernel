package resource

import (
	"context"

	"github.com/vernal96/go-cms-kernel/security"
)

// WithMediaCascade pins each affected site's hooks through the enclosing
// filesystem transaction. Cascades may be vetoed, but their derived changes
// cannot be rewritten by a hook.
func (s *Service) WithMediaCascade(ctx context.Context, actor security.Actor, apply func(context.Context) error) error {
	ctx, release, err := s.beginMutation(ctx, actor, OperationMediaCascade)
	if err != nil {
		return err
	}
	defer release()
	return apply(ctx)
}

// MediaCascadeRecordsRevision preserves the site's LibraryItem history policy.
// Tree resource revisions are always recorded, including repository maintenance.
func MediaCascadeRecordsRevision(ctx context.Context, state EventState) bool {
	if state.Data.StorageKind == StorageTree {
		return true
	}
	m := mutationFrom(ctx)
	if m == nil {
		return DefaultRevisionPolicy().LibraryItems
	}
	runtime, ok := m.service.runtime(ctx, state.SiteID)
	return ok && revisionPolicyFor(runtime).LibraryItems
}
