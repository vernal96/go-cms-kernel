package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

const (
	sitesListCacheKey = "site:list:v1"
	sitesTag          = cache.Tag("sites")
)

type cachedDatabase struct {
	Database
	sites     site.Repository
	resources resource.Repository
}

func newCachedDatabase(
	database Database,
	store cache.Store,
	ttl time.Duration,
	policy *repositoryCachePolicy,
) Database {
	return &cachedDatabase{
		Database: database,
		sites: &cachedSiteRepository{
			base:   database.Sites(),
			store:  store,
			ttl:    ttl,
			policy: policy,
		},
		resources: &cachedResourceRepository{
			base:   database.Resources(),
			store:  store,
			ttl:    ttl,
			policy: policy,
		},
	}
}

func (d *cachedDatabase) Sites() site.Repository {
	return d.sites
}

func (d *cachedDatabase) Resources() resource.Repository {
	return d.resources
}

type cachedSiteRepository struct {
	base   site.Repository
	store  cache.Store
	ttl    time.Duration
	policy *repositoryCachePolicy
}

func (r *cachedSiteRepository) List(
	ctx context.Context,
) ([]site.Site, error) {
	return withRepositoryCacheRead(r.policy, []cache.Tag{sitesTag}, func() ([]site.Site, error) {
		return cache.RememberJSON(
			ctx,
			r.store,
			sitesListCacheKey,
			cache.SetOptions{TTL: r.ttl, Tags: []cache.Tag{sitesTag}},
			func(ctx context.Context) ([]site.Site, error) {
				return r.base.List(ctx)
			},
		)
	})
}

func (r *cachedSiteRepository) FindByID(
	ctx context.Context,
	id site.ID,
) (site.Site, error) {
	management, ok := r.base.(site.ManagementRepository)
	if !ok {
		return site.Site{}, errors.New("site management repository is unavailable")
	}
	return management.FindByID(ctx, id)
}

func (r *cachedSiteRepository) FindByDomain(
	ctx context.Context,
	domain string,
) (site.Site, error) {
	management, ok := r.base.(site.ManagementRepository)
	if !ok {
		return site.Site{}, errors.New("site management repository is unavailable")
	}
	return management.FindByDomain(ctx, domain)
}

func (r *cachedSiteRepository) ListPage(
	ctx context.Context,
	query site.ListQuery,
) (site.Page, error) {
	management, ok := r.base.(site.ManagementRepository)
	if !ok {
		return site.Page{}, errors.New("site management repository is unavailable")
	}
	return management.ListPage(ctx, query)
}

func (r *cachedSiteRepository) Statistics(
	ctx context.Context,
	query site.StatisticsQuery,
) (site.Statistics, error) {
	statistics, ok := r.base.(site.StatisticsRepository)
	if !ok {
		return site.Statistics{}, errors.New("site statistics repository is unavailable")
	}
	return statistics.Statistics(ctx, query)
}

func (r *cachedSiteRepository) Create(
	ctx context.Context,
	actorID *security.UserID,
	item site.Site,
) (site.Site, error) {
	management, ok := r.base.(site.ManagementRepository)
	if !ok {
		return site.Site{}, errors.New("site management repository is unavailable")
	}
	result, err := management.Create(ctx, actorID, item)
	if err != nil {
		return site.Site{}, err
	}
	return result, nil
}

func (r *cachedSiteRepository) Update(
	ctx context.Context,
	actorID *security.UserID,
	item site.Site,
) (site.Site, error) {
	result, err := r.base.Update(ctx, actorID, item)
	if err != nil {
		return site.Site{}, err
	}
	return result, nil
}

func (r *cachedSiteRepository) Delete(
	ctx context.Context,
	id site.ID,
) error {
	management, ok := r.base.(site.ManagementRepository)
	if !ok {
		return errors.New("site management repository is unavailable")
	}
	if err := management.Delete(ctx, id); err != nil {
		return err
	}
	return nil
}

type cachedResourceRepository struct {
	base   resource.Repository
	store  cache.Store
	ttl    time.Duration
	policy *repositoryCachePolicy
}

