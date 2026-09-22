package core

import (
	"context"
	"errors"
	"hash/fnv"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

const repositoryCacheInvalidationTimeout = 5 * time.Second
const repositoryCacheLockStripes = 64

// repositoryCachePolicy owns core domain cache coherence. Physical target
// registration and cross-store fan-out belong to cache.Manager's coordinator.
type repositoryCachePolicy struct {
	coherence   [repositoryCacheLockStripes]sync.RWMutex
	invalidator cache.Invalidator
}

func newRepositoryCachePolicy(
	invalidator cache.Invalidator,
) *repositoryCachePolicy {
	return &repositoryCachePolicy{invalidator: invalidator}
}

func (p *repositoryCachePolicy) invalidate(
	ctx context.Context,
	tags ...cache.Tag,
) {
	if p == nil || p.invalidator == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	invalidationCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		repositoryCacheInvalidationTimeout,
	)
	defer cancel()
	// Cache invalidation remains fail-open for the authoritative mutation;
	// application cache stores report failures through their observer.
	_ = p.invalidator.Invalidate(invalidationCtx, tags...)
}

func withRepositoryCacheRead[T any](
	policy *repositoryCachePolicy,
	tags []cache.Tag,
	read func() (T, error),
) (T, error) {
	if policy == nil {
		return read()
	}
	locks := policy.lockIndexes(tags)
	for _, index := range locks {
		policy.coherence[index].RLock()
	}
	defer func() {
		for index := len(locks) - 1; index >= 0; index-- {
			policy.coherence[locks[index]].RUnlock()
		}
	}()
	return read()
}

func withRepositoryCacheWrite(
	policy *repositoryCachePolicy,
	tags []cache.Tag,
	write func() error,
) error {
	if policy == nil {
		return write()
	}
	locks := policy.lockIndexes(tags)
	for _, index := range locks {
		policy.coherence[index].Lock()
	}
	defer func() {
		for index := len(locks) - 1; index >= 0; index-- {
			policy.coherence[locks[index]].Unlock()
		}
	}()
	return write()
}

func (*repositoryCachePolicy) lockIndexes(tags []cache.Tag) []int {
	unique := make(map[int]struct{}, len(tags))
	for _, tag := range tags {
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(tag))
		unique[int(hash.Sum32()%repositoryCacheLockStripes)] = struct{}{}
	}
	result := make([]int, 0, len(unique))
	for index := range unique {
		result = append(result, index)
	}
	sort.Ints(result)
	return result
}

type coherentDatabase struct {
	Database
	sites     site.Repository
	resources resource.Repository
	policy    *repositoryCachePolicy
}

func newCoherentDatabase(
	database Database,
	invalidator cache.Invalidator,
) (*coherentDatabase, error) {
	if err := validateDatabase(database); err != nil {
		return nil, err
	}
	policy := newRepositoryCachePolicy(invalidator)
	return &coherentDatabase{
		Database: database,
		sites: &invalidatingSiteRepository{
			base: database.Sites(), policy: policy,
		},
		resources: &invalidatingResourceRepository{
			base: database.Resources(), policy: policy,
		},
		policy: policy,
	}, nil
}

func (d *coherentDatabase) Sites() site.Repository {
	return d.sites
}

func (d *coherentDatabase) Resources() resource.Repository {
	return d.resources
}

type invalidatingSiteRepository struct {
	base   site.Repository
	policy *repositoryCachePolicy
}

func (r *invalidatingSiteRepository) List(
	ctx context.Context,
) ([]site.Site, error) {
	return r.base.List(ctx)
}

func (r *invalidatingSiteRepository) FindByID(
	ctx context.Context,
	id site.ID,
) (site.Site, error) {
	management, ok := r.base.(site.ManagementRepository)
	if !ok {
		return site.Site{}, errors.New("site management repository is unavailable")
	}
	return management.FindByID(ctx, id)
}

func (r *invalidatingSiteRepository) FindByDomain(
	ctx context.Context,
	domain string,
) (site.Site, error) {
	management, ok := r.base.(site.ManagementRepository)
	if !ok {
		return site.Site{}, errors.New("site management repository is unavailable")
	}
	return management.FindByDomain(ctx, domain)
}

