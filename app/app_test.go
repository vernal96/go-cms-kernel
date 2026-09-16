package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/stub"
	"github.com/vernal96/go-cms-kernel"
	appkernel "github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/background"
	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/console"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/logging"
	"github.com/vernal96/go-cms-kernel/messageid"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core"
	coreaccess "github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	corefile "github.com/vernal96/go-cms-kernel/modules/core/file"
	coregroup "github.com/vernal96/go-cms-kernel/modules/core/group"
	coremedia "github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	coreuser "github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/modules/core/user/adapters/argon2id"
	"github.com/vernal96/go-cms-kernel/outbox"
	"github.com/vernal96/go-cms-kernel/security"
	"github.com/vernal96/go-cms-kernel/seeds"
)

const featureModuleCode kernel.ModuleCode = "feature"

type fakeLoggerFactory struct {
	connector *fakeLoggerConnector
	err       error
	onOpen    func()
}

type fakeLoggerConnector struct {
	logger   *slog.Logger
	pingErr  error
	closeErr error
	onPing   func()
	onClose  func()
}

func (f fakeLoggerFactory) Open(
	context.Context,
) (logging.Connector, error) {
	if f.onOpen != nil {
		f.onOpen()
	}
	connector := f.connector
	if connector == nil {
		connector = &fakeLoggerConnector{
			logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		}
	}
	return connector, f.err
}

func (c *fakeLoggerConnector) Logger() *slog.Logger {
	return c.logger
}

func (c *fakeLoggerConnector) Ping(context.Context) error {
	if c.onPing != nil {
		c.onPing()
	}
	return c.pingErr
}

func (c *fakeLoggerConnector) Close() error {
	if c.onClose != nil {
		c.onClose()
	}
	return c.closeErr
}

type fakeEventBusFactory struct {
	connector eventbus.Connector
	err       error
	onOpen    func()
}

type fakeEventBusConnector struct {
	pingErr  error
	closeErr error
	onPing   func()
	onClose  func()
}

func (f fakeEventBusFactory) Open(
	context.Context,
) (eventbus.Connector, error) {
	if f.onOpen != nil {
		f.onOpen()
	}
	connector := f.connector
	if connector == nil {
		connector = &fakeEventBusConnector{}
	}
	return connector, f.err
}

func (c *fakeEventBusConnector) Ping(context.Context) error {
	if c.onPing != nil {
		c.onPing()
	}
	return c.pingErr
}

func (*fakeEventBusConnector) Publish(
	context.Context,
	eventbus.Message,
) error {
	return nil
}

func (*fakeEventBusConnector) Consume(
	ctx context.Context,
	_ eventbus.Subscription,
	_ eventbus.Handler,
) error {
	<-ctx.Done()
	return nil
}

func (c *fakeEventBusConnector) Close() error {
	if c.onClose != nil {
		c.onClose()
	}
	return c.closeErr
}

type lifecycleEventBusConnector struct {
	rootContext context.Context
	cancel      context.CancelFunc
	consumers   sync.WaitGroup
	closeOnce   sync.Once
}

func newLifecycleEventBusConnector() *lifecycleEventBusConnector {
	rootContext, cancel := context.WithCancel(context.Background())
	return &lifecycleEventBusConnector{
		rootContext: rootContext,
		cancel:      cancel,
	}
}

func (*lifecycleEventBusConnector) Ping(context.Context) error {
	return nil
}

func (*lifecycleEventBusConnector) Publish(
	context.Context,
	eventbus.Message,
) error {
	return nil
}

func (c *lifecycleEventBusConnector) Consume(
	ctx context.Context,
	_ eventbus.Subscription,
	handler eventbus.Handler,
) error {
	c.consumers.Add(1)
	defer c.consumers.Done()

	consumeContext, cancel := context.WithCancel(ctx)
	stopRootCancel := context.AfterFunc(c.rootContext, cancel)
	defer func() {
		stopRootCancel()
		cancel()
	}()
	_ = handler(consumeContext, eventbus.Message{Topic: "topic"})
	if consumeContext.Err() != nil {
		return nil
	}
	return nil
}

func (c *lifecycleEventBusConnector) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.consumers.Wait()
	})
	return nil
}

type fakeConnector struct {
	code kernel.ConnectionCode

	mu       sync.Mutex
	drivers  map[string]*stub.Stub
	pings    atomic.Int32
	closes   atomic.Int32
	closeErr error
	onPing   func()
	onClose  func()
}

type fakeCacheFactory struct {
	store cache.Store
}

func (f fakeCacheFactory) Code() cache.Code {
	return f.store.Code()
}

func (f fakeCacheFactory) Open(
	context.Context,
	cache.Dependencies,
) (cache.Store, error) {
	return f.store, nil
}

type fakeCacheStore struct {
	code    cache.Code
	pingErr error
	pings   atomic.Int32
	closes  atomic.Int32
}

type taggedCacheStore struct {
	code cache.Code

	mu        sync.Mutex
	values    map[string][]byte
	entryTags map[string][]cache.Tag
	tagged    map[cache.Tag]map[string]struct{}
}

func newTaggedCacheStore(code cache.Code) *taggedCacheStore {
	return &taggedCacheStore{
		code:      code,
		values:    make(map[string][]byte),
		entryTags: make(map[string][]cache.Tag),
		tagged:    make(map[cache.Tag]map[string]struct{}),
	}
}

func (s *taggedCacheStore) Code() cache.Code { return s.code }

func (*taggedCacheStore) Ping(context.Context) error { return nil }

func (s *taggedCacheStore) Get(
	_ context.Context,
	key string,
) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.values[key]
	if !exists {
		return nil, cache.ErrMiss
	}
	return append([]byte(nil), value...), nil
}

func (s *taggedCacheStore) Set(
	_ context.Context,
	key string,
	value []byte,
	options cache.SetOptions,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteLocked(key)
	s.values[key] = append([]byte(nil), value...)
	s.entryTags[key] = append([]cache.Tag(nil), options.Tags...)
	for _, tag := range options.Tags {
		keys := s.tagged[tag]
		if keys == nil {
			keys = make(map[string]struct{})
			s.tagged[tag] = keys
		}
		keys[key] = struct{}{}
	}
	return nil
}

func (s *taggedCacheStore) Exists(
	_ context.Context,
	key string,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.values[key]
	return exists, nil
}

func (s *taggedCacheStore) Delete(
	_ context.Context,
	key string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteLocked(key)
	return nil
}

func (s *taggedCacheStore) InvalidateTag(
	_ context.Context,
	tag cache.Tag,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.tagged[tag]))
	for key := range s.tagged[tag] {
		keys = append(keys, key)
	}
	for _, key := range keys {
		s.deleteLocked(key)
	}
	return nil
}

func (*taggedCacheStore) Close() error { return nil }

func (s *taggedCacheStore) entryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.values)
}

func (s *taggedCacheStore) deleteLocked(key string) {
	delete(s.values, key)
	for _, tag := range s.entryTags[key] {
		delete(s.tagged[tag], key)
		if len(s.tagged[tag]) == 0 {
			delete(s.tagged, tag)
		}
	}
	delete(s.entryTags, key)
}

func (s *fakeCacheStore) Code() cache.Code {
	return s.code
}

func (s *fakeCacheStore) Ping(context.Context) error {
	s.pings.Add(1)
	return s.pingErr
}

func (*fakeCacheStore) Get(
	context.Context,
	string,
) ([]byte, error) {
	return nil, cache.ErrMiss
}

func (*fakeCacheStore) Set(
	context.Context,
	string,
	[]byte,
	cache.SetOptions,
) error {
	return nil
}

func (*fakeCacheStore) Exists(
	context.Context,
	string,
) (bool, error) {
	return false, nil
}

func (*fakeCacheStore) Delete(context.Context, string) error {
	return nil
}

func (*fakeCacheStore) InvalidateTag(
	context.Context,
	cache.Tag,
) error {
	return nil
}

func (s *fakeCacheStore) Close() error {
	s.closes.Add(1)
	return nil
}

func newFakeConnector(code kernel.ConnectionCode) *fakeConnector {
	return &fakeConnector{
		code:    code,
		drivers: make(map[string]*stub.Stub),
	}
}

func (c *fakeConnector) Code() kernel.ConnectionCode { return c.code }

func (c *fakeConnector) Ping(context.Context) error {
	c.pings.Add(1)
	if c.onPing != nil {
		c.onPing()
	}
	return nil
}

func (c *fakeConnector) Close() error {
	c.closes.Add(1)
	if c.onClose != nil {
		c.onClose()
	}
	return c.closeErr
}

func (c *fakeConnector) OpenMigrationDriver(
	_ context.Context,
	_ string,
	historyTable string,
) (migratedb.Driver, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if driver, exists := c.drivers[historyTable]; exists {
		return driver, nil
	}

	driver, err := stub.WithInstance(nil, &stub.Config{})
	if err != nil {
		return nil, err
	}

	c.drivers[historyTable] = driver.(*stub.Stub)
	return driver, nil
}

func (c *fakeConnector) version(historyTable string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	driver, exists := c.drivers[historyTable]
	if !exists {
		return 0, false
	}
	return driver.CurrentVersion, true
}

type fakeConnectorFactory struct {
	connector *fakeConnector
	opens     atomic.Int32
	err       error
	onOpen    func()
}

func (f *fakeConnectorFactory) Code() kernel.ConnectionCode {
	return f.connector.code
}