func (r *cachedResourceRepository) Create(
	ctx context.Context,
	actorID *security.UserID,
	item resource.Resource,
	validate resource.ValidateImageMedia,
) (resource.Resource, error) {
	result, err := r.base.Create(ctx, actorID, item, validate)
	if err != nil {
		return resource.Resource{}, err
	}
	return result, nil
}

func (r *cachedResourceRepository) ByID(
	ctx context.Context,
	id resource.ID,
) (resource.Resource, error) {
	return withRepositoryCacheRead(r.policy, []cache.Tag{resourceTag(id)}, func() (resource.Resource, error) {
		key := fmt.Sprintf("resource:id:v2:%d", id)
		return cache.RememberJSONWithOptions(
			ctx,
			r.store,
			key,
			func(result resource.Resource) cache.SetOptions {
				return cache.SetOptions{
					TTL:  r.ttl,
					Tags: resourceDependencies(result),
				}
			},
			func(ctx context.Context) (resource.Resource, error) {
				result, err := r.base.ByID(ctx, id)
				if err != nil {
					return resource.Resource{}, err
				}
				return result, nil
			},
		)
	})
}

func (r *cachedResourceRepository) ByPath(
	ctx context.Context,
	siteID site.ID,
	pathValue string,
) (resource.Resource, error) {
	return withRepositoryCacheRead(r.policy, []cache.Tag{siteResourcesTag(siteID)}, func() (resource.Resource, error) {
		sum := sha256.Sum256([]byte(pathValue))
		key := fmt.Sprintf(
			"resource:path:v2:%d:%s",
			siteID,
			hex.EncodeToString(sum[:]),
		)
		return cache.RememberJSON(
			ctx,
			r.store,
			key,
			cache.SetOptions{
				TTL: r.ttl,
				Tags: []cache.Tag{
					siteTag(siteID),
					siteResourcesTag(siteID),
					resourcePathTag(siteID, pathValue),
				},
			},
			func(ctx context.Context) (resource.Resource, error) {
				result, err := r.base.ByPath(ctx, siteID, pathValue)
				if err != nil {
					return resource.Resource{}, err
				}
				return result, nil
			},
		)
	})
}

func (r *cachedResourceRepository) ListBySite(
	ctx context.Context,
	siteID site.ID,
) ([]resource.Resource, error) {
	return withRepositoryCacheRead(r.policy, []cache.Tag{siteResourcesTag(siteID)}, func() ([]resource.Resource, error) {
		key := fmt.Sprintf("resource:list:v2:%d", siteID)
		return cache.RememberJSON(
			ctx,
			r.store,
			key,
			cache.SetOptions{
				TTL: r.ttl,
				Tags: []cache.Tag{
					siteTag(siteID),
					siteResourcesTag(siteID),
				},
			},
			func(ctx context.Context) ([]resource.Resource, error) {
				return r.base.ListBySite(ctx, siteID)
			},
		)
	})
}

func (r *cachedResourceRepository) Query(ctx context.Context, query resource.Query) (resource.Page, error) {
	repository, ok := r.base.(resource.QueryRepository)
	if !ok {
		return resource.Page{}, errors.New("resource query repository is unavailable")
	}
	// Collection results are cached by the widget result layer, where their
	// complete normalized identity and dependencies are known.
	return repository.Query(ctx, query)
}