func (r *invalidatingSiteRepository) ListPage(
	ctx context.Context,
	query site.ListQuery,
) (site.Page, error) {
	management, ok := r.base.(site.ManagementRepository)
	if !ok {
		return site.Page{}, errors.New("site management repository is unavailable")
	}
	return management.ListPage(ctx, query)
}

func (r *invalidatingSiteRepository) Statistics(
	ctx context.Context,
	query site.StatisticsQuery,
) (site.Statistics, error) {
	statistics, ok := r.base.(site.StatisticsRepository)
	if !ok {
		return site.Statistics{}, errors.New("site statistics repository is unavailable")
	}
	return statistics.Statistics(ctx, query)
}

func (r *invalidatingSiteRepository) Create(
	ctx context.Context,
	actorID *security.UserID,
	item site.Site,
) (site.Site, error) {
	management, ok := r.base.(site.ManagementRepository)
	if !ok {
		return site.Site{}, errors.New("site management repository is unavailable")
	}
	var result site.Site
	err := withRepositoryCacheWrite(r.policy, []cache.Tag{sitesTag}, func() error {
		var err error
		result, err = management.Create(ctx, actorID, item)
		if err != nil {
			return err
		}
		r.policy.invalidate(ctx, sitesTag, siteTag(result.ID))
		return nil
	})
	return result, err
}

func (r *invalidatingSiteRepository) Update(
	ctx context.Context,
	actorID *security.UserID,
	item site.Site,
) (site.Site, error) {
	var result site.Site
	err := withRepositoryCacheWrite(
		r.policy,
		[]cache.Tag{sitesTag, siteTag(item.ID)},
		func() error {
			var err error
			result, err = r.base.Update(ctx, actorID, item)
			if err != nil {
				return err
			}
			r.policy.invalidate(ctx, sitesTag, siteTag(result.ID))
			return nil
		},
	)
	return result, err
}

func (r *invalidatingSiteRepository) Delete(
	ctx context.Context,
	id site.ID,
) error {
	management, ok := r.base.(site.ManagementRepository)
	if !ok {
		return errors.New("site management repository is unavailable")
	}
	return withRepositoryCacheWrite(
		r.policy,
		[]cache.Tag{sitesTag, siteTag(id)},
		func() error {
			if err := management.Delete(ctx, id); err != nil {
				return err
			}
			r.policy.invalidate(ctx, sitesTag, siteTag(id))
			return nil
		},
	)
}

type invalidatingResourceRepository struct {
	base   resource.Repository
	policy *repositoryCachePolicy
}

func (r *invalidatingResourceRepository) revisionRepository() (resource.RevisionRepository, error) {
	repository, ok := r.base.(resource.RevisionRepository)
	if !ok {
		return nil, errors.New("resource revision repository is unavailable")
	}
	return repository, nil
}

func (r *invalidatingResourceRepository) ListRevisions(ctx context.Context, siteID site.ID, id resource.ID, page, perPage int) (resource.RevisionPage, error) {
	repository, err := r.revisionRepository()
	if err != nil {
		return resource.RevisionPage{}, err
	}
	return repository.ListRevisions(ctx, siteID, id, page, perPage)
}
func (r *invalidatingResourceRepository) Revision(ctx context.Context, siteID site.ID, id resource.ID, version int64) (resource.Revision, error) {
	repository, err := r.revisionRepository()
	if err != nil {
		return resource.Revision{}, err
	}
	return repository.Revision(ctx, siteID, id, version)
}
func (r *invalidatingResourceRepository) PurgeRevisions(ctx context.Context, siteID site.ID, id resource.ID) (int64, error) {
	repository, err := r.revisionRepository()
	if err != nil {
		return 0, err
	}
	return repository.PurgeRevisions(ctx, siteID, id)
}
func (r *invalidatingResourceRepository) CountRevisions(ctx context.Context) (int64, error) {
	repository, err := r.revisionRepository()
	if err != nil {
		return 0, err
	}
	return repository.CountRevisions(ctx)
}
func (r *invalidatingResourceRepository) PurgeAllRevisions(ctx context.Context) (int64, error) {
	repository, err := r.revisionRepository()
	if err != nil {
		return 0, err
	}
	return repository.PurgeAllRevisions(ctx)
}
func (r *invalidatingResourceRepository) RestoreRevision(ctx context.Context, actorID *security.UserID, current, candidate resource.Resource, source int64) (resource.Resource, error) {
	repository, err := r.revisionRepository()
	if err != nil {
		return resource.Resource{}, err
	}
	var result resource.Resource
	err = withRepositoryCacheWrite(r.policy, resourceTags(current), func() error {
		var mutationErr error
		result, mutationErr = repository.RestoreRevision(ctx, actorID, current, candidate, source)
		if mutationErr == nil {
			r.policy.invalidate(ctx, append(treeMutationTags(current), treeMutationTags(result)...)...)
		}
		return mutationErr
	})
	return result, err
}

