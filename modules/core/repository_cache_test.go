package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

func TestCachedSiteRepositoryUsesCacheAndInvalidatesUpdate(t *testing.T) {
	store := newMemoryCacheStore()
	base := &siteRepositoryStub{
		items: []site.Site{{
			ID:          1,
			ProfileCode: "dev",
			Domain:      "example.test",
			Locale:      "en",
			Settings: map[string]any{
				"limit": json.Number("12"),
			},
		}},
	}
	policy := newTestRepositoryCachePolicy(store)
	repository := &cachedSiteRepository{
		base:   &invalidatingSiteRepository{base: base, policy: policy},
		store:  store,
		ttl:    5 * time.Minute,
		policy: policy,
	}

	first, err := repository.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first[0].Settings["limit"] = json.Number("99")
	second, err := repository.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if base.listCalls != 1 {
		t.Fatalf("site list calls = %d", base.listCalls)
	}
	if value, ok := second[0].Settings["limit"].(json.Number); !ok ||
		value.String() != "12" {
		t.Fatalf("cached settings = %#v", second[0].Settings)
	}
	if options := store.options[sitesListCacheKey]; options.TTL != 5*time.Minute ||
		!reflect.DeepEqual(options.Tags, []cache.Tag{sitesTag}) {
		t.Fatalf("site cache options = %#v", options)
	}

	updated := second[0]
	updated.Domain = "new.example.test"
	if _, err := repository.Update(
		context.Background(),
		nil,
		updated,
	); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		store.invalidated,
		[]cache.Tag{sitesTag, siteTag(1)},
	) {
		t.Fatalf("invalidated tags = %v", store.invalidated)
	}

	store.invalidated = nil
	created, err := repository.Create(
		context.Background(),
		nil,
		site.Site{ID: 2, Domain: "created.example.test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		store.invalidated,
		[]cache.Tag{sitesTag, siteTag(created.ID)},
	) {
		t.Fatalf("create invalidated tags = %v", store.invalidated)
	}

	store.invalidated = nil
	if err := repository.Delete(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		store.invalidated,
		[]cache.Tag{sitesTag, siteTag(created.ID)},
	) {
		t.Fatalf("delete invalidated tags = %v", store.invalidated)
	}
}

func TestCachedRepositoriesFailOpen(t *testing.T) {
	store := newMemoryCacheStore()
	store.getErr = errors.New("redis unavailable")
	store.setErr = errors.New("redis unavailable")
	store.invalidateErr = errors.New("redis unavailable")
	base := &siteRepositoryStub{
		items: []site.Site{{ID: 1, Settings: map[string]any{}}},
	}
	policy := newTestRepositoryCachePolicy(store)
	repository := &cachedSiteRepository{
		base:   &invalidatingSiteRepository{base: base, policy: policy},
		store:  store,
		ttl:    time.Minute,
		policy: policy,
	}
	if _, err := repository.List(context.Background()); err != nil {
		t.Fatalf("cache read/write error escaped: %v", err)
	}
	if base.listCalls != 1 {
		t.Fatalf("site list calls = %d", base.listCalls)
	}
	if _, err := repository.Update(
		context.Background(),
		nil,
		base.items[0],
	); err != nil {
		t.Fatalf("cache invalidation error escaped: %v", err)
	}
}

func TestCachedSiteRepositoryDoesNotInvalidateFailedMutation(t *testing.T) {
	store := newMemoryCacheStore()
	updateErr := errors.New("database unavailable")
	base := &siteRepositoryStub{updateErr: updateErr}
	policy := newTestRepositoryCachePolicy(store)
	repository := &cachedSiteRepository{
		base:   &invalidatingSiteRepository{base: base, policy: policy},
		store:  store,
		ttl:    time.Minute,
		policy: policy,
	}
	if _, err := repository.Update(
		context.Background(),
		nil,
		site.Site{ID: 1},
	); !errors.Is(err, updateErr) {
		t.Fatalf("update error = %v", err)
	}
	if len(store.invalidated) != 0 {
		t.Fatalf("failed update invalidated tags: %v", store.invalidated)
	}
}