func (f *fakeConnectorFactory) Open(
	context.Context,
) (kernel.DBConnector, error) {
	f.opens.Add(1)
	if f.onOpen != nil {
		f.onOpen()
	}
	return f.connector, f.err
}

type fakeSiteRepository struct {
	mu          sync.Mutex
	sites       []site.Site
	err         error
	updateErr   error
	calls       int
	updateCalls int
	beforeList  func()
}

func (r *fakeSiteRepository) List(context.Context) ([]site.Site, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls++
	if r.beforeList != nil {
		r.beforeList()
	}
	if r.err != nil {
		return nil, r.err
	}

	return append([]site.Site(nil), r.sites...), nil
}

func (r *fakeSiteRepository) Update(
	_ context.Context,
	_ *security.UserID,
	item site.Site,
) (site.Site, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.updateCalls++
	if r.updateErr != nil {
		return site.Site{}, r.updateErr
	}
	if r.err != nil {
		return site.Site{}, r.err
	}

	for index := range r.sites {
		if r.sites[index].ID != item.ID {
			continue
		}

		r.sites[index] = item
		return item, nil
	}

	return site.Site{}, site.ErrNotFound
}

func (r *fakeSiteRepository) FindByID(_ context.Context, id site.ID) (site.Site, error) {
	for _, item := range r.sites {
		if item.ID == id {
			return item, nil
		}
	}
	return site.Site{}, site.ErrNotFound
}

func (r *fakeSiteRepository) FindByDomain(_ context.Context, domain string) (site.Site, error) {
	for _, item := range r.sites {
		if item.Domain == domain {
			return item, nil
		}
	}
	return site.Site{}, site.ErrNotFound
}

func (r *fakeSiteRepository) ListPage(_ context.Context, query site.ListQuery) (site.Page, error) {
	items := append([]site.Site(nil), r.sites...)
	return site.Page{Items: items, Total: len(items)}, nil
}

func (r *fakeSiteRepository) Create(_ context.Context, _ *security.UserID, item site.Site) (site.Site, error) {
	item.ID = site.ID(len(r.sites) + 1)
	r.sites = append(r.sites, item)
	return item, nil
}

func (r *fakeSiteRepository) Delete(_ context.Context, id site.ID) error {
	for index, item := range r.sites {
		if item.ID == id {
			r.sites = append(r.sites[:index], r.sites[index+1:]...)
			return nil
		}
	}
	return site.ErrNotFound
}

func (r *fakeSiteRepository) set(items []site.Site, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sites = append([]site.Site(nil), items...)
	r.err = err
}

func (r *fakeSiteRepository) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *fakeSiteRepository) setUpdateError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateErr = err
}

func (r *fakeSiteRepository) updateCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updateCalls
}

type fakeCoreDatabase struct {
	repository         site.Repository
	resourceRepository resource.Repository
	fileRepository     corefile.Repository
	mediaRepository    coremedia.Repository
	userRepository     coreuser.Repository
	groupRepository    coregroup.Repository
	accessRepository   coreaccess.Repository
	seedSources        []seeds.Source
	outboxSources      []outbox.Source
}

func (*fakeCoreDatabase) ModuleCode() kernel.ModuleCode { return core.ModuleCode }
func (d *fakeCoreDatabase) Sites() site.Repository      { return d.repository }
func (d *fakeCoreDatabase) Resources() resource.Repository {
	if d.resourceRepository != nil {
		return d.resourceRepository
	}
	return fakeResourceRepository{}
}
func (d *fakeCoreDatabase) Files() corefile.Repository {
	if d.fileRepository != nil {
		return d.fileRepository
	}
	return fakeFileRepository{}
}
func (d *fakeCoreDatabase) Media() coremedia.Repository {
	if d.mediaRepository != nil {
		return d.mediaRepository
	}
	return fakeMediaRepository{}
}
func (d *fakeCoreDatabase) Users() coreuser.Repository {
	if d.userRepository != nil {
		return d.userRepository
	}
	return fakeUserRepository{}
}
func (d *fakeCoreDatabase) Groups() coregroup.Repository {
	if d.groupRepository != nil {
		return d.groupRepository
	}
	return fakeGroupRepository{}
}
func (d *fakeCoreDatabase) Access() coreaccess.Repository {
	if d.accessRepository != nil {
		return d.accessRepository
	}
	return fakeAccessRepository{}
}
func (d *fakeCoreDatabase) OutboxSources() []outbox.Source {
	return append([]outbox.Source(nil), d.outboxSources...)
}

type blockingOutboxSource struct {
	started chan struct{}
	onStart func()
	onStop  func()
	once    sync.Once
}

func (*blockingOutboxSource) Name() string { return "core:test" }
func (s *blockingOutboxSource) Claim(ctx context.Context, _ outbox.Claim) ([]outbox.Record, error) {
	s.once.Do(func() {
		if s.onStart != nil {
			s.onStart()
		}
		close(s.started)
	})
	<-ctx.Done()
	if s.onStop != nil {
		s.onStop()
	}
	return nil, ctx.Err()
}
func (*blockingOutboxSource) MarkPublished(context.Context, messageid.ID, string) error {
	return nil
}
func (*blockingOutboxSource) MarkFailed(context.Context, messageid.ID, string, string, time.Duration) error {
	return nil
}
func (*blockingOutboxSource) CleanupPublished(context.Context, time.Duration, int) (int64, error) {
	return 0, nil
}

type fakeFileRepository struct {
	corefile.Repository
}

type fakeMediaRepository struct {
	coremedia.Repository
}

type fakeUserRepository struct {
	coreuser.Repository
}

type fakeGroupRepository struct {
	coregroup.Repository
}

func (fakeUserRepository) ListPage(context.Context, coreuser.ListQuery) (coreuser.Page, error) {
	return coreuser.Page{}, nil
}

func (fakeGroupRepository) ListPage(context.Context, coregroup.ListQuery) (coregroup.Page, error) {
	return coregroup.Page{}, nil
}

type fakeAccessRepository struct {
	coreaccess.Repository
}

type fakeResourceRepository struct{}

func (fakeResourceRepository) Create(
	context.Context,
	*security.UserID,
	resource.Resource,
	resource.ValidateImageMedia,
) (resource.Resource, error) {
	return resource.Resource{}, resource.ErrNotFound
}

func (fakeResourceRepository) ByID(
	context.Context,
	resource.ID,
) (resource.Resource, error) {
	return resource.Resource{}, resource.ErrNotFound
}

func (fakeResourceRepository) ByPath(
	context.Context,
	site.ID,
	string,
) (resource.Resource, error) {
	return resource.Resource{}, resource.ErrNotFound
}

func (fakeResourceRepository) ListBySite(
	context.Context,
	site.ID,
) ([]resource.Resource, error) {
	return nil, nil
}

func (fakeResourceRepository) ExistsInSite(
	context.Context,
	site.ID,
	resource.ID,
) (bool, error) {
	return false, nil
}

func (fakeResourceRepository) ListChildren(
	context.Context,
	site.ID,
	*resource.ID,
) ([]resource.Child, error) {
	return nil, nil
}

func (fakeResourceRepository) Update(
	context.Context,
	*security.UserID,
	resource.Resource,
	resource.Resource,
	resource.ValidateImageMedia,
) (resource.Resource, error) {
	return resource.Resource{}, resource.ErrNotFound
}

func (fakeResourceRepository) Delete(
	context.Context,
	resource.ID,
) error {
	return resource.ErrNotFound
}

func (fakeResourceRepository) SoftDelete(context.Context, *security.UserID, resource.ID) error {
	return resource.ErrNotFound
}

func (fakeResourceRepository) Restore(context.Context, *security.UserID, resource.ID, bool) error {
	return resource.ErrNotFound
}

type appResourceRepository struct {
	mu     sync.Mutex
	nextID resource.ID
	items  map[resource.ID]resource.Resource
}

type appFileRepository struct {
	corefile.Repository
	item corefile.File
}

func (r appFileRepository) FileByID(
	_ context.Context,
	id corefile.ID,
) (corefile.File, error) {
	if r.item.ID != id {
		return corefile.File{}, corefile.ErrNotFound
	}
	return corefile.Clone(r.item), nil
}

type appMediaRepository struct {
	mu     sync.Mutex
	nextID coremedia.ID
	items  map[coremedia.ID]coremedia.Media
}

func newAppMediaRepository() *appMediaRepository {
	return &appMediaRepository{
		nextID: 1,
		items:  make(map[coremedia.ID]coremedia.Media),
	}
}

func (r *appMediaRepository) Create(
	_ context.Context,
	_ *security.UserID,
	item coremedia.Media,
) (coremedia.Media, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	item = coremedia.Clone(item)
	item.ID = r.nextID
	r.nextID++
	r.items[item.ID] = item
	return coremedia.Clone(item), nil
}

func (r *appMediaRepository) ByID(
	_ context.Context,
	id coremedia.ID,
) (coremedia.Media, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	item, exists := r.items[id]
	if !exists {
		return coremedia.Media{}, coremedia.ErrNotFound
	}
	return coremedia.Clone(item), nil
}

func (r *appMediaRepository) Update(
	ctx context.Context,
	_ *security.UserID,
	item coremedia.Media,
	validate coremedia.ValidateUsages,
) (coremedia.Media, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.items[item.ID]; !exists {
		return coremedia.Media{}, coremedia.ErrNotFound
	}
	if err := validate(ctx, nil); err != nil {
		return coremedia.Media{}, err
	}
	r.items[item.ID] = coremedia.Clone(item)
	return coremedia.Clone(item), nil
}