func (r *invalidatingResourceRepository) RestoreLibraryItemRevision(ctx context.Context, actorID *security.UserID, current, candidate resource.LibraryItem, source int64) (resource.LibraryItem, error) {
	repository, err := r.revisionRepository()
	if err != nil {
		return resource.LibraryItem{}, err
	}
	var result resource.LibraryItem
	tags := append(libraryItemTags(current), resourceTag(candidate.LibraryID))
	err = withRepositoryCacheWrite(r.policy, tags, func() error {
		var mutationErr error
		result, mutationErr = repository.RestoreLibraryItemRevision(ctx, actorID, current, candidate, source)
		if mutationErr == nil {
			r.policy.invalidate(ctx, append(append(tags, libraryItemTags(result)...), siteRoutesTag(current.SiteID), siteRoutesTag(result.SiteID))...)
		}
		return mutationErr
	})
	return result, err
}

func (r *invalidatingResourceRepository) Create(
	ctx context.Context,
	actorID *security.UserID,
	item resource.Resource,
	validate resource.ValidateImageMedia,
) (resource.Resource, error) {
	var result resource.Resource
	err := withRepositoryCacheWrite(
		r.policy,
		[]cache.Tag{siteResourcesTag(item.SiteID)},
		func() error {
			var err error
			result, err = r.base.Create(ctx, actorID, item, validate)
			if err != nil {
				return err
			}
			r.policy.invalidate(ctx, append(resourceTags(result), siteRoutesTag(result.SiteID))...)
			return nil
		},
	)
	return result, err
}

func (r *invalidatingResourceRepository) ByID(
	ctx context.Context,
	id resource.ID,
) (resource.Resource, error) {
	return r.base.ByID(ctx, id)
}

func (r *invalidatingResourceRepository) ByPath(
	ctx context.Context,
	siteID site.ID,
	path string,
) (resource.Resource, error) {
	return r.base.ByPath(ctx, siteID, path)
}

func (r *invalidatingResourceRepository) ListBySite(
	ctx context.Context,
	siteID site.ID,
) ([]resource.Resource, error) {
	return r.base.ListBySite(ctx, siteID)
}

func (r *invalidatingResourceRepository) Query(ctx context.Context, query resource.Query) (resource.Page, error) {
	repository, ok := r.base.(resource.QueryRepository)
	if !ok {
		return resource.Page{}, errors.New("resource query repository is unavailable")
	}
	return repository.Query(ctx, query)
}

func (r *invalidatingResourceRepository) ExistsInSite(
	ctx context.Context,
	siteID site.ID,
	id resource.ID,
) (bool, error) {
	management, ok := r.base.(resource.ManagementRepository)
	if !ok {
		return false, errors.New("resource management repository is unavailable")
	}
	return management.ExistsInSite(ctx, siteID, id)
}

func (r *invalidatingResourceRepository) ListChildren(
	ctx context.Context,
	siteID site.ID,
	parentID *resource.ID,
) ([]resource.Child, error) {
	management, ok := r.base.(resource.ManagementRepository)
	if !ok {
		return nil, errors.New("resource management repository is unavailable")
	}
	return management.ListChildren(ctx, siteID, parentID)
}