func (r *cachedResourceRepository) ExistsInSite(
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

func (r *cachedResourceRepository) ListChildren(
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

func (r *cachedResourceRepository) TransferToSite(
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
	return repository.TransferToSite(ctx, actorID, id, sourceSiteID, targetSiteID, expectedVersion, expectedSourceProfile, expectedTargetProfile)
}

func (r *cachedResourceRepository) Statistics(
	ctx context.Context,
	query resource.StatisticsQuery,
) (resource.Statistics, error) {
	statistics, ok := r.base.(resource.StatisticsRepository)
	if !ok {
		return resource.Statistics{}, errors.New("resource statistics repository is unavailable")
	}
	return statistics.Statistics(ctx, query)
}

func (r *cachedResourceRepository) Update(
	ctx context.Context,
	actorID *security.UserID,
	current resource.Resource,
	item resource.Resource,
	validate resource.ValidateImageMedia,
) (resource.Resource, error) {
	result, err := r.base.Update(
		ctx,
		actorID,
		current,
		item,
		validate,
	)
	if err != nil {
		return resource.Resource{}, err
	}
	return result, nil
}

func (r *cachedResourceRepository) CreateWidget(ctx context.Context, actorID *security.UserID, id resource.ID, expectedVersion int64, binding widget.Binding, recordRevision bool) (widget.Binding, error) {
	repository, ok := r.base.(resource.WidgetRepository)
	if !ok {
		return widget.Binding{}, errors.New("resource widget repository is unavailable")
	}
	return repository.CreateWidget(ctx, actorID, id, expectedVersion, binding, recordRevision)
}

func (r *cachedResourceRepository) UpdateWidget(ctx context.Context, actorID *security.UserID, id resource.ID, expectedVersion int64, binding widget.Binding, recordRevision bool) (widget.Binding, error) {
	repository, ok := r.base.(resource.WidgetRepository)
	if !ok {
		return widget.Binding{}, errors.New("resource widget repository is unavailable")
	}
	return repository.UpdateWidget(ctx, actorID, id, expectedVersion, binding, recordRevision)
}

func (r *cachedResourceRepository) DeleteWidget(ctx context.Context, actorID *security.UserID, id resource.ID, expectedVersion int64, bindingID widget.BindingID, recordRevision bool) error {
	repository, ok := r.base.(resource.WidgetRepository)
	if !ok {
		return errors.New("resource widget repository is unavailable")
	}
	return repository.DeleteWidget(ctx, actorID, id, expectedVersion, bindingID, recordRevision)
}

func (r *cachedResourceRepository) ReorderWidgets(ctx context.Context, actorID *security.UserID, id resource.ID, expectedVersion int64, order []widget.Order, recordRevision bool) ([]widget.Binding, error) {
	repository, ok := r.base.(resource.WidgetRepository)
	if !ok {
		return nil, errors.New("resource widget repository is unavailable")
	}
	return repository.ReorderWidgets(ctx, actorID, id, expectedVersion, order, recordRevision)
}

func (r *cachedResourceRepository) Delete(
	ctx context.Context,
	id resource.ID,
) error {
	if err := r.base.Delete(ctx, id); err != nil {
		return err
	}
	return nil
}

func (r *cachedResourceRepository) SoftDelete(
	ctx context.Context,
	actorID *security.UserID,
	id resource.ID,
) error {
	lifecycle, ok := r.base.(resource.LifecycleRepository)
	if !ok {
		return errors.New("resource lifecycle repository is unavailable")
	}
	if err := lifecycle.SoftDelete(ctx, actorID, id); err != nil {
		return err
	}
	return nil
}

func (r *cachedResourceRepository) Restore(
	ctx context.Context,
	actorID *security.UserID,
	id resource.ID,
	withDescendants bool,
) error {
	lifecycle, ok := r.base.(resource.LifecycleRepository)
	if !ok {
		return errors.New("resource lifecycle repository is unavailable")
	}
	if err := lifecycle.Restore(ctx, actorID, id, withDescendants); err != nil {
		return err
	}
	return nil
}

func (r *cachedResourceRepository) libraryItems() (resource.LibraryItemRepository, error) {
	repository, ok := r.base.(resource.LibraryItemRepository)
	if !ok {
		return nil, errors.New("library item repository is unavailable")
	}
	return repository, nil
}

func (r *cachedResourceRepository) CreateLibraryItem(ctx context.Context, actorID *security.UserID, item resource.LibraryItem, recordRevision bool) (resource.LibraryItem, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItem{}, err
	}
	return repository.CreateLibraryItem(ctx, actorID, item, recordRevision)
}
func (r *cachedResourceRepository) LibraryItemByID(ctx context.Context, id resource.ID) (resource.LibraryItem, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItem{}, err
	}
	return repository.LibraryItemByID(ctx, id)
}
func (r *cachedResourceRepository) UpdateLibraryItem(ctx context.Context, actorID *security.UserID, current, item resource.LibraryItem, recordRevision bool) (resource.LibraryItem, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItem{}, err
	}
	return repository.UpdateLibraryItem(ctx, actorID, current, item, recordRevision)
}
func (r *cachedResourceRepository) SoftDeleteLibraryItem(ctx context.Context, actorID *security.UserID, id resource.ID) error {
	repository, err := r.libraryItems()
	if err != nil {
		return err
	}
	return repository.SoftDeleteLibraryItem(ctx, actorID, id)
}
func (r *cachedResourceRepository) RestoreLibraryItem(ctx context.Context, actorID *security.UserID, id resource.ID) error {
	repository, err := r.libraryItems()
	if err != nil {
		return err
	}
	return repository.RestoreLibraryItem(ctx, actorID, id)
}
func (r *cachedResourceRepository) DeleteLibraryItem(ctx context.Context, id resource.ID) error {
	repository, err := r.libraryItems()
	if err != nil {
		return err
	}
	return repository.DeleteLibraryItem(ctx, id)
}
func (r *cachedResourceRepository) MoveLibraryItem(ctx context.Context, actorID *security.UserID, id, target resource.ID, expectedVersion int64, recordRevision bool) (resource.LibraryItem, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItem{}, err
	}
	return repository.MoveLibraryItem(ctx, actorID, id, target, expectedVersion, recordRevision)
}
func (r *cachedResourceRepository) QueryLibraryItems(ctx context.Context, query resource.LibraryItemQuery) (resource.LibraryItemPage, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItemPage{}, err
	}
	return repository.QueryLibraryItems(ctx, query)
}
func (r *cachedResourceRepository) LibraryItemTemplateCodes(ctx context.Context, siteID site.ID, libraryID resource.ID) ([]template.Code, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return nil, err
	}
	return repository.LibraryItemTemplateCodes(ctx, siteID, libraryID)
}

