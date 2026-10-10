package site

import (
	"context"
	"errors"
	"sync"

	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

type FileDeletionRepository interface {
	ApplyFileDeletionSettings(context.Context, *security.UserID, Site) (Site, error)
}

// PrepareFileOccurrence holds the catalog mutation lock through SQL and atomic
// publication. Nothing visible changes if preparation or persistence fails.
func (c *Catalog) PrepareFileOccurrence(ctx context.Context, actor security.Actor, ref media.FileOccurrence) (media.PreparedFileOwner, error) {
	if err := c.access.Check(ctx, actor, updatePermission); err != nil {
		return media.PreparedFileOwner{}, err
	}
	c.mutationMu.Lock()
	var once sync.Once
	unlock := func() { once.Do(c.mutationMu.Unlock) }
	current := c.snapshot.Load()
	runtime, ok := current.byID[ID(ref.OwnerID)]
	if !ok {
		unlock()
		return media.PreparedFileOwner{}, ErrNotFound
	}
	item := runtime.Site()
	pruned, err := media.PruneFileOccurrence(item.Settings, ref.Path, ref.MediaID)
	if err != nil {
		unlock()
		return media.PreparedFileOwner{}, err
	}
	item.Settings = pruned.(map[string]any)
	blueprint, ok := c.profiles.ProfileBlueprint(item.ProfileCode)
	if !ok {
		unlock()
		return media.PreparedFileOwner{}, ErrInvalid
	}
	nextRuntime, err := NewRuntimeFromBlueprint(ctx, item, blueprint)
	if err != nil {
		unlock()
		return media.PreparedFileOwner{}, err
	}
	next := cloneSnapshot(current, 0)
	next.byID[item.ID] = nextRuntime
	next.byDomain[item.Domain] = nextRuntime
	preparations, err := c.prepareRuntimePlan(ctx, current, next)
	if err != nil {
		unlock()
		return media.PreparedFileOwner{}, err
	}
	repo, ok := c.repository.(FileDeletionRepository)
	if !ok {
		abortRuntimePreparations(preparations)
		unlock()
		return media.PreparedFileOwner{}, errors.New("site file deletion repository unavailable")
	}
	return media.PreparedFileOwner{
		Apply: func(ctx context.Context) (*media.ClearedFileReference, error) {
			stored, err := repo.ApplyFileDeletionSettings(ctx, actor.AuditUserID(), nextRuntime.Site())
			if err != nil {
				return nil, err
			}
			nextRuntime.site.Version = stored.Version
			nextRuntime.site.UpdatedAt = stored.UpdatedAt
			nextRuntime.site.UpdatedBy = stored.UpdatedBy
			return &media.ClearedFileReference{FileOccurrence: ref, OwnerVersion: stored.Version}, nil
		},
		Publish: func() { c.publishRuntimeSnapshot(next, preparations); unlock() },
		Abort:   func() { abortRuntimePreparations(preparations); unlock() },
	}, nil
}