func (r *invalidatingResourceRepository) TransferToSite(
	ctx context.Context,
	actorID *security.UserID,
	id resource.ID,
	sourceSiteID site.ID,
	targetSiteID site.ID,
	expectedVersion int64,
	expectedSourceProfile string,
	expectedTargetProfile string,
) (resource.SiteTransferResult, error) {
	repository, ok := r.base.(resource.SiteTransferRepository)
	if !ok {
		return resource.SiteTransferResult{}, errors.New("resource site transfer repository is unavailable")
	}
	tags := []cache.Tag{
		siteResourcesTag(sourceSiteID),
		siteResourcesTag(targetSiteID),
		resourceTag(id),
	}
	var result resource.SiteTransferResult
	err := withRepositoryCacheWrite(r.policy, tags, func() error {
		var writeErr error
		result, writeErr = repository.TransferToSite(
			ctx, actorID, id, sourceSiteID, targetSiteID, expectedVersion,
			expectedSourceProfile, expectedTargetProfile,
		)
		if writeErr != nil {
			return writeErr
		}
		invalidate := append(append([]cache.Tag(nil), tags...), siteRoutesTag(sourceSiteID), siteRoutesTag(targetSiteID), siteResourceTreeTag(sourceSiteID), siteResourceTreeTag(targetSiteID))
		for _, resourceID := range result.ResourceIDs {
			invalidate = append(invalidate, resourceTag(resourceID))
		}
		r.policy.invalidate(ctx, invalidate...)
		return nil
	})
	return result, err
}

func (r *invalidatingResourceRepository) Statistics(
	ctx context.Context,
	query resource.StatisticsQuery,
) (resource.Statistics, error) {
	statistics, ok := r.base.(resource.StatisticsRepository)
	if !ok {
		return resource.Statistics{}, errors.New("resource statistics repository is unavailable")
	}
	return statistics.Statistics(ctx, query)
}

func (r *invalidatingResourceRepository) Update(
	ctx context.Context,
	actorID *security.UserID,
	current resource.Resource,
	item resource.Resource,
	validate resource.ValidateImageMedia,
) (resource.Resource, error) {
	var result resource.Resource
	err := withRepositoryCacheWrite(
		r.policy,
		[]cache.Tag{
			siteResourcesTag(current.SiteID),
			siteResourcesTag(item.SiteID),
			resourceTag(current.ID),
		},
		func() error {
			var err error
			result, err = r.base.Update(ctx, actorID, current, item, validate)
			if err != nil {
				return err
			}
			tags := append(resourceTags(current), resourceTags(result)...)
			if routeChanged(current, result) {
				tags = append(tags, siteRoutesTag(current.SiteID), siteRoutesTag(result.SiteID), siteResourceTreeTag(current.SiteID), siteResourceTreeTag(result.SiteID))
			}
			r.policy.invalidate(ctx, tags...)

			return nil
		},
	)
	return result, err
}

func (r *invalidatingResourceRepository) CreateWidget(ctx context.Context, actorID *security.UserID, id resource.ID, expectedVersion int64, binding widget.Binding, recordRevision bool) (widget.Binding, error) {
	repository, ok := r.base.(resource.WidgetRepository)
	if !ok {
		return widget.Binding{}, errors.New("resource widget repository is unavailable")
	}
	var result widget.Binding
	err := r.mutateWidgets(ctx, id, func() error {
		var err error
		result, err = repository.CreateWidget(ctx, actorID, id, expectedVersion, binding, recordRevision)
		return err
	})
	return result, err
}

func (r *invalidatingResourceRepository) UpdateWidget(ctx context.Context, actorID *security.UserID, id resource.ID, expectedVersion int64, binding widget.Binding, recordRevision bool) (widget.Binding, error) {
	repository, ok := r.base.(resource.WidgetRepository)
	if !ok {
		return widget.Binding{}, errors.New("resource widget repository is unavailable")
	}
	var result widget.Binding
	err := r.mutateWidgets(ctx, id, func() error {
		var err error
		result, err = repository.UpdateWidget(ctx, actorID, id, expectedVersion, binding, recordRevision)
		return err
	})
	return result, err
}

func (r *invalidatingResourceRepository) DeleteWidget(ctx context.Context, actorID *security.UserID, id resource.ID, expectedVersion int64, bindingID widget.BindingID, recordRevision bool) error {
	repository, ok := r.base.(resource.WidgetRepository)
	if !ok {
		return errors.New("resource widget repository is unavailable")
	}
	return r.mutateWidgets(ctx, id, func() error {
		return repository.DeleteWidget(ctx, actorID, id, expectedVersion, bindingID, recordRevision)
	})
}