func (r *appMediaRepository) Delete(
	_ context.Context,
	id coremedia.ID,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.items[id]; !exists {
		return coremedia.ErrNotFound
	}
	delete(r.items, id)
	return nil
}

func newAppResourceRepository() *appResourceRepository {
	return &appResourceRepository{
		nextID: 1,
		items:  make(map[resource.ID]resource.Resource),
	}
}

func (r *appResourceRepository) Create(
	_ context.Context,
	_ *security.UserID,
	item resource.Resource,
	_ resource.ValidateImageMedia,
) (resource.Resource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	item = resource.Clone(item)
	item.ID = r.nextID
	item.Version = 1
	r.nextID++
	r.items[item.ID] = item
	return resource.Clone(item), nil
}

func (r *appResourceRepository) ByID(
	_ context.Context,
	id resource.ID,
) (resource.Resource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	item, exists := r.items[id]
	if !exists {
		return resource.Resource{}, resource.ErrNotFound
	}
	return resource.Clone(item), nil
}

func (r *appResourceRepository) ByPath(
	_ context.Context,
	siteID site.ID,
	path string,
) (resource.Resource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, item := range r.items {
		if item.SiteID == siteID &&
			item.Path != nil &&
			*item.Path == path {
			return resource.Clone(item), nil
		}
	}
	return resource.Resource{}, resource.ErrNotFound
}

func (r *appResourceRepository) ListBySite(
	_ context.Context,
	siteID site.ID,
) ([]resource.Resource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]resource.Resource, 0)
	for _, item := range r.items {
		if item.SiteID == siteID {
			result = append(result, resource.Clone(item))
		}
	}
	return result, nil
}

func (r *appResourceRepository) ExistsInSite(
	_ context.Context,
	siteID site.ID,
	id resource.ID,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, exists := r.items[id]
	return exists && item.SiteID == siteID, nil
}

func (r *appResourceRepository) ListChildren(
	ctx context.Context,
	siteID site.ID,
	parentID *resource.ID,
) ([]resource.Child, error) {
	items, err := r.ListBySite(ctx, siteID)
	if err != nil {
		return nil, err
	}
	if parentID != nil {
		exists := false
		for _, item := range items {
			if item.ID == *parentID {
				exists = true
				break
			}
		}
		if !exists {
			return nil, resource.ErrNotFound
		}
	}
	result := make([]resource.Child, 0)
	for _, item := range items {
		if (item.ParentID == nil) != (parentID == nil) ||
			(item.ParentID != nil && *item.ParentID != *parentID) {
			continue
		}
		result = append(result, resource.Child{
			ID: item.ID, Version: item.Version, SiteID: item.SiteID, ParentID: item.ParentID,
			Type: item.Type, Template: item.Template, Title: item.Title, MenuTitle: item.MenuTitle,
		})
	}
	return result, nil
}

func (r *appResourceRepository) Update(
	_ context.Context,
	_ *security.UserID,
	current resource.Resource,
	item resource.Resource,
	_ resource.ValidateImageMedia,
) (resource.Resource, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.items[item.ID]; !exists {
		return resource.Resource{}, resource.ErrNotFound
	}
	item.Version = current.Version + 1
	r.items[item.ID] = resource.Clone(item)
	return resource.Clone(item), nil
}

func (r *appResourceRepository) Delete(
	_ context.Context,
	id resource.ID,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.items[id]; !exists {
		return resource.ErrNotFound
	}
	delete(r.items, id)
	return nil
}

func (r *appResourceRepository) SoftDelete(
	_ context.Context,
	actorID *security.UserID,
	id resource.ID,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, exists := r.items[id]
	if !exists {
		return resource.ErrNotFound
	}
	now := time.Now().UTC()
	item.DeletedAt = &now
	item.DeletedBy = actorID
	r.items[id] = item
	return nil
}

func (r *appResourceRepository) Restore(
	_ context.Context,
	_ *security.UserID,
	id resource.ID,
	_ bool,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, exists := r.items[id]
	if !exists {
		return resource.ErrNotFound
	}
	item.DeletedAt = nil
	item.DeletedBy = nil
	r.items[id] = item
	return nil
}

func (*fakeCoreDatabase) MigrationSources() []migrations.Source {
	return []migrations.Source{versionedSource("migration")}
}

func (d *fakeCoreDatabase) SeedSources() []seeds.Source {
	if d.seedSources != nil {
		return d.seedSources
	}

	return []seeds.Source{seedSource("defaults", "dev", "seed")}
}

type fakeFeatureDatabase struct {
	name        string
	seedSources []seeds.Source
}

func (*fakeFeatureDatabase) ModuleCode() kernel.ModuleCode {
	return featureModuleCode
}

func (d *fakeFeatureDatabase) SeedSources() []seeds.Source {
	return d.seedSources
}

type fakeDatabaseFactory struct {
	code     kernel.ModuleCode
	database kernel.ModuleDatabase
	err      error
	builds   atomic.Int32
}

func (f *fakeDatabaseFactory) ModuleCode() kernel.ModuleCode { return f.code }

func (f *fakeDatabaseFactory) Build(
	kernel.DBConnector,
) (kernel.ModuleDatabase, error) {
	f.builds.Add(1)
	return f.database, f.err
}

type featureConfig struct {
	Connection kernel.ConnectionCode
}

type featureModule struct {
	builds   *atomic.Int32
	selected **fakeFeatureDatabase
}

func (*featureModule) Code() kernel.ModuleCode { return featureModuleCode }

func (m *featureModule) Build(
	_ context.Context,
	ctx kernel.ModuleContext,
) (kernel.ModuleRuntime, error) {
	m.builds.Add(1)

	config, err := kernel.ModuleConfigFrom[featureConfig](ctx)
	if err != nil {
		return nil, err
	}
	database, err := kernel.ModuleDatabaseFrom[*fakeFeatureDatabase](
		ctx,
		config.Connection,
		featureModuleCode,
	)
	if err != nil {
		return nil, err
	}

	*m.selected = database
	return featureRuntime{}, nil
}

func (*featureModule) Commands() []console.Command {
	return []console.Command{testCommand{name: "feature-info"}}
}

type featureRuntime struct{}

func (featureRuntime) ModuleCode() kernel.ModuleCode { return featureModuleCode }

type backgroundLifecycleModule struct{ events chan string }
type backgroundLifecycleRuntime struct {
	scope      string
	generation string
	events     chan string
}

func (backgroundLifecycleModule) Code() kernel.ModuleCode { return "background_lifecycle" }
func (m backgroundLifecycleModule) Build(_ context.Context, ctx kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return backgroundLifecycleRuntime{
		scope: ctx.Scope().SiteID(), generation: fmt.Sprint(ctx.Scope().Settings()["generation"]), events: m.events,
	}, nil
}
func (backgroundLifecycleRuntime) ModuleCode() kernel.ModuleCode { return "background_lifecycle" }
func (r backgroundLifecycleRuntime) BackgroundTasks() []background.Task {
	return []background.Task{{Name: "shared_task", Run: func(ctx context.Context) error {
		r.events <- r.scope + ":" + r.generation + ":start"
		<-ctx.Done()
		r.events <- r.scope + ":" + r.generation + ":stop"
		return nil
	}}}
}

type testCommand struct{ name string }

func (c testCommand) Name() string      { return c.name }
func (testCommand) Description() string { return "test feature command" }
func (c testCommand) Run(
	_ context.Context,
	_ []string,
	streams console.IO,
) error {
	_, err := streams.Out.Write([]byte(c.name))
	return err
}

func versionedSource(contents string) migrations.Source {
	return migrations.Source{
		ID:     string(core.ModuleCode),
		Schema: "core",
		FS: fstest.MapFS{
			"000001_core.up.sql": {
				Data: []byte("UP " + contents),
			},
			"000001_core.down.sql": {
				Data: []byte("DOWN " + contents),
			},
		},
		Path: ".",
	}
}

func seedSource(
	id string,
	tag seeds.Tag,
	contents string,
) seeds.Source {
	source := versionedSource(contents)

	return seeds.Source{
		ID:     id,
		Tags:   []seeds.Tag{tag},
		Schema: source.Schema,
		FS:     source.FS,
		Path:   source.Path,
	}
}

func TestAppRequiresHealthyLoggerBeforeInfrastructure(t *testing.T) {
	loggerClosed := false
	databaseOpened := false
	loggerConnector := &fakeLoggerConnector{
		logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		pingErr: errors.New("Loki is not ready"),
		onClose: func() {
			loggerClosed = true
		},
	}
	databaseConnector := newFakeConnector("main")
	_, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           fakeLoggerFactory{connector: loggerConnector},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus:         fakeEventBusFactory{},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{
					connector: databaseConnector,
					onOpen: func() {
						databaseOpened = true
					},
				},
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "Loki is not ready") {
		t.Fatalf("New error = %v", err)
	}
	if databaseOpened {
		t.Fatal("database opened before logger readiness succeeded")
	}
	if !loggerClosed {
		t.Fatal("failed logger connector was not closed")
	}

	if _, err := appkernel.New(
		context.Background(),
		appkernel.Definition{},
	); err == nil || !strings.Contains(err.Error(), "logger factory is nil") {
		t.Fatalf("nil logger error = %v", err)
	}
	if _, err := appkernel.New(
		context.Background(),
		appkernel.Definition{Logger: fakeLoggerFactory{}},
	); err == nil || !strings.Contains(
		err.Error(),
		"event bus factory is nil",
	) {
		t.Fatalf("nil event bus error = %v", err)
	}
	if _, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           fakeLoggerFactory{},
			EventBus:         fakeEventBusFactory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		},
	); err == nil || !strings.Contains(err.Error(), "password hasher factory is nil") {
		t.Fatalf("nil password hasher factory error = %v", err)
	}
}