func (r *cachedResourceRepository) LibraryItemWidgetCodes(ctx context.Context, siteID site.ID, libraryID resource.ID) ([]widget.Code, error) {
	repository, ok := r.base.(resource.LibraryItemRepository)
	if !ok {
		return nil, errors.New("resource library item repository is unavailable")
	}
	return repository.LibraryItemWidgetCodes(ctx, siteID, libraryID)
}
func (r *cachedResourceRepository) ResolveLibraryItemRoute(ctx context.Context, siteID site.ID, path string) (resource.LibraryItem, resource.Resource, error) {
	repository, err := r.libraryItems()
	if err != nil {
		return resource.LibraryItem{}, resource.Resource{}, err
	}
	return repository.ResolveLibraryItemRoute(ctx, siteID, path)
}

func siteTag(id site.ID) cache.Tag {
	return cache.Tag(fmt.Sprintf("site:%d", id))
}

func resourceTag(id resource.ID) cache.Tag {
	return cache.Tag(fmt.Sprintf("resource:%d", id))
}

func siteResourcesTag(id site.ID) cache.Tag {
	return cache.Tag(fmt.Sprintf("site:%d:resources", id))
}

func resourcePathTag(siteID site.ID, value string) cache.Tag {
	sum := sha256.Sum256([]byte(value))
	return cache.Tag(fmt.Sprintf(
		"site:%d:resource-path:%s",
		siteID,
		hex.EncodeToString(sum[:]),
	))
}

func resourceTags(item resource.Resource) []cache.Tag {
	return []cache.Tag{
		siteResourcesTag(item.SiteID),
		resourceTag(item.ID),
	}
}

func resourceDependencies(item resource.Resource) []cache.Tag {
	return []cache.Tag{
		siteTag(item.SiteID),
		siteResourcesTag(item.SiteID),
		resourceTag(item.ID),
	}
}

var _ Database = (*cachedDatabase)(nil)
var _ site.Repository = (*cachedSiteRepository)(nil)
var _ site.ManagementRepository = (*cachedSiteRepository)(nil)
var _ site.StatisticsRepository = (*cachedSiteRepository)(nil)
var _ resource.Repository = (*cachedResourceRepository)(nil)
var _ resource.WidgetRepository = (*cachedResourceRepository)(nil)
var _ resource.ManagementRepository = (*cachedResourceRepository)(nil)
var _ resource.SiteTransferRepository = (*cachedResourceRepository)(nil)
var _ resource.StatisticsRepository = (*cachedResourceRepository)(nil)
var _ resource.QueryRepository = (*cachedResourceRepository)(nil)
var _ resource.LibraryItemRepository = (*cachedResourceRepository)(nil)