func (r *invalidatingResourceRepository) ReorderWidgets(ctx context.Context, actorID *security.UserID, id resource.ID, expectedVersion int64, order []widget.Order, recordRevision bool) ([]widget.Binding, error) {
	repository, ok := r.base.(resource.WidgetRepository)
	if !ok {
		return nil, errors.New("resource widget repository is unavailable")
	}
	var result []widget.Binding
	err := r.mutateWidgets(ctx, id, func() error {
		var err error
		result, err = repository.ReorderWidgets(ctx, actorID, id, expectedVersion, order, recordRevision)
		return err
	})
	return result, err
}

func (r *invalidatingResourceRepository) mutateWidgets(ctx context.Context, id resource.ID, mutate func() error) error {
	current, err := r.base.ByID(ctx, id)
	if errors.Is(err, resource.ErrNotFound) {
		if _, ok := r.base.(resource.LibraryItemRepository); ok {
			return r.mutateLibraryItem(ctx, id, func(resource.LibraryItemRepository) error {
				return mutate()
			})
		}
	}
	if err != nil {
		return err
	}
	return withRepositoryCacheWrite(r.policy, resourceTags(current), func() error {
		if err := mutate(); err != nil {
			return err
		}
		r.policy.invalidate(ctx, resourceTags(current)...)
		return nil
	})
}

func (r *invalidatingResourceRepository) Delete(
	ctx context.Context,
	id resource.ID,
) error {
	current, err := r.base.ByID(ctx, id)
	if err != nil {
		return err
	}
	return withRepositoryCacheWrite(r.policy, resourceTags(current), func() error {
		if err := r.base.Delete(ctx, id); err != nil {
			return err
		}
		r.policy.invalidate(ctx, treeMutationTags(current)...)
		return nil
	})
}

func (r *invalidatingResourceRepository) SoftDelete(
	ctx context.Context,
	actorID *security.UserID,
	id resource.ID,
) error {
	lifecycle, ok := r.base.(resource.LifecycleRepository)
	if !ok {
		return errors.New("resource lifecycle repository is unavailable")
	}
	current, err := r.base.ByID(ctx, id)
	if err != nil {
		return err
	}
	return withRepositoryCacheWrite(r.policy, resourceTags(current), func() error {
		if err := lifecycle.SoftDelete(ctx, actorID, id); err != nil {
			return err
		}
		r.policy.invalidate(ctx, treeMutationTags(current)...)
		return nil
	})
}

func (r *invalidatingResourceRepository) Restore(
	ctx context.Context,
	actorID *security.UserID,
	id resource.ID,
	withDescendants bool,
) error {
	lifecycle, ok := r.base.(resource.LifecycleRepository)
	if !ok {
		return errors.New("resource lifecycle repository is unavailable")
	}
	current, err := r.base.ByID(ctx, id)
	if err != nil {
		return err
	}
	return withRepositoryCacheWrite(r.policy, resourceTags(current), func() error {
		if err := lifecycle.Restore(ctx, actorID, id, withDescendants); err != nil {
			return err
		}
		r.policy.invalidate(ctx, treeMutationTags(current)...)
		return nil
	})
}

func (r *invalidatingResourceRepository) libraryItems() (resource.LibraryItemRepository, error) {
	repository, ok := r.base.(resource.LibraryItemRepository)
	if !ok {
		return nil, errors.New("library item repository is unavailable")
	}
	return repository, nil
}

func libraryItemTags(item resource.LibraryItem) []cache.Tag {
	return []cache.Tag{siteResourcesTag(item.SiteID), resourceTag(item.LibraryID), resourceTag(item.ID)}
}