func TestAppRequiresHealthyEventBusBeforeOtherInfrastructure(t *testing.T) {
	eventBusClosed := false
	databaseOpened := false
	eventBusConnector := &fakeEventBusConnector{
		pingErr: errors.New("Kafka is not ready"),
		onClose: func() {
			eventBusClosed = true
		},
	}
	_, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           fakeLoggerFactory{},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus: fakeEventBusFactory{
				connector: eventBusConnector,
			},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{
					connector: newFakeConnector("main"),
					onOpen: func() {
						databaseOpened = true
					},
				},
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "Kafka is not ready") {
		t.Fatalf("New error = %v", err)
	}
	if databaseOpened {
		t.Fatal("database opened before event bus readiness succeeded")
	}
	if !eventBusClosed {
		t.Fatal("failed event bus connector was not closed")
	}

	partiallyOpenedClosed := false
	_, err = appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           fakeLoggerFactory{},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus: fakeEventBusFactory{
				connector: &fakeEventBusConnector{
					onClose: func() {
						partiallyOpenedClosed = true
					},
				},
				err: errors.New("event bus open failed"),
			},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{
					connector: newFakeConnector("main"),
				},
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "event bus open failed") {
		t.Fatalf("partial event bus open error = %v", err)
	}
	if !partiallyOpenedClosed {
		t.Fatal("partially opened event bus connector was not closed")
	}
}

func TestAppOpensLoggerThenEventBusAndClosesLoggerLast(t *testing.T) {
	var events []string
	record := func(event string) func() {
		return func() {
			events = append(events, event)
		}
	}
	loggerConnector := &fakeLoggerConnector{
		logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		onPing:  record("logger.ping"),
		onClose: record("logger.close"),
	}
	eventBusConnector := &fakeEventBusConnector{
		onPing:  record("eventbus.ping"),
		onClose: record("eventbus.close"),
	}
	databaseConnector := newFakeConnector("main")
	databaseConnector.onPing = record("database.ping")
	databaseConnector.onClose = record("database.close")

	application, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger: fakeLoggerFactory{
				connector: loggerConnector,
				onOpen:    record("logger.open"),
			},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus: fakeEventBusFactory{
				connector: eventBusConnector,
				onOpen:    record("eventbus.open"),
			},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{
					connector: databaseConnector,
					onOpen:    record("database.open"),
				},
				Adapters: []kernel.ModuleDatabaseFactory{
					&fakeDatabaseFactory{
						code: core.ModuleCode,
						database: &fakeCoreDatabase{
							repository: &fakeSiteRepository{},
						},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	expected := []string{
		"logger.open",
		"logger.ping",
		"eventbus.open",
		"eventbus.ping",
		"database.open",
		"database.ping",
		"eventbus.close",
		"database.close",
		"logger.close",
	}
	if strings.Join(events, ",") != strings.Join(expected, ",") {
		t.Fatalf("lifecycle order = %v", events)
	}
	if application.EventBus() != eventBusConnector {
		t.Fatal("App.EventBus did not expose the configured bus")
	}
}

func TestAppCloseStopsActiveEventBusConsumerBeforeDatabase(t *testing.T) {
	eventBusConnector := newLifecycleEventBusConnector()
	databaseConnector := newFakeConnector("main")
	handlerStarted := make(chan struct{})
	handlerStopped := atomic.Bool{}
	databaseClosedTooEarly := atomic.Bool{}
	databaseConnector.onClose = func() {
		if !handlerStopped.Load() {
			databaseClosedTooEarly.Store(true)
		}
	}

	application, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           fakeLoggerFactory{},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus: fakeEventBusFactory{
				connector: eventBusConnector,
			},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{
					connector: databaseConnector,
				},
				Adapters: []kernel.ModuleDatabaseFactory{
					&fakeDatabaseFactory{
						code: core.ModuleCode,
						database: &fakeCoreDatabase{
							repository: &fakeSiteRepository{},
						},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	consumerResult := make(chan error, 1)
	go func() {
		consumerResult <- application.EventBus().Consume(
			context.Background(),
			eventbus.Subscription{
				Topics: []string{"topic"},
				Group:  "group",
			},
			func(ctx context.Context, _ eventbus.Message) error {
				close(handlerStarted)
				<-ctx.Done()
				handlerStopped.Store(true)
				return ctx.Err()
			},
		)
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("event bus handler did not start")
	}

	closeResult := make(chan error, 1)
	go func() {
		closeResult <- application.Close()
	}()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("App.Close deadlocked with an active event bus handler")
	}
	if err := <-consumerResult; err != nil {
		t.Fatal(err)
	}
	if databaseClosedTooEarly.Load() {
		t.Fatal("database closed before the event bus handler stopped")
	}
}

func TestAppOutboxPublisherStartsAfterMigrationAndStopsBeforeDependencies(t *testing.T) {
	var eventsMu sync.Mutex
	var events []string
	record := func(value string) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, value)
	}
	databaseConnector := newFakeConnector("main")
	source := &blockingOutboxSource{started: make(chan struct{})}
	source.onStart = func() {
		version, exists := databaseConnector.version(migrations.DefaultHistoryTable)
		if !exists || version != 1 {
			record("worker.started.before.migration")
		} else {
			record("worker.start")
		}
	}
	source.onStop = func() { record("worker.stop") }
	databaseConnector.onClose = func() { record("database.close") }
	eventBusConnector := &fakeEventBusConnector{onClose: func() { record("eventbus.close") }}
	application, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger: fakeLoggerFactory{}, PasswordHasher: argon2id.Factory{}, SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus: fakeEventBusFactory{connector: eventBusConnector},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: &fakeConnectorFactory{connector: databaseConnector},
			Adapters: []kernel.ModuleDatabaseFactory{&fakeDatabaseFactory{code: core.ModuleCode, database: &fakeCoreDatabase{
				repository: &fakeSiteRepository{}, outboxSources: []outbox.Source{source},
			}}},
		},
		Profiles:        []kernel.Profile{{Code: "dev", Modules: []kernel.ProfileModule{{Module: core.Module{}}, {Module: admin.Module{}}}}},
		OutboxPublisher: outbox.PublisherConfig{PollInterval: time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.started:
		t.Fatal("outbox publisher started before Boot")
	default:
	}
	if err := application.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("outbox publisher did not start")
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	eventsMu.Lock()
	actual := append([]string(nil), events...)
	eventsMu.Unlock()
	if strings.Join(actual, ",") != "worker.start,worker.stop,eventbus.close,database.close" {
		t.Fatalf("outbox lifecycle order = %#v", actual)
	}
}

