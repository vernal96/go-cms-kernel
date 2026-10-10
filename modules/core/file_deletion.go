package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type MediaFileDeletions struct {
	repository media.FileDeletionRepository
	services   *Services
	disks      filesystem.Resolver
	owners     []media.FileOccurrenceOwner
	authorizer security.Authorizer
}

func (r *Runtime) MediaFileDeletions() media.FileDeletionService {
	if r == nil || r.services == nil || r.services.FileDeletions == nil {
		return nil
	}
	return r.services.FileDeletions
}

func (s *MediaFileDeletions) Delete(ctx context.Context, actor security.Actor, input media.DeleteFileInput) (media.FileDeletion, error) {
	if input.SiteID <= 0 || input.MediaID <= 0 || input.ExpectedFileID <= 0 || input.ExpectedUpdatedAt.IsZero() {
		return media.FileDeletion{}, media.ErrFileDeleteConflict
	}
	if err := s.authorizer.Check(ctx, actor, permission.Code("core.file.delete")); err != nil {
		return media.FileDeletion{}, err
	}
	if err := s.authorizer.Check(ctx, actor, permission.Code("core.media.delete")); err != nil {
		return media.FileDeletion{}, err
	}
	var result media.FileDeletion
	// Site and resource namespace locks prevent a read-through cache from
	// repopulating an old value between SQL commit and runtime publication.
	tags := []cache.Tag{sitesTag, siteTag(site.ID(input.SiteID)), siteResourcesTag(site.ID(input.SiteID))}
	refs, err := s.repository.FileOccurrences(ctx, input.MediaID)
	if err != nil {
		return media.FileDeletion{}, err
	}
	// Catalog mutations acquire mutationMu before repository cache locks. Keep
	// that ordering here too: preparation owns mutationMu until SQL publication
	// or abort, and the transaction must rediscover exactly this occurrence.
	var siteRef *media.FileOccurrence
	var preparedSite media.PreparedFileOwner
	siteFinished := false
	if len(refs) == 1 && refs[0].OwnerKind == "site" && refs[0].Target == field.ReferenceFile && refs[0].SiteID == input.SiteID {
		ref := refs[0]
		ref.Path = append([]string(nil), ref.Path...)
		siteRef = &ref
		preparedSite, err = s.services.Sites.PrepareFileOccurrence(ctx, actor, ref)
		if err != nil {
			return media.FileDeletion{}, err
		}
		defer func() {
			if !siteFinished && preparedSite.Abort != nil {
				preparedSite.Abort()
			}
		}()
	}
	lockedResources := map[int64]bool{}
	lockedLibraries := map[int64]bool{}
	for _, ref := range refs {
		if ref.OwnerKind == "resource" {
			tags = append(tags, resourceTag(resource.ID(ref.OwnerID)))
			lockedResources[ref.OwnerID] = true
			if ref.LibraryID > 0 {
				tags = append(tags, resourceTag(resource.ID(ref.LibraryID)))
				lockedLibraries[ref.LibraryID] = true
			}
		}
	}
	err = withRepositoryCacheWrite(s.services.cachePolicy, tags, func() error {
		return s.services.Resources.WithMediaCascade(ctx, actor, func(ctx context.Context) error {
			var err error
			result, err = s.repository.DeleteMediaFile(ctx, actor.AuditUserID(), input, func(ctx context.Context, ref *media.FileOccurrence) (media.PreparedFileOwner, error) {
				if siteRef != nil {
					if ref == nil || ref.OwnerKind != siteRef.OwnerKind || ref.OwnerID != siteRef.OwnerID || ref.SiteID != siteRef.SiteID || ref.Container != siteRef.Container || ref.MediaID != siteRef.MediaID || ref.Target != siteRef.Target || ref.LibraryID != siteRef.LibraryID || !slices.Equal(ref.Path, siteRef.Path) {
						return media.PreparedFileOwner{}, media.ErrFileDeleteConflict
					}
					return media.PreparedFileOwner{
						Apply: preparedSite.Apply,
						Publish: func() {
							siteFinished = true
							if preparedSite.Publish != nil {
								preparedSite.Publish()
							}
						},
						Abort: func() {
							if !siteFinished {
								siteFinished = true
								if preparedSite.Abort != nil {
									preparedSite.Abort()
								}
							}
						},
					}, nil
				}
				if ref == nil {
					return media.PreparedFileOwner{}, nil
				}
				if ref.OwnerKind == "resource" && !lockedResources[ref.OwnerID] {
					return media.PreparedFileOwner{}, media.ErrFileDeleteConflict
				}
				if ref.LibraryID > 0 && !lockedLibraries[ref.LibraryID] {
					return media.PreparedFileOwner{}, media.ErrFileDeleteConflict
				}
				if ref.OwnerKind == "site" {
					// A site owner appeared after preflight. Never acquire mutationMu
					// underneath cache locks; retry from a fresh owner snapshot.
					return media.PreparedFileOwner{}, media.ErrFileDeleteConflict
				}
				owners := append([]media.FileOccurrenceOwner{}, s.owners...)
				if runtime, ok := s.services.Sites.RuntimeByID(site.ID(ref.SiteID)); ok {
					for _, module := range runtime.Profile().Modules() {
						if provider, ok := module.(interface {
							FileOccurrenceOwners() []media.FileOccurrenceOwner
						}); ok {
							owners = append(owners, provider.FileOccurrenceOwners()...)
						}
					}
				}
				for _, owner := range owners {
					if owner.Kind() == ref.OwnerKind {
						if err := s.authorizer.Check(ctx, actor, owner.UpdatePermission()); err != nil {
							return media.PreparedFileOwner{}, err
						}
						return media.PreparedFileOwner{Apply: func(ctx context.Context) (*media.ClearedFileReference, error) {
							version, err := owner.ClearFileOccurrence(ctx, actor.AuditUserID(), *ref)
							if err != nil {
								return nil, err
							}
							return &media.ClearedFileReference{FileOccurrence: *ref, OwnerVersion: version}, nil
						}}, nil
					}
				}
				return media.PreparedFileOwner{}, media.ErrFileDeleteUnsupported
			})
			if err == nil {
				s.services.cachePolicy.invalidate(ctx, tags...)
				if result.ClearedReference != nil && result.ClearedReference.OwnerKind == "resource" {
					s.services.cachePolicy.invalidate(ctx, resourceTag(resource.ID(result.ClearedReference.OwnerID)))
				}
			}
			return err
		})
	})
	if err != nil {
		return media.FileDeletion{}, err
	}
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	cleaned, err := s.repository.CleanFileDeletion(cleanupContext, input.SiteID, result.OperationID, s.deletePhysical)
	if err != nil {
		return result, nil
	} // SQL already committed; the durable pending state remains authoritative.
	return cleaned, nil
}

func (s *MediaFileDeletions) Status(ctx context.Context, actor security.Actor, siteID int64, id string) (media.FileDeletion, error) {
	if err := s.authorizer.Check(ctx, actor, permission.Code("core.file.delete")); err != nil {
		return media.FileDeletion{}, err
	}
	return s.repository.FileDeletion(ctx, siteID, id)
}

func (s *MediaFileDeletions) RetryPending(ctx context.Context) error {
	if err := s.services.Sites.Synchronize(ctx); err != nil {
		return err
	}
	items, err := s.repository.PendingFileDeletions(ctx, 100)
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range items {
		if _, err := s.repository.CleanFileDeletion(ctx, item.SiteID, item.OperationID, s.deletePhysical); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *MediaFileDeletions) deletePhysical(ctx context.Context, files []file.File) error {
	var failures []error
	for _, f := range files {
		disk, ok := s.disks.Disk(f.Storage)
		if !ok {
			failures = append(failures, fmt.Errorf("missing disk %q", f.Storage))
			continue
		}
		if err := disk.Delete(ctx, f.Path); err != nil && !errors.Is(err, filesystem.ErrNotFound) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