func (r *invalidatingResourceRepository) CreateLibraryItem(ctx context.Context, actorID *security.UserID, item resource.LibraryItem, recordRevision bool) (resource.LibraryItem, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItem{}, err
	}
	var result resource.LibraryItem
	err = withRepositoryCacheWrite(r.policy, []cache.Tag{siteResourcesTag(item.SiteID), resourceTag(item.LibraryID)}, func() error {
		var writeErr error
		result, writeErr = repository.CreateLibraryItem(ctx, actorID, item, recordRevision)
		if writeErr == nil {
			r.policy.invalidate(ctx, append(libraryItemTags(result), siteRoutesTag(result.SiteID))...)
		}
		return writeErr
	})
	return result, err
}
func (r *invalidatingResourceRepository) LibraryItemByID(ctx context.Context, id resource.ID) (resource.LibraryItem, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItem{}, err
	}
	return repository.LibraryItemByID(ctx, id)
}
func (r *invalidatingResourceRepository) UpdateLibraryItem(ctx context.Context, actorID *security.UserID, current, item resource.LibraryItem, recordRevision bool) (resource.LibraryItem, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItem{}, err
	}
	var result resource.LibraryItem
	tags := libraryItemTags(current)
	err = withRepositoryCacheWrite(r.policy, tags, func() error {
		var writeErr error
		result, writeErr = repository.UpdateLibraryItem(ctx, actorID, current, item, recordRevision)
		if writeErr == nil {
			invalidate := append(tags, libraryItemTags(result)...)
			if current.Slug != result.Slug || current.LibraryID != result.LibraryID || !reflect.DeepEqual(current.PublishedAt, result.PublishedAt) {
				invalidate = append(invalidate, siteRoutesTag(current.SiteID), siteRoutesTag(result.SiteID))
			}
			r.policy.invalidate(ctx, invalidate...)
		}
		return writeErr
	})
	return result, err
}
func (r *invalidatingResourceRepository) mutateLibraryItem(ctx context.Context, id resource.ID, mutate func(resource.LibraryItemRepository) error) error {
	repository, err := r.libraryItems()
	if err != nil {
		return err
	}
	current, err := repository.LibraryItemByID(ctx, id)
	if err != nil {
		return err
	}
	tags := libraryItemTags(current)
	return withRepositoryCacheWrite(r.policy, tags, func() error {
		if err := mutate(repository); err != nil {
			return err
		}
		r.policy.invalidate(ctx, append(tags, siteRoutesTag(current.SiteID))...)
		return nil
	})
}
func (r *invalidatingResourceRepository) SoftDeleteLibraryItem(ctx context.Context, actorID *security.UserID, id resource.ID) error {
	return r.mutateLibraryItem(ctx, id, func(repository resource.LibraryItemRepository) error {
		return repository.SoftDeleteLibraryItem(ctx, actorID, id)
	})
}
func (r *invalidatingResourceRepository) RestoreLibraryItem(ctx context.Context, actorID *security.UserID, id resource.ID) error {
	return r.mutateLibraryItem(ctx, id, func(repository resource.LibraryItemRepository) error {
		return repository.RestoreLibraryItem(ctx, actorID, id)
	})
}
func (r *invalidatingResourceRepository) DeleteLibraryItem(ctx context.Context, id resource.ID) error {
	return r.mutateLibraryItem(ctx, id, func(repository resource.LibraryItemRepository) error { return repository.DeleteLibraryItem(ctx, id) })
}
func (r *invalidatingResourceRepository) MoveLibraryItem(ctx context.Context, actorID *security.UserID, id, target resource.ID, expectedVersion int64, recordRevision bool) (resource.LibraryItem, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItem{}, err
	}
	current, err := repository.LibraryItemByID(ctx, id)
	if err != nil {
		return resource.LibraryItem{}, err
	}
	tags := append(libraryItemTags(current), resourceTag(target))
	var result resource.LibraryItem
	err = withRepositoryCacheWrite(r.policy, tags, func() error {
		var writeErr error
		result, writeErr = repository.MoveLibraryItem(ctx, actorID, id, target, expectedVersion, recordRevision)
		if writeErr == nil {
			r.policy.invalidate(ctx, append(append(tags, libraryItemTags(result)...), siteRoutesTag(current.SiteID), siteRoutesTag(result.SiteID))...)
		}
		return writeErr
	})
	return result, err
}
func (r *invalidatingResourceRepository) QueryLibraryItems(ctx context.Context, query resource.LibraryItemQuery) (resource.LibraryItemPage, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItemPage{}, err
	}
	return withRepositoryCacheRead(r.policy, []cache.Tag{siteResourcesTag(query.SiteID), resourceTag(query.LibraryID)}, func() (resource.LibraryItemPage, error) { return repository.QueryLibraryItems(ctx, query) })
}
func (r *invalidatingResourceRepository) LibraryItemTemplateCodes(ctx context.Context, siteID site.ID, libraryID resource.ID) ([]template.Code, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return nil, err
	}
	return repository.LibraryItemTemplateCodes(ctx, siteID, libraryID)
}