func TestAppCloseJoinsEventBusDatabaseAndLoggerErrors(t *testing.T) {
	eventBusCloseErr := errors.New("event bus close failed")
	databaseCloseErr := errors.New("database close failed")
	loggerCloseErr := errors.New("logger close failed")
	databaseConnector := newFakeConnector("main")
	databaseConnector.closeErr = databaseCloseErr

	application, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger: fakeLoggerFactory{
				connector: &fakeLoggerConnector{
					logger: slog.New(
						slog.NewJSONHandler(io.Discard, nil),
					),
					closeErr: loggerCloseErr,
				},
			},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus: fakeEventBusFactory{
				connector: &fakeEventBusConnector{
					closeErr: eventBusCloseErr,
				},
			},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{
					connector: databaseConnector,
				},
				Adapters: []kernel.ModuleDatabaseFactory{
					&fakeDatabaseFactory{
						code: core.ModuleCode,
						database: &fakeCoreDatabase{
							repository: &fakeSiteRepository{},
						},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	closeErr := application.Close()
	for _, expected := range []error{
		eventBusCloseErr,
		databaseCloseErr,
		loggerCloseErr,
	} {
		if !errors.Is(closeErr, expected) {
			t.Fatalf("close error %q was lost: %v", expected, closeErr)
		}
	}
	if secondCloseErr := application.Close(); secondCloseErr != closeErr {
		t.Fatalf(
			"idempotent Close returned different error: first=%v second=%v",
			closeErr,
			secondCloseErr,
		)
	}
}

func TestAppLogsBootAndCloseFailures(t *testing.T) {
	var logs bytes.Buffer
	loggerConnector := &fakeLoggerConnector{
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	databaseConnector := newFakeConnector("main")
	databaseConnector.closeErr = errors.New("database close failed")
	application, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           fakeLoggerFactory{connector: loggerConnector},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus:         fakeEventBusFactory{},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{
					connector: databaseConnector,
				},
				Adapters: []kernel.ModuleDatabaseFactory{
					&fakeDatabaseFactory{
						code: core.ModuleCode,
						database: &fakeCoreDatabase{
							repository: &fakeSiteRepository{},
						},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Boot(nil); err == nil {
		t.Fatal("nil boot context was accepted")
	}
	if err := application.Close(); err == nil {
		t.Fatal("database close failure was lost")
	}
	for _, event := range []string{
		"app.boot.failed",
		"app.shutdown.failed",
	} {
		if !strings.Contains(logs.String(), event) {
			t.Fatalf("event %q is missing from logs: %s", event, logs.String())
		}
	}
}

func TestAppNewBootConsoleAndRuntimeLifecycle(t *testing.T) {
	ctx := context.Background()
	mainConnector := newFakeConnector("main")
	logsConnector := newFakeConnector("logs")

	repository := &fakeSiteRepository{
		sites: []site.Site{
			{
				ID:          1,
				ProfileCode: "dev",
				Domain:      "Example.COM.",
				Locale:      "ru-RU",
				Settings: map[string]any{
					"theme": "light",
					"roles": []any{"admin"},
				},
			},
			{
				ID:          2,
				ProfileCode: "dev",
				Domain:      "second.example.com",
				Locale:      "ru-RU",
			},
		},
	}
	repository.beforeList = func() {
		version, exists := mainConnector.version(migrations.DefaultHistoryTable)
		if !exists || version != 1 {
			t.Fatalf("repository called before migration up: %d, %t", version, exists)
		}
	}

	mainFeature := &fakeFeatureDatabase{name: "main"}
	logsFeature := &fakeFeatureDatabase{name: "logs"}
	coreDatabase := &fakeCoreDatabase{repository: repository}

	var moduleBuilds atomic.Int32
	var selected *fakeFeatureDatabase
	module := &featureModule{builds: &moduleBuilds, selected: &selected}

	application, err := appkernel.New(ctx, appkernel.Definition{
		Logger:           fakeLoggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         fakeEventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: &fakeConnectorFactory{connector: mainConnector},
			Adapters: []kernel.ModuleDatabaseFactory{
				&fakeDatabaseFactory{code: core.ModuleCode, database: coreDatabase},
				&fakeDatabaseFactory{code: featureModuleCode, database: mainFeature},
			},
		},
		AdditionalDatabases: []appkernel.DatabaseDefinition{
			{
				Connector: &fakeConnectorFactory{connector: logsConnector},
				Adapters: []kernel.ModuleDatabaseFactory{
					&fakeDatabaseFactory{code: featureModuleCode, database: logsFeature},
				},
			},
		},
		Profiles: []kernel.Profile{
			{
				Code: "dev",
				Params: []field.Definition{
					{
						Key:   "theme",
						Type:  field.TypeString,
						Label: "Theme",
					},
					{
						Key:   "roles",
						Type:  field.TypeSelect,
						Label: "Roles",
						Options: field.SelectOptions{
							Multiple: true,
							Choices: []field.Choice{
								{Value: "admin", Label: "Admin"},
							},
						},
					},
				},
				Modules: []kernel.ProfileModule{
					{Module: core.Module{}},
					{Module: admin.Module{}},
					{
						Module: module,
						Config: featureConfig{Connection: "logs"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if mainConnector.pings.Load() != 1 || logsConnector.pings.Load() != 1 {
		t.Fatalf(
			"ping counts = main:%d logs:%d",
			mainConnector.pings.Load(),
			logsConnector.pings.Load(),
		)
	}
	if repository.callCount() != 0 {
		t.Fatalf("New called site repository %d times", repository.callCount())
	}
	if application.Authorization() != nil {
		t.Fatal("Authorization before Boot is available")
	}
	if _, err := application.RuntimeByDomain(
		ctx,
		security.System(),
		"example.com",
	); !errors.Is(err, appkernel.ErrNotBooted) {
		t.Fatalf("RuntimeByDomain before Boot = %v", err)
	}
	if err := application.ReloadSites(ctx); !errors.Is(err, appkernel.ErrNotBooted) {
		t.Fatalf("ReloadSites before Boot = %v", err)
	}
	if application.Sites() != nil {
		t.Fatal("Sites before Boot is available")
	}
	if application.Media() != nil {
		t.Fatal("Media before Boot is available")
	}
	if _, exists := mainConnector.version(migrations.DefaultHistoryTable); exists {
		t.Fatal("New applied migrations")
	}
	if _, exists := mainConnector.version(
		seeds.HistoryTable("defaults"),
	); exists {
		t.Fatal("New applied seeds")
	}
	consoleDatabase, exists := application.Console().Application().ModuleDatabase(
		"logs",
		featureModuleCode,
	)
	if !exists || consoleDatabase != logsFeature {
		t.Fatalf("console resolved database = %#v", consoleDatabase)
	}

	var consoleOutput bytes.Buffer
	if err := application.Console().Run(
		ctx,
		[]string{"feature-info"},
		console.IO{Out: &consoleOutput},
	); err != nil {
		t.Fatal(err)
	}
	if consoleOutput.String() != "feature-info" {
		t.Fatalf("custom command output = %q", consoleOutput.String())
	}

	consoleOutput.Reset()
	if err := application.Console().Run(
		ctx,
		[]string{"migrations", "status"},
		console.IO{Out: &consoleOutput},
	); err != nil {
		t.Fatal(err)
	}
	mainConnector.mu.Lock()
	mainConnector.drivers[migrations.DefaultHistoryTable].CurrentVersion = 1
	mainConnector.drivers[migrations.DefaultHistoryTable].IsDirty = true
	mainConnector.mu.Unlock()

	consoleOutput.Reset()
	if err := application.Console().Run(
		ctx,
		[]string{
			"migrations",
			"force",
			"-connection=main",
			"-module=core",
			"-version=-1",
		},
		console.IO{Out: &consoleOutput},
	); err != nil {
		t.Fatalf("repair dirty migration before Boot: %v", err)
	}

	if err := application.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := application.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	if moduleBuilds.Load() != 2 {
		t.Fatalf("module Build calls = %d", moduleBuilds.Load())
	}
	if codes := application.Authorization().Codes(); len(codes) != 30 {
		t.Fatalf("permission catalog = %#v", codes)
	}
	if selected != logsFeature {
		t.Fatalf("selected database = %#v", selected)
	}
	if repository.callCount() != 1 {
		t.Fatalf("repository calls after Boot = %d", repository.callCount())
	}
	if _, exists := mainConnector.version(seeds.DefaultHistoryTable); exists {
		t.Fatal("Boot applied seeds")
	}

	first, err := application.RuntimeByDomain(
		ctx,
		security.System(),
		"EXAMPLE.com.:443",
	)
	if err != nil {
		t.Fatalf("runtime not found by Host with port: %v", err)
	}
	second, err := application.RuntimeByDomain(
		ctx,
		security.System(),
		"example.com",
	)
	if err != nil || first != second {
		t.Fatal("same domain did not return the same runtime")
	}
	other, err := application.RuntimeByDomain(
		ctx,
		security.System(),
		"second.example.com",
	)
	if err != nil || other == first {
		t.Fatal("different site did not get a distinct runtime")
	}
	if first.Profile() == other.Profile() {
		t.Fatal("sites of one profile share final profile runtime")
	}
	if first.Profile().Blueprint() != other.Profile().Blueprint() {
		t.Fatal("sites of one profile do not share immutable blueprint")
	}
	firstCore, _ := first.Profile().Registry().Module(core.ModuleCode)
	otherCore, _ := other.Profile().Registry().Module(core.ModuleCode)
	if firstCore == nil || otherCore == nil || firstCore == otherCore {
		t.Fatal("sites of one profile share core module runtime")
	}
	firstCopy := first.Site()
	firstCopy.Settings["roles"].([]string)[0] = "mutated"
	if first.Site().Settings["roles"].([]string)[0] != "admin" {
		t.Fatal("site runtime exposed mutable settings slice")
	}

	_, err = application.Sites().Update(
		ctx,
		security.System(),
		site.UpdateInput{
			ID:          1,
			ProfileCode: "dev",
			Domain:      first.Site().Domain,
			Locale:      first.Site().Locale,
			Settings:    map[string]any{"unknown": "value"},
			IsPublic:    first.Site().IsPublic,
		},
	)
	var validationErrors field.ValidationErrors
	if !errors.As(err, &validationErrors) {
		t.Fatalf("invalid settings error = %T %v", err, err)
	}
	unchanged, lookupErr := application.RuntimeByDomain(
		ctx,
		security.System(),
		"example.com",
	)
	if lookupErr != nil || unchanged != first {
		t.Fatal("validation failure changed site runtime")
	}
	if repository.updateCallCount() != 0 {
		t.Fatal("validation failure called site repository")
	}

	updated, err := application.Sites().Update(
		ctx,
		security.System(),
		site.UpdateInput{
			ID:          1,
			ProfileCode: "dev",
			Domain:      "renamed.example.com",
			Locale:      first.Site().Locale,
			Settings:    map[string]any{"theme": "dark"},
			IsPublic:    true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Site().Settings["theme"] != "dark" {
		t.Fatalf("updated settings = %#v", updated.Site().Settings)
	}
	if _, lookupErr := application.RuntimeByDomain(
		ctx,
		security.System(),
		"example.com",
	); !errors.Is(lookupErr, site.ErrNotFound) {
		t.Fatalf("old domain still resolves: %v", lookupErr)
	}
	currentByDomain, lookupErr := application.RuntimeByDomain(
		ctx,
		security.System(),
		"renamed.example.com",
	)
	if lookupErr != nil ||
		currentByDomain != updated ||
		currentByDomain == first {
		t.Fatal("successful settings update did not replace runtime")
	}
	if repository.updateCallCount() != 1 {
		t.Fatalf("repository update calls = %d", repository.updateCallCount())
	}

	repository.setUpdateError(errors.New("update unavailable"))
	_, err = application.Sites().Update(
		ctx,
		security.System(),
		site.UpdateInput{
			ID:          1,
			ProfileCode: "dev",
			Domain:      updated.Site().Domain,
			Locale:      updated.Site().Locale,
			Settings:    map[string]any{"theme": "broken"},
			IsPublic:    updated.Site().IsPublic,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "update unavailable") {
		t.Fatalf("repository update error = %v", err)
	}
	preservedAfterUpdateError, lookupErr := application.RuntimeByDomain(
		ctx,
		security.System(),
		"renamed.example.com",
	)
	if lookupErr != nil || preservedAfterUpdateError != updated {
		t.Fatal("repository failure changed site runtime")
	}
	repository.setUpdateError(nil)

	if _, err := application.Sites().Update(
		ctx,
		security.System(),
		site.UpdateInput{
			ID:       999,
			Domain:   "missing.example.com",
			Locale:   "en-US",
			Settings: map[string]any{"theme": "missing"},
		},
	); !errors.Is(err, site.ErrNotFound) {
		t.Fatalf("missing site update error = %v", err)
	}

	consoleOutput.Reset()
	if err := application.Console().Run(
		ctx,
		[]string{"seeds", "up", "-tags=dev"},
		console.IO{Out: &consoleOutput},
	); err != nil {
		t.Fatal(err)
	}
	seedVersion, exists := mainConnector.version(
		seeds.HistoryTable("defaults"),
	)
	if !exists || seedVersion != 1 {
		t.Fatalf("seed version = %d, exists = %t", seedVersion, exists)
	}

	repository.set([]site.Site{
		{ID: 3, ProfileCode: "dev", Domain: "new.example.com", Locale: "en-US"},
	}, nil)
	if err := application.ReloadSites(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := application.RuntimeByDomain(
		ctx,
		security.System(),
		"new.example.com",
	)
	if err != nil {
		t.Fatalf("reloaded runtime not found: %v", err)
	}

	repository.set([]site.Site{
		{
			ID:          4,
			ProfileCode: "dev",
			Domain:      "invalid.example.com",
			Locale:      "en-US",
			Settings:    map[string]any{"unknown": true},
		},
	}, nil)
	if err := application.ReloadSites(ctx); err == nil {
		t.Fatal("expected invalid stored settings error")
	}
	preserved, lookupErr := application.RuntimeByDomain(
		ctx,
		security.System(),
		"new.example.com",
	)
	if lookupErr != nil || preserved != current {
		t.Fatal("invalid settings reload replaced the previous snapshot")
	}

	repository.set(nil, errors.New("database unavailable"))
	if err := application.ReloadSites(ctx); err == nil {
		t.Fatal("expected reload error")
	}
	preserved, lookupErr = application.RuntimeByDomain(
		ctx,
		security.System(),
		"new.example.com",
	)
	if lookupErr != nil || preserved != current {
		t.Fatal("failed reload replaced the previous snapshot")
	}

	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	if application.Media() != nil {
		t.Fatal("Media after Close is available")
	}
	if application.Authorization() != nil {
		t.Fatal("Authorization after Close is available")
	}
	if mainConnector.closes.Load() != 1 || logsConnector.closes.Load() != 1 {
		t.Fatalf(
			"close counts = main:%d logs:%d",
			mainConnector.closes.Load(),
			logsConnector.closes.Load(),
		)
	}
}

func TestAppRunsBackgroundTasksPerSiteAndReplacesStaleRuntimeTasks(t *testing.T) {
	ctx := context.Background()
	events := make(chan string, 32)
	repository := &fakeSiteRepository{sites: []site.Site{
		{ID: 1, ProfileCode: "dev", Domain: "one.example.com", Locale: "en-US", Settings: map[string]any{"generation": "a1"}},
		{ID: 2, ProfileCode: "dev", Domain: "two.example.com", Locale: "en-US", Settings: map[string]any{"generation": "b1"}},
	}}
	application, err := appkernel.New(ctx, appkernel.Definition{
		Logger: fakeLoggerFactory{}, PasswordHasher: argon2id.Factory{}, SiteAccessPolicy: admin.AllowAllSitesPolicy{}, EventBus: fakeEventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{Connector: &fakeConnectorFactory{connector: newFakeConnector("main")}, Adapters: []kernel.ModuleDatabaseFactory{&fakeDatabaseFactory{
			code: core.ModuleCode, database: &fakeCoreDatabase{repository: repository},
		}}},
		Profiles: []kernel.Profile{{Code: "dev", Params: []field.Definition{{Key: "generation", Type: field.TypeString, Label: "Generation"}}, Modules: []kernel.ProfileModule{{Module: core.Module{}}, {Module: admin.Module{}}, {Module: backgroundLifecycleModule{events: events}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()
	if err := application.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	waitBackgroundEvents(t, events, "1:a1:start", "2:b1:start")

	if _, err := application.Sites().Update(ctx, security.System(), site.UpdateInput{
		ID: 1, ProfileCode: "dev", Domain: "one.example.com", Locale: "en-US", Settings: map[string]any{"generation": "a2"},
	}); err != nil {
		t.Fatal(err)
	}
	waitBackgroundEvents(t, events, "1:a1:stop", "1:a2:start")
	select {
	case event := <-events:
		t.Fatalf("unrelated site task was restarted: %s", event)
	case <-time.After(50 * time.Millisecond):
	}

	if err := application.Sites().Delete(ctx, security.System(), 2); err != nil {
		t.Fatal(err)
	}
	waitBackgroundEvents(t, events, "2:b1:stop")
}

func waitBackgroundEvents(t *testing.T, events <-chan string, expected ...string) {
	t.Helper()
	remaining := make(map[string]struct{}, len(expected))
	for _, event := range expected {
		remaining[event] = struct{}{}
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for len(remaining) > 0 {
		select {
		case event := <-events:
			if _, exists := remaining[event]; !exists {
				t.Fatalf("unexpected background event %q; waiting for %#v", event, remaining)
			}
			delete(remaining, event)
		case <-deadline.C:
			t.Fatalf("timed out waiting for background events %#v", remaining)
		}
	}
}

func TestAppResourceServices(t *testing.T) {
	ctx := context.Background()
	connector := newFakeConnector("main")
	resourceRepository := newAppResourceRepository()
	coreDatabase := &fakeCoreDatabase{
		repository: &fakeSiteRepository{
			sites: []site.Site{{
				ID:          1,
				ProfileCode: "dev",
				Domain:      "example.com",
				Locale:      "en-US",
			}},
		},
		resourceRepository: resourceRepository,
	}
	templateCode := template.Code("article")

	application, err := appkernel.New(ctx, appkernel.Definition{
		Logger:           fakeLoggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         fakeEventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: &fakeConnectorFactory{connector: connector},
			Adapters: []kernel.ModuleDatabaseFactory{
				&fakeDatabaseFactory{
					code:     core.ModuleCode,
					database: coreDatabase,
				},
			},
		},
		Profiles: []kernel.Profile{{
			Code: "dev",
			Modules: []kernel.ProfileModule{
				{Module: core.Module{}},
				{Module: admin.Module{}},
			},
			Templates: []template.Definition{{
				Code:  templateCode,
				Label: "Article",
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()

	if application.Resources() != nil {
		t.Fatal("resources before boot are available")
	}
	if err := application.Boot(ctx); err != nil {
		t.Fatal(err)
	}

	created, err := application.Resources().Create(
		ctx,
		security.System(),
		resource.CreateInput{
			SiteID:   1,
			Template: &templateCode,
			Title:    "Home",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.Type != resourcetype.Page ||
		created.Path == nil ||
		*created.Path != "/" {
		t.Fatalf("created resource = %#v", created)
	}

	byID, err := application.Resources().Get(ctx, security.System(), created.ID)
	if err != nil || byID.ID != created.ID {
		t.Fatalf("resource by id = %#v, %v", byID, err)
	}
	byPath, err := application.Resources().GetByPath(ctx, security.System(), 1, "/")
	if err != nil || byPath.ID != created.ID {
		t.Fatalf("resource by path = %#v, %v", byPath, err)
	}
	tree, err := application.Resources().Tree(ctx, security.System(), 1)
	if err != nil || len(tree) != 1 ||
		tree[0].Resource.ID != created.ID {
		t.Fatalf("resource tree = %#v, %v", tree, err)
	}

	updated, err := application.Resources().Update(
		ctx,
		security.System(),
		resource.UpdateInput{
			ID:              created.ID,
			ExpectedVersion: created.Version,
			Type:            resourcetype.Page,
			Template:        &templateCode,
			Title:           "Updated home",
			IsPublic:        true,
			IsSearchable:    true,
			InMenu:          true,
			InSitemap:       true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Updated home" {
		t.Fatalf("updated resource = %#v", updated)
	}

	if err := application.Resources().Delete(ctx, security.System(), created.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err := application.Resources().Get(
		ctx,
		security.System(),
		created.ID,
	)
	if err != nil || deleted.DeletedAt == nil {
		t.Fatalf("soft-deleted resource = %#v, %v", deleted, err)
	}
}

func TestAppResourceWriteInvalidatesSiteRuntimeRepositoryCache(t *testing.T) {
	ctx := context.Background()
	cacheStore := newTaggedCacheStore("repository")
	resourceRepository := newAppResourceRepository()
	coreDatabase := &fakeCoreDatabase{
		repository: &fakeSiteRepository{sites: []site.Site{{
			ID:          1,
			ProfileCode: "dev",
			Domain:      "example.com",
			Locale:      "en-US",
		}, {
			ID:          2,
			ProfileCode: "dev",
			Domain:      "second.example.com",
			Locale:      "en-US",
		}},
		},
		resourceRepository: resourceRepository,
	}
	templateCode := template.Code("article")

	application, err := appkernel.New(ctx, appkernel.Definition{
		Logger:           fakeLoggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         fakeEventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: &fakeConnectorFactory{connector: newFakeConnector("main")},
			Adapters: []kernel.ModuleDatabaseFactory{&fakeDatabaseFactory{
				code: core.ModuleCode, database: coreDatabase,
			}},
		},
		Caches: []cache.Factory{fakeCacheFactory{store: cacheStore}},
		Profiles: []kernel.Profile{{
			Code: "dev",
			Modules: []kernel.ProfileModule{
				{
					Module: core.Module{},
					Caches: []cache.Binding{{
						Alias: core.DurableCacheAlias,
						Code:  cacheStore.Code(),
					}},
				},
				{Module: admin.Module{}},
			},
			Templates: []template.Definition{{Code: templateCode, Label: "Article"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()
	if err := application.Boot(ctx); err != nil {
		t.Fatal(err)
	}

	created, err := application.Resources().Create(
		ctx,
		security.System(),
		resource.CreateInput{SiteID: 1, Template: &templateCode, Title: "Before"},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondResource, err := application.Resources().Create(
		ctx,
		security.System(),
		resource.CreateInput{SiteID: 2, Template: &templateCode, Title: "Second"},
	)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDatabase := func(id site.ID) core.Database {
		t.Helper()
		siteRuntime, exists := application.Sites().RuntimeByID(id)
		if !exists {
			t.Fatalf("site runtime %d is unavailable", id)
		}
		moduleRuntime, exists := siteRuntime.Profile().Registry().Module(core.ModuleCode)
		if !exists {
			t.Fatalf("core runtime for site %d is unavailable", id)
		}
		coreRuntime, ok := moduleRuntime.(*core.Runtime)
		if !ok {
			t.Fatalf("core runtime type = %T", moduleRuntime)
		}
		return coreRuntime.Database()
	}
	firstDatabase := runtimeDatabase(1)
	secondDatabase := runtimeDatabase(2)
	for _, database := range []core.Database{firstDatabase, secondDatabase} {
		cached, err := database.Resources().ByID(ctx, created.ID)
		if err != nil || cached.Title != "Before" {
			t.Fatalf("initial runtime read = %#v, %v", cached, err)
		}
	}
	if _, err := secondDatabase.Resources().ByID(ctx, secondResource.ID); err != nil {
		t.Fatal(err)
	}
	if count := cacheStore.entryCount(); count != 3 {
		t.Fatalf("populated runtime cache entries = %d", count)
	}
	updated, err := application.Resources().Update(
		ctx,
		security.System(),
		resource.UpdateInput{
			ID:              created.ID,
			ExpectedVersion: created.Version,
			Type:            created.Type,
			Template:        created.Template,
			Title:           "After",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "After" {
		t.Fatalf("application update = %#v", updated)
	}
	if count := cacheStore.entryCount(); count != 1 {
		t.Fatalf("resource update invalidated unrelated site cache: %d entries", count)
	}
	for _, database := range []core.Database{firstDatabase, secondDatabase} {
		fresh, err := database.Resources().ByID(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Title != "After" {
			t.Fatalf("runtime cached read remained stale: %#v", fresh)
		}
		if _, err := database.Sites().List(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if count := cacheStore.entryCount(); count != 5 {
		t.Fatalf("resource and site cache entries = %d", count)
	}
	updatedSite, err := application.Sites().Update(
		ctx,
		security.System(),
		site.UpdateInput{
			ID:          1,
			ProfileCode: "dev",
			Domain:      "renamed.example.com",
			Locale:      "en-US",
			Settings:    map[string]any{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if count := cacheStore.entryCount(); count != 1 {
		t.Fatalf("site update invalidation left stale entries: %d", count)
	}
	refreshedSites, err := runtimeDatabase(updatedSite.Site().ID).Sites().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshedSites) != 2 || refreshedSites[0].Domain != "renamed.example.com" {
		t.Fatalf("fresh runtime site list = %#v", refreshedSites)
	}
	if second, err := secondDatabase.Resources().ByID(ctx, secondResource.ID); err != nil || second.Title != "Second" {
		t.Fatalf("unrelated site cache = %#v, %v", second, err)
	}
}

func TestAppNewRequiresCoreAndAdminModules(t *testing.T) {
	tests := []struct {
		name    string
		modules []kernel.ProfileModule
		missing kernel.ModuleCode
	}{
		{
			name: "core",
			modules: []kernel.ProfileModule{
				{Module: admin.Module{}},
			},
			missing: core.ModuleCode,
		},
		{
			name: "admin",
			modules: []kernel.ProfileModule{
				{Module: core.Module{}},
			},
			missing: admin.ModuleCode,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := appkernel.New(
				context.Background(),
				appkernel.Definition{
					Logger:           fakeLoggerFactory{},
					PasswordHasher:   argon2id.Factory{},
					SiteAccessPolicy: admin.AllowAllSitesPolicy{},
					EventBus:         fakeEventBusFactory{},
					MainDatabase: appkernel.DatabaseDefinition{
						Connector: &fakeConnectorFactory{
							connector: newFakeConnector("main"),
						},
						Adapters: []kernel.ModuleDatabaseFactory{
							&fakeDatabaseFactory{
								code: core.ModuleCode,
								database: &fakeCoreDatabase{
									repository: &fakeSiteRepository{},
								},
							},
						},
					},
					Profiles: []kernel.Profile{{
						Code:    "dev",
						Modules: test.modules,
					}},
				},
			)
			if err == nil ||
				!strings.Contains(
					err.Error(),
					`required module "`+string(test.missing)+`"`,
				) {
				t.Fatalf("New error = %v", err)
			}
		})
	}
}

func TestAppNewValidatesCoreFirstDuplicatesAndValidOrder(t *testing.T) {
	tests := []struct {
		name    string
		modules []kernel.ProfileModule
		wantErr string
	}{
		{
			name: "core not first",
			modules: []kernel.ProfileModule{
				{Module: admin.Module{}},
				{Module: core.Module{}},
			},
			wantErr: `must declare module "core" first`,
		},
		{
			name: "duplicate module",
			modules: []kernel.ProfileModule{
				{Module: core.Module{}},
				{Module: admin.Module{}},
				{Module: admin.Module{}},
			},
			wantErr: `duplicate module "admin"`,
		},
		{
			name: "valid ordered profile",
			modules: []kernel.ProfileModule{
				{Module: core.Module{}},
				{Module: admin.Module{}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			application, err := appkernel.New(
				context.Background(),
				appkernel.Definition{
					Logger:           fakeLoggerFactory{},
					PasswordHasher:   argon2id.Factory{},
					SiteAccessPolicy: admin.AllowAllSitesPolicy{},
					EventBus:         fakeEventBusFactory{},
					MainDatabase: appkernel.DatabaseDefinition{
						Connector: &fakeConnectorFactory{
							connector: newFakeConnector("main"),
						},
						Adapters: []kernel.ModuleDatabaseFactory{&fakeDatabaseFactory{
							code: core.ModuleCode,
							database: &fakeCoreDatabase{
								repository: &fakeSiteRepository{},
							},
						}},
					},
					Profiles: []kernel.Profile{{Code: "dev", Modules: test.modules}},
				},
			)
			if application != nil {
				defer func() { _ = application.Close() }()
			}
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("New error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestAppMediaServices(t *testing.T) {
	ctx := context.Background()
	connector := newFakeConnector("main")
	mediaRepository := newAppMediaRepository()
	coreDatabase := &fakeCoreDatabase{
		repository: &fakeSiteRepository{
			sites: []site.Site{{
				ID:          1,
				ProfileCode: "dev",
				Domain:      "example.com",
				Locale:      "en-US",
			}},
		},
		fileRepository: appFileRepository{
			item: corefile.File{
				ID:       1,
				Name:     "image.png",
				MIMEType: "image/png",
				Size:     5,
			},
		},
		mediaRepository: mediaRepository,
	}

	application, err := appkernel.New(ctx, appkernel.Definition{
		Logger:           fakeLoggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         fakeEventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: &fakeConnectorFactory{connector: connector},
			Adapters: []kernel.ModuleDatabaseFactory{
				&fakeDatabaseFactory{
					code:     core.ModuleCode,
					database: coreDatabase,
				},
			},
		},
		Profiles: []kernel.Profile{{
			Code: "dev",
			Modules: []kernel.ProfileModule{
				{Module: core.Module{}},
				{Module: admin.Module{}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if application.Media() != nil {
		t.Fatal("media before boot is available")
	}
	if err := application.Boot(ctx); err != nil {
		t.Fatal(err)
	}

	title := " Hero "
	created, err := application.Media().Create(
		ctx,
		security.System(),
		coremedia.CreateInput{
			FileID: 1,
			Title:  &title,
			Params: map[string]any{"meta_alt": "Hero"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.Title == nil || *created.Title != "Hero" {
		t.Fatalf("created media = %#v", created)
	}

	loaded, err := application.Media().Get(ctx, security.System(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.FileID != 1 || loaded.Params["meta_alt"] != "Hero" {
		t.Fatalf("loaded media = %#v", loaded)
	}

	updatedTitle := "Updated"
	updated, err := application.Media().Update(
		ctx,
		security.System(),
		coremedia.UpdateInput{
			ID:     created.ID,
			FileID: 1,
			Title:  &updatedTitle,
			Params: map[string]any{"meta_alt": "Updated"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title == nil || *updated.Title != updatedTitle {
		t.Fatalf("updated media = %#v", updated)
	}

	if err := application.Media().Delete(ctx, security.System(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Media().Get(
		ctx,
		security.System(),
		created.ID,
	); !errors.Is(err, coremedia.ErrNotFound) {
		t.Fatalf("deleted media error = %v", err)
	}

	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	if application.Media() != nil {
		t.Fatal("media after close is available")
	}
}

func TestNewClosesPreviouslyOpenedConnectorOnFactoryError(t *testing.T) {
	mainConnector := newFakeConnector("main")
	brokenConnector := newFakeConnector("broken")

	_, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger:           fakeLoggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         fakeEventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: &fakeConnectorFactory{connector: mainConnector},
			Adapters: []kernel.ModuleDatabaseFactory{
				&fakeDatabaseFactory{
					code: core.ModuleCode,
					database: &fakeCoreDatabase{
						repository: &fakeSiteRepository{},
					},
				},
			},
		},
		AdditionalDatabases: []appkernel.DatabaseDefinition{
			{
				Connector: &fakeConnectorFactory{
					connector: brokenConnector,
					err:       errors.New("open failed"),
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "open failed") {
		t.Fatalf("New error = %v", err)
	}
	if mainConnector.closes.Load() != 1 || brokenConnector.closes.Load() != 1 {
		t.Fatalf(
			"close counts = main:%d broken:%d",
			mainConnector.closes.Load(),
			brokenConnector.closes.Load(),
		)
	}
}

func TestAppNewRequiresCachePingAndClosesFailedStore(t *testing.T) {
	cacheStore := &fakeCacheStore{
		code:    "required",
		pingErr: errors.New("cache unavailable"),
	}
	_, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           fakeLoggerFactory{},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus:         fakeEventBusFactory{},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{
					connector: newFakeConnector("main"),
				},
				Adapters: []kernel.ModuleDatabaseFactory{
					&fakeDatabaseFactory{
						code: core.ModuleCode,
						database: &fakeCoreDatabase{
							repository: &fakeSiteRepository{},
						},
					},
				},
			},
			Caches: []cache.Factory{
				fakeCacheFactory{store: cacheStore},
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "cache unavailable") {
		t.Fatalf("New error = %v", err)
	}
	if cacheStore.pings.Load() != 1 || cacheStore.closes.Load() != 1 {
		t.Fatalf(
			"cache lifecycle = pings:%d closes:%d",
			cacheStore.pings.Load(),
			cacheStore.closes.Load(),
		)
	}
}

func TestAppBootAllowsDifferentCoreRepositoryCachesAcrossProfiles(
	t *testing.T,
) {
	cacheStore := &fakeCacheStore{code: "shared"}
	application, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           fakeLoggerFactory{},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus:         fakeEventBusFactory{},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{
					connector: newFakeConnector("main"),
				},
				Adapters: []kernel.ModuleDatabaseFactory{
					&fakeDatabaseFactory{
						code: core.ModuleCode,
						database: &fakeCoreDatabase{
							repository: &fakeSiteRepository{},
						},
					},
				},
			},
			Caches: []cache.Factory{
				fakeCacheFactory{store: cacheStore},
			},
			Profiles: []kernel.Profile{
				{
					Code: "first",
					Modules: []kernel.ProfileModule{
						{
							Module: core.Module{},
							Config: core.Config{
								RepositoryCacheTTL: time.Minute,
							},
							Caches: []cache.Binding{{
								Alias:     core.DurableCacheAlias,
								Code:      cacheStore.code,
								Namespace: "core/first",
							}},
						},
						{Module: admin.Module{}},
					},
				},
				{
					Code: "second",
					Modules: []kernel.ProfileModule{
						{
							Module: core.Module{},
							Config: core.Config{
								RepositoryCacheTTL: time.Minute,
							},
							Caches: []cache.Binding{{
								Alias:     core.DurableCacheAlias,
								Code:      cacheStore.code,
								Namespace: "core/second",
							}},
						},
						{Module: admin.Module{}},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()

	if err := application.Boot(context.Background()); err != nil {
		t.Fatalf("Boot error = %v", err)
	}
	for _, profileCode := range []kernel.ProfileCode{"first", "second"} {
		blueprint, exists := application.ProfileBlueprint(profileCode)
		if !exists {
			t.Fatalf("profile blueprint %q is unavailable", profileCode)
		}
		binding := blueprint.Profile().Modules[0].Caches[0]
		if binding.Namespace != "core/"+string(profileCode) {
			t.Fatalf("core cache %q = %#v", profileCode, binding)
		}
	}
}

func TestAppCollectsSeedSourcesAcrossConnectionsAndClonesTags(t *testing.T) {
	mainConnector := newFakeConnector("main")
	logsConnector := newFakeConnector("logs")
	coreDatabase := &fakeCoreDatabase{
		repository: &fakeSiteRepository{},
		seedSources: []seeds.Source{
			seedSource("sites_dev", "dev", "core dev"),
		},
	}
	mainFeature := &fakeFeatureDatabase{
		name: "main",
		seedSources: []seeds.Source{
			{
				ID:     "feature_shared",
				Tags:   []seeds.Tag{"dev", "prod"},
				Schema: "feature",
				FS:     versionedSource("feature shared").FS,
				Path:   ".",
			},
		},
	}
	logsFeature := &fakeFeatureDatabase{
		name: "logs",
		seedSources: []seeds.Source{
			{
				ID:     "audit_prod",
				Tags:   []seeds.Tag{"prod"},
				Schema: "feature",
				FS:     versionedSource("audit prod").FS,
				Path:   ".",
			},
		},
	}

	application, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           fakeLoggerFactory{},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus:         fakeEventBusFactory{},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{connector: mainConnector},
				Adapters: []kernel.ModuleDatabaseFactory{
					&fakeDatabaseFactory{
						code:     core.ModuleCode,
						database: coreDatabase,
					},
					&fakeDatabaseFactory{
						code:     featureModuleCode,
						database: mainFeature,
					},
				},
			},
			AdditionalDatabases: []appkernel.DatabaseDefinition{
				{
					Connector: &fakeConnectorFactory{
						connector: logsConnector,
					},
					Adapters: []kernel.ModuleDatabaseFactory{
						&fakeDatabaseFactory{
							code:     featureModuleCode,
							database: logsFeature,
						},
					},
				},
			},
			Profiles: []kernel.Profile{
				{
					Code: "dev",
					Modules: []kernel.ProfileModule{
						{Module: core.Module{}},
						{Module: admin.Module{}},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()

	plans := application.SeedPlans()
	if len(plans) != 3 {
		t.Fatalf("seed plans = %#v", plans)
	}
	got := []string{
		plans[0].Connection + "/" +
			string(plans[0].Module) + "/" +
			plans[0].Source.ID,
		plans[1].Connection + "/" +
			string(plans[1].Module) + "/" +
			plans[1].Source.ID,
		plans[2].Connection + "/" +
			string(plans[2].Module) + "/" +
			plans[2].Source.ID,
	}
	want := []string{
		"main/core/sites_dev",
		"main/feature/feature_shared",
		"logs/feature/audit_prod",
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("plan order = %#v", got)
		}
	}

	plans[1].Source.Tags[0] = "changed"
	fresh := application.SeedPlans()
	if fresh[1].Source.Tags[0] != "dev" {
		t.Fatalf("seed plan tags were not cloned: %#v", fresh[1].Source.Tags)
	}
}

func TestAppRejectsSeedHistoryCollision(t *testing.T) {
	connector := newFakeConnector("main")
	coreSource := seedSource("shared", "dev", "core")
	coreSource.Schema = "shared"
	featureSource := seedSource("shared", "prod", "feature")
	featureSource.Schema = "shared"

	_, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           fakeLoggerFactory{},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus:         fakeEventBusFactory{},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: &fakeConnectorFactory{connector: connector},
				Adapters: []kernel.ModuleDatabaseFactory{
					&fakeDatabaseFactory{
						code: core.ModuleCode,
						database: &fakeCoreDatabase{
							repository:  &fakeSiteRepository{},
							seedSources: []seeds.Source{coreSource},
						},
					},
					&fakeDatabaseFactory{
						code: featureModuleCode,
						database: &fakeFeatureDatabase{
							seedSources: []seeds.Source{featureSource},
						},
					},
				},
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "share history") {
		t.Fatalf("history collision error = %v", err)
	}
}

func TestBootFailureIsRememberedAndNotRetried(t *testing.T) {
	connector := newFakeConnector("main")
	repository := &fakeSiteRepository{err: errors.New("list failed")}

	application, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger:           fakeLoggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         fakeEventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: &fakeConnectorFactory{connector: connector},
			Adapters: []kernel.ModuleDatabaseFactory{
				&fakeDatabaseFactory{
					code: core.ModuleCode,
					database: &fakeCoreDatabase{
						repository: repository,
					},
				},
			},
		},
		Profiles: []kernel.Profile{
			{
				Code: "dev",
				Modules: []kernel.ProfileModule{
					{Module: core.Module{}},
					{Module: admin.Module{}},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = application.Close() }()

	firstErr := application.Boot(context.Background())
	if firstErr == nil {
		t.Fatal("expected Boot error")
	}

	repository.set(nil, nil)
	secondErr := application.Boot(context.Background())
	if secondErr == nil || secondErr.Error() != firstErr.Error() {
		t.Fatalf("second Boot error = %v, want %v", secondErr, firstErr)
	}
	if repository.callCount() != 1 {
		t.Fatalf("failed Boot was retried: repository calls = %d", repository.callCount())
	}
	if _, exists := application.ProfileBlueprint("dev"); exists {
		t.Fatal("failed Boot published profile blueprint")
	}
}

var _ fs.FS = fstest.MapFS{}