func TestCachedResourceRepositoryKeysTagsAndInvalidation(t *testing.T) {
	store := newMemoryCacheStore()
	base := &resourceRepositoryStub{
		item: resource.Resource{
			ID:     7,
			SiteID: 3,
			Title:  "cached",
			Fields: map[string]any{"count": json.Number("2")},
			Widgets: []widget.Binding{{
				ID:           1,
				Code:         widget.Code("content_summary"),
				Area:         widget.AreaBody,
				Position:     0,
				Presentation: widget.DefaultPresentation(),
				Params: map[string]any{
					"limit": json.Number("3"),
				},
			}},
		},
	}
	policy := newTestRepositoryCachePolicy(store)
	repository := &cachedResourceRepository{
		base: &invalidatingResourceRepository{
			base: base, policy: policy,
		},
		store:  store,
		ttl:    5 * time.Minute,
		policy: policy,
	}

	first, err := repository.ByID(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	first.Widgets[0].Params["limit"] = json.Number("99")
	second, err := repository.ByID(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if base.byIDCalls != 1 {
		t.Fatalf("resource ByID calls = %d", base.byIDCalls)
	}
	if len(second.Widgets) != 1 ||
		second.Widgets[0].Code != "content_summary" {
		t.Fatalf("cached widgets = %#v", second.Widgets)
	}
	if value, ok := second.Widgets[0].Params["limit"].(json.Number); !ok ||
		value.String() != "3" {
		t.Fatalf("cached widget params = %#v", second.Widgets[0].Params)
	}
	key := "resource:id:v2:7"
	if !reflect.DeepEqual(
		store.options[key].Tags,
		[]cache.Tag{siteTag(3), siteResourcesTag(3), resourceTag(7)},
	) {
		t.Fatalf("resource tags = %v", store.options[key].Tags)
	}

	store.invalidated = nil
	created, err := repository.Create(
		context.Background(),
		nil,
		base.item,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 7 {
		t.Fatalf("created resource = %#v", created)
	}
	if !reflect.DeepEqual(
		store.invalidated,
		[]cache.Tag{siteResourcesTag(3), resourceTag(7)},
	) {
		t.Fatalf("create invalidated = %v", store.invalidated)
	}

	store.invalidated = nil
	updatedInput := base.item
	updatedInput.SiteID = 4
	updated, err := repository.Update(
		context.Background(),
		nil,
		base.item,
		updatedInput,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		store.invalidated,
		[]cache.Tag{
			siteResourcesTag(3),
			siteResourcesTag(4),
			resourceTag(7),
		},
	) {
		t.Fatalf("update invalidated = %v", store.invalidated)
	}
	store.invalidated = nil
	transferResult, err := repository.TransferToSite(context.Background(), nil, updated.ID, 4, 5, updated.Version, "dev", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if transferResult.Resource.SiteID != 5 || !reflect.DeepEqual(transferResult.ResourceIDs, []resource.ID{7, 8}) {
		t.Fatalf("transfer result = %#v", transferResult)
	}
	if !reflect.DeepEqual(store.invalidated, []cache.Tag{
		siteResourcesTag(4), siteResourcesTag(5), resourceTag(7), resourceTag(8),
	}) {
		t.Fatalf("transfer invalidated = %v", store.invalidated)
	}
	updated = transferResult.Resource
	beforeWidgetUpdate, err := repository.ByID(context.Background(), updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeWidgetUpdate.Widgets[0].Presentation.Columns != 12 {
		t.Fatalf("widget before cached update = %#v", beforeWidgetUpdate.Widgets[0])
	}

	store.invalidated = nil
	changedWidget := updated.Widgets[0]
	changedWidget.Presentation.Columns = 8
	if _, err := repository.UpdateWidget(context.Background(), nil, updated.ID, updated.Version, changedWidget, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.invalidated, []cache.Tag{siteResourcesTag(5), resourceTag(7)}) {
		t.Fatalf("widget update invalidated = %v", store.invalidated)
	}
	freshAfterWidgetUpdate, err := repository.ByID(context.Background(), updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if freshAfterWidgetUpdate.Widgets[0].Presentation.Columns != 8 {
		t.Fatalf("stale widget survived cache invalidation = %#v", freshAfterWidgetUpdate.Widgets[0])
	}

	store.invalidated = nil
	if err := repository.SoftDelete(context.Background(), nil, updated.ID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		store.invalidated,
		[]cache.Tag{siteResourcesTag(5), resourceTag(7)},
	) {
		t.Fatalf("soft delete invalidated = %v", store.invalidated)
	}

	store.invalidated = nil
	if err := repository.Restore(context.Background(), nil, updated.ID, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		store.invalidated,
		[]cache.Tag{siteResourcesTag(5), resourceTag(7)},
	) {
		t.Fatalf("restore invalidated = %v", store.invalidated)
	}

	store.invalidated = nil
	if err := repository.Delete(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		store.invalidated,
		[]cache.Tag{siteResourcesTag(5), resourceTag(7)},
	) {
		t.Fatalf("delete invalidated = %v", store.invalidated)
	}
}

func TestRepositoryCacheCoherencePreventsStaleReadFillAfterWrite(t *testing.T) {
	store := newMemoryCacheStore()
	base := &blockingResourceRepository{
		resourceRepositoryStub: &resourceRepositoryStub{item: resource.Resource{
			ID: 7, SiteID: 3, Title: "before",
		}},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	policy := newTestRepositoryCachePolicy(store)
	repository := &cachedResourceRepository{
		base: &invalidatingResourceRepository{
			base: base, policy: policy,
		},
		store:  store,
		ttl:    time.Minute,
		policy: policy,
	}

	readDone := make(chan resource.Resource, 1)
	readErr := make(chan error, 1)
	go func() {
		result, err := repository.ByID(context.Background(), 7)
		readDone <- result
		readErr <- err
	}()
	<-base.started
	lock := policy.lockIndexes([]cache.Tag{resourceTag(7)})[0]
	if policy.coherence[lock].TryLock() {
		policy.coherence[lock].Unlock()
		t.Fatal("cache fill did not hold the coherence read barrier")
	}

	writeDone := make(chan error, 1)
	go func() {
		_, err := repository.Update(
			context.Background(),
			nil,
			base.item,
			resource.Resource{ID: 7, SiteID: 3, Title: "after"},
			nil,
		)
		writeDone <- err
	}()
	close(base.release)
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
	if result := <-readDone; result.Title != "before" {
		t.Fatalf("in-flight read = %#v", result)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}

	fresh, err := repository.ByID(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Title != "after" {
		t.Fatalf("stale cache fill survived write: %#v", fresh)
	}
}

func TestRepositoryCacheCoherenceDoesNotSerializeUnrelatedDependencies(t *testing.T) {
	policy := newRepositoryCachePolicy(nil)
	first := resourceTag(1)
	firstLock := policy.lockIndexes([]cache.Tag{first})[0]
	var second cache.Tag
	for id := resource.ID(2); id < 100; id++ {
		candidate := resourceTag(id)
		if policy.lockIndexes([]cache.Tag{candidate})[0] != firstLock {
			second = candidate
			break
		}
	}
	if second == "" {
		t.Fatal("could not find an independent coherence stripe")
	}

	policy.coherence[firstLock].RLock()
	defer policy.coherence[firstLock].RUnlock()
	done := make(chan struct{})
	go func() {
		_ = withRepositoryCacheWrite(policy, []cache.Tag{second}, func() error {
			close(done)
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unrelated dependency write was serialized")
	}
}

type memoryCacheStore struct {
	values        map[string][]byte
	options       map[string]cache.SetOptions
	invalidated   []cache.Tag
	getErr        error
	setErr        error
	invalidateErr error
}

func newTestRepositoryCachePolicy(store cache.Store) *repositoryCachePolicy {
	return newRepositoryCachePolicy(cache.InvalidatorFunc(func(
		ctx context.Context,
		tags ...cache.Tag,
	) error {
		seen := make(map[cache.Tag]struct{}, len(tags))
		var result []error
		for _, tag := range tags {
			if tag == "" {
				continue
			}
			if _, exists := seen[tag]; exists {
				continue
			}
			seen[tag] = struct{}{}
			if err := store.InvalidateTag(ctx, tag); err != nil {
				result = append(result, err)
			}
		}
		return errors.Join(result...)
	}))
}

func newMemoryCacheStore() *memoryCacheStore {
	return &memoryCacheStore{
		values:  make(map[string][]byte),
		options: make(map[string]cache.SetOptions),
	}
}

func (*memoryCacheStore) Code() cache.Code {
	return "test"
}

func (*memoryCacheStore) Ping(context.Context) error {
	return nil
}

func (s *memoryCacheStore) Get(
	_ context.Context,
	key string,
) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	value, exists := s.values[key]
	if !exists {
		return nil, cache.ErrMiss
	}
	return append([]byte(nil), value...), nil
}

func (s *memoryCacheStore) Set(
	_ context.Context,
	key string,
	value []byte,
	options cache.SetOptions,
) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = append([]byte(nil), value...)
	options.Tags = append([]cache.Tag(nil), options.Tags...)
	s.options[key] = options
	return nil
}

func (s *memoryCacheStore) Exists(
	ctx context.Context,
	key string,
) (bool, error) {
	_, err := s.Get(ctx, key)
	if errors.Is(err, cache.ErrMiss) {
		return false, nil
	}
	return err == nil, err
}

func (s *memoryCacheStore) Delete(
	_ context.Context,
	key string,
) error {
	delete(s.values, key)
	return nil
}

func (s *memoryCacheStore) InvalidateTag(
	_ context.Context,
	tag cache.Tag,
) error {
	s.invalidated = append(s.invalidated, tag)
	if s.invalidateErr != nil {
		return s.invalidateErr
	}
	for key, options := range s.options {
		for _, current := range options.Tags {
			if current != tag {
				continue
			}
			delete(s.values, key)
			delete(s.options, key)
			break
		}
	}
	return nil
}

func (*memoryCacheStore) Close() error {
	return nil
}

type siteRepositoryStub struct {
	items     []site.Site
	listCalls int
	updateErr error
}

func (r *siteRepositoryStub) List(
	context.Context,
) ([]site.Site, error) {
	r.listCalls++
	result := make([]site.Site, len(r.items))
	copy(result, r.items)
	return result, nil
}

func (r *siteRepositoryStub) Update(
	_ context.Context,
	_ *security.UserID,
	item site.Site,
) (site.Site, error) {
	if r.updateErr != nil {
		return site.Site{}, r.updateErr
	}
	for index := range r.items {
		if r.items[index].ID == item.ID {
			r.items[index] = item
			return item, nil
		}
	}
	return item, nil
}

func (r *siteRepositoryStub) FindByID(
	_ context.Context,
	id site.ID,
) (site.Site, error) {
	for _, item := range r.items {
		if item.ID == id {
			return item, nil
		}
	}
	return site.Site{}, site.ErrNotFound
}

func (r *siteRepositoryStub) FindByDomain(
	_ context.Context,
	domain string,
) (site.Site, error) {
	for _, item := range r.items {
		if item.Domain == domain {
			return item, nil
		}
	}
	return site.Site{}, site.ErrNotFound
}

func (r *siteRepositoryStub) ListPage(
	_ context.Context,
	_ site.ListQuery,
) (site.Page, error) {
	return site.Page{Items: append([]site.Site(nil), r.items...), Total: len(r.items)}, nil
}

func (r *siteRepositoryStub) Create(
	_ context.Context,
	_ *security.UserID,
	item site.Site,
) (site.Site, error) {
	r.items = append(r.items, item)
	return item, nil
}

func (r *siteRepositoryStub) Delete(
	_ context.Context,
	id site.ID,
) error {
	for index, item := range r.items {
		if item.ID == id {
			r.items = append(r.items[:index], r.items[index+1:]...)
			return nil
		}
	}
	return site.ErrNotFound
}

type resourceRepositoryStub struct {
	item      resource.Resource
	byIDCalls int
	deleteErr error
}

type blockingResourceRepository struct {
	*resourceRepositoryStub
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (r *blockingResourceRepository) ByID(
	context.Context,
	resource.ID,
) (resource.Resource, error) {
	result := r.item
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	r.byIDCalls++
	return result, nil
}

func (r *resourceRepositoryStub) Create(
	context.Context,
	*security.UserID,
	resource.Resource,
	resource.ValidateImageMedia,
) (resource.Resource, error) {
	return r.item, nil
}

func (r *resourceRepositoryStub) ByID(
	context.Context,
	resource.ID,
) (resource.Resource, error) {
	r.byIDCalls++
	return r.item, nil
}

func (r *resourceRepositoryStub) ByPath(
	context.Context,
	site.ID,
	string,
) (resource.Resource, error) {
	return r.item, nil
}

func (r *resourceRepositoryStub) ListBySite(
	context.Context,
	site.ID,
) ([]resource.Resource, error) {
	return []resource.Resource{r.item}, nil
}

func (r *resourceRepositoryStub) TransferToSite(
	_ context.Context,
	_ *security.UserID,
	_ resource.ID,
	_ site.ID,
	targetSiteID site.ID,
	_ int64,
	_ string,
	_ string,
) (resource.SiteTransferResult, error) {
	r.item.SiteID = targetSiteID
	return resource.SiteTransferResult{
		Resource:    r.item,
		ResourceIDs: []resource.ID{r.item.ID, 8},
	}, nil
}

func (r *resourceRepositoryStub) Update(
	_ context.Context,
	_ *security.UserID,
	_ resource.Resource,
	item resource.Resource,
	_ resource.ValidateImageMedia,
) (resource.Resource, error) {
	r.item = item
	return item, nil
}

func (r *resourceRepositoryStub) Delete(
	context.Context,
	resource.ID,
) error {
	return r.deleteErr
}

func (r *resourceRepositoryStub) CreateWidget(_ context.Context, _ *security.UserID, _ resource.ID, _ int64, binding widget.Binding, _ bool) (widget.Binding, error) {
	r.item.Widgets = append(r.item.Widgets, binding)
	return binding, nil
}

func (r *resourceRepositoryStub) UpdateWidget(_ context.Context, _ *security.UserID, _ resource.ID, _ int64, binding widget.Binding, _ bool) (widget.Binding, error) {
	for index := range r.item.Widgets {
		if r.item.Widgets[index].ID == binding.ID {
			r.item.Widgets[index] = binding
			return binding, nil
		}
	}
	return widget.Binding{}, resource.ErrNotFound
}

func (r *resourceRepositoryStub) DeleteWidget(_ context.Context, _ *security.UserID, _ resource.ID, _ int64, bindingID widget.BindingID, _ bool) error {
	for index, binding := range r.item.Widgets {
		if binding.ID == bindingID {
			r.item.Widgets = append(r.item.Widgets[:index], r.item.Widgets[index+1:]...)
			return nil
		}
	}
	return resource.ErrNotFound
}

func (r *resourceRepositoryStub) ReorderWidgets(_ context.Context, _ *security.UserID, _ resource.ID, _ int64, order []widget.Order, _ bool) ([]widget.Binding, error) {
	for index := range r.item.Widgets {
		for _, item := range order {
			if r.item.Widgets[index].ID == item.ID {
				r.item.Widgets[index].Area = item.Area
				r.item.Widgets[index].Position = item.Position
			}
		}
	}
	return widget.CloneBindings(r.item.Widgets), nil
}

func (r *resourceRepositoryStub) ExistsInSite(
	_ context.Context,
	siteID site.ID,
	id resource.ID,
) (bool, error) {
	return r.item.SiteID == siteID && r.item.ID == id, nil
}

func (r *resourceRepositoryStub) ListChildren(
	context.Context,
	site.ID,
	*resource.ID,
) ([]resource.Child, error) {
	return nil, nil
}

func (*resourceRepositoryStub) SoftDelete(
	context.Context,
	*security.UserID,
	resource.ID,
) error {
	return nil
}

func (*resourceRepositoryStub) Restore(
	context.Context,
	*security.UserID,
	resource.ID,
	bool,
) error {
	return nil
}

var _ cache.Store = (*memoryCacheStore)(nil)
var _ site.ManagementRepository = (*siteRepositoryStub)(nil)
var _ resource.ManagementRepository = (*resourceRepositoryStub)(nil)
var _ resource.WidgetRepository = (*resourceRepositoryStub)(nil)
var _ resource.LifecycleRepository = (*resourceRepositoryStub)(nil)

type cascadeCacheFixture struct {
	file.CascadeRepository
	resources *resourceRepositoryStub
}

func (f cascadeCacheFixture) DeleteImpact(context.Context, []file.ItemReference) (file.DeleteImpact, error) {
	return file.DeleteImpact{Token: "impact", ResourceSites: []int64{3}}, nil
}
func (f cascadeCacheFixture) DeleteConfirmed(context.Context, *security.UserID, []file.ItemReference, string, file.DeletePhysical) error {
	f.resources.item.ImageMediaID = nil
	return nil
}
func TestConfirmedFileCascadeInvalidatesCachedResourceOwnership(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCacheStore()
	policy := newTestRepositoryCachePolicy(store)
	id := media.ID(10)
	base := &resourceRepositoryStub{item: resource.Resource{ID: 7, SiteID: 3, ImageMediaID: &id}}
	cached := &cachedResourceRepository{base: base, store: store, ttl: time.Minute, policy: policy}
	first, err := cached.ByID(ctx, 7)
	if err != nil || first.ImageMediaID == nil {
		t.Fatal(err)
	}
	files := &invalidatingFileRepository{cascade: cascadeCacheFixture{resources: base}, policy: policy}
	if err := files.DeleteConfirmed(ctx, nil, []file.ItemReference{{Kind: file.ItemFile, ID: 1}}, "impact", nil); err != nil {
		t.Fatal(err)
	}
	next, err := cached.ByID(ctx, 7)
	if err != nil || next.ImageMediaID != nil {
		t.Fatal("stale media reference after cascade", err)
	}
}

type widgetLibraryRepository struct {
	*resourceRepositoryStub
	resource.LibraryItemRepository
}

func (r *widgetLibraryRepository) ByID(context.Context, resource.ID) (resource.Resource, error) {
	return resource.Resource{}, resource.ErrNotFound
}
func (r *widgetLibraryRepository) LibraryItemByID(_ context.Context, id resource.ID) (resource.LibraryItem, error) {
	if id != r.item.ID {
		return resource.LibraryItem{}, resource.ErrNotFound
	}
	return resource.LibraryItem{ID: r.item.ID, SiteID: r.item.SiteID, LibraryID: 10, Widgets: widget.CloneBindings(r.item.Widgets)}, nil
}

func TestLibraryWidgetMutationsInvalidateItemAndLibraryDependencies(t *testing.T) {
	ctx := context.Background()
	store := newMemoryCacheStore()
	policy := newTestRepositoryCachePolicy(store)
	base := &widgetLibraryRepository{resourceRepositoryStub: &resourceRepositoryStub{item: resource.Resource{ID: 20, SiteID: 3}}}
	repository := &cachedResourceRepository{base: &invalidatingResourceRepository{base: base, policy: policy}, store: store, ttl: time.Minute, policy: policy}
	binding := widget.Binding{ID: 1, Code: "test", Area: widget.AreaBody, Presentation: widget.DefaultPresentation(), ParamBindings: widget.ParamBindings{"text": widget.ResourceProperty("title")}}
	operations := []struct {
		name  string
		run   func() error
		count int
	}{
		{"create", func() error { _, err := repository.CreateWidget(ctx, nil, 20, 1, binding, true); return err }, 1},
		{"update", func() error {
			binding = widget.CloneBinding(binding)
			binding.ParamBindings["text"] = widget.ResourceField("headline")
			_, err := repository.UpdateWidget(ctx, nil, 20, 1, binding, true)
			return err
		}, 1},
		{"reorder", func() error {
			_, err := repository.ReorderWidgets(ctx, nil, 20, 1, []widget.Order{{ID: 1, Area: widget.AreaSidebar}}, true)
			return err
		}, 1},
		{"delete", func() error { return repository.DeleteWidget(ctx, nil, 20, 1, 1, true) }, 0},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			before, err := repository.LibraryItemByID(ctx, 20)
			if err != nil {
				t.Fatal(err)
			}
			tags := libraryItemTags(before)
			for _, tag := range tags {
				if err := store.Set(ctx, string(tag), []byte("stale"), cache.SetOptions{Tags: []cache.Tag{tag}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := op.run(); err != nil {
				t.Fatal(err)
			}
			for _, tag := range tags {
				if _, err := store.Get(ctx, string(tag)); !errors.Is(err, cache.ErrMiss) {
					t.Fatalf("stale dependency %s survived: %v", tag, err)
				}
			}
			after, err := repository.LibraryItemByID(ctx, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(after.Widgets) != op.count {
				t.Fatalf("widget mutation lost: %#v", after.Widgets)
			}
			if op.count > 0 && after.Widgets[0].ParamBindings["text"] != binding.ParamBindings["text"] {
				t.Fatal("binding lost through cache wrapper")
			}
		})
	}
}