func (r *invalidatingResourceRepository) LibraryItemWidgetCodes(ctx context.Context, siteID site.ID, libraryID resource.ID) ([]widget.Code, error) {
	repository, ok := r.base.(resource.LibraryItemRepository)
	if !ok {
		return nil, errors.New("resource library item repository is unavailable")
	}
	return repository.LibraryItemWidgetCodes(ctx, siteID, libraryID)
}
func (r *invalidatingResourceRepository) ResolveLibraryItemRoute(ctx context.Context, siteID site.ID, path string) (resource.LibraryItem, resource.Resource, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItem{}, resource.Resource{}, err
	}
	return repository.ResolveLibraryItemRoute(ctx, siteID, path)
}

var _ Database = (*coherentDatabase)(nil)
var _ site.ManagementRepository = (*invalidatingSiteRepository)(nil)
var _ site.StatisticsRepository = (*invalidatingSiteRepository)(nil)
var _ resource.ManagementRepository = (*invalidatingResourceRepository)(nil)
var _ resource.SiteTransferRepository = (*invalidatingResourceRepository)(nil)
var _ resource.WidgetRepository = (*invalidatingResourceRepository)(nil)
var _ resource.LifecycleRepository = (*invalidatingResourceRepository)(nil)
var _ resource.StatisticsRepository = (*invalidatingResourceRepository)(nil)
var _ resource.QueryRepository = (*invalidatingResourceRepository)(nil)
var _ resource.LibraryItemRepository = (*invalidatingResourceRepository)(nil)

// Confirmed filesystem cascades mutate resource image ownership via database
// FKs. Keep those writes on the same cache coherence boundary as resource CRUD.
func (d *coherentDatabase) Files() file.Repository {
	base := d.Database.Files()
	management, ok := base.(file.ManagementRepository)
	if !ok {
		return base
	}
	cascade, ok := base.(file.CascadeRepository)
	if !ok {
		return base
	}
	return &invalidatingFileRepository{ManagementRepository: management, cascade: cascade, policy: d.policy}
}

type invalidatingFileRepository struct {
	file.ManagementRepository
	cascade file.CascadeRepository
	policy  *repositoryCachePolicy
}

func (r *invalidatingFileRepository) DeleteImpact(ctx context.Context, items []file.ItemReference) (file.DeleteImpact, error) {
	return r.cascade.DeleteImpact(ctx, items)
}
func (r *invalidatingFileRepository) DeleteConfirmed(ctx context.Context, actorID *security.UserID, items []file.ItemReference, token string, physical file.DeletePhysical) error {
	impact, err := r.cascade.DeleteImpact(ctx, items)
	if err != nil {
		return err
	}
	if impact.Token != token {
		return file.ErrConflict
	}
	tags := make([]cache.Tag, 0, len(impact.ResourceSites))
	for _, id := range impact.ResourceSites {
		tags = append(tags, siteResourcesTag(site.ID(id)), siteResourceTreeTag(site.ID(id)))
	}
	return withRepositoryCacheWrite(r.policy, tags, func() error {
		err := r.cascade.DeleteConfirmed(ctx, actorID, items, token, physical)
		r.policy.invalidate(ctx, tags...)
		return err
	})
}

func treeMutationTags(item resource.Resource) []cache.Tag {
	return append(resourceTags(item), siteRoutesTag(item.SiteID), siteResourceTreeTag(item.SiteID))
}
func routeChanged(before, after resource.Resource) bool {
	return before.SiteID != after.SiteID || before.Slug != after.Slug || before.Type != after.Type ||
		!reflect.DeepEqual(before.ParentID, after.ParentID) || !reflect.DeepEqual(before.Path, after.Path) ||
		libraryRoutePattern(before) != libraryRoutePattern(after)
}

func libraryRoutePattern(item resource.Resource) string {
	if item.Type != resourcetype.Library {
		return ""
	}
	pattern, _ := item.TypeSettings["item_url_pattern"].(string)
	if pattern == "" {
		return resourcetype.DefaultItemURLPattern
	}
	return pattern
}
