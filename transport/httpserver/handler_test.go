package httpserver_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/connectors/localstorage"
	"github.com/vernal96/go-cms-kernel"
	appkernel "github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/logging"
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
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	corewidgets "github.com/vernal96/go-cms-kernel/modules/core/widgets"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
	httpserver "github.com/vernal96/go-cms-kernel/transport/httpserver"
)

type connector struct{}

type loggerFactory struct {
	writer io.Writer
}

type loggerConnector struct {
	logger *slog.Logger
}

func (f loggerFactory) Open(context.Context) (logging.Connector, error) {
	writer := f.writer
	if writer == nil {
		writer = io.Discard
	}
	return &loggerConnector{
		logger: slog.New(slog.NewJSONHandler(writer, nil)),
	}, nil
}

func (c *loggerConnector) Logger() *slog.Logger {
	return c.logger
}

func (*loggerConnector) Ping(context.Context) error {
	return nil
}

func (*loggerConnector) Close() error {
	return nil
}

type eventBusFactory struct{}
type eventBusConnector struct{}

func (eventBusFactory) Open(
	context.Context,
) (eventbus.Connector, error) {
	return eventBusConnector{}, nil
}

func (eventBusConnector) Ping(context.Context) error {
	return nil
}

func (eventBusConnector) Publish(
	context.Context,
	eventbus.Message,
) error {
	return nil
}

func (eventBusConnector) Consume(
	ctx context.Context,
	_ eventbus.Subscription,
	_ eventbus.Handler,
) error {
	<-ctx.Done()
	return nil
}

func (eventBusConnector) Close() error {
	return nil
}

func (connector) Code() kernel.ConnectionCode { return "test" }
func (connector) Ping(context.Context) error  { return nil }
func (connector) Close() error                { return nil }

type connectorFactory struct{}

func (connectorFactory) Code() kernel.ConnectionCode { return "test" }
func (connectorFactory) Open(context.Context) (kernel.DBConnector, error) {
	return connector{}, nil
}

type repository struct {
	isPublic bool
}

type publicationSiteRepository struct {
	items       []site.Site
	updateCalls int
}

func (r *publicationSiteRepository) List(context.Context) ([]site.Site, error) {
	return append([]site.Site(nil), r.items...), nil
}

func (r *publicationSiteRepository) Update(
	_ context.Context,
	_ *security.UserID,
	item site.Site,
) (site.Site, error) {
	for index := range r.items {
		if r.items[index].ID != item.ID {
			continue
		}
		r.updateCalls++
		r.items[index] = item
		return item, nil
	}
	return site.Site{}, site.ErrNotFound
}

func (r *publicationSiteRepository) FindByID(
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

func (r *publicationSiteRepository) FindByDomain(
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

func (r *publicationSiteRepository) ListPage(
	context.Context,
	site.ListQuery,
) (site.Page, error) {
	return site.Page{
		Items: append([]site.Site(nil), r.items...),
		Total: len(r.items),
	}, nil
}

func (r *publicationSiteRepository) Create(
	_ context.Context,
	_ *security.UserID,
	item site.Site,
) (site.Site, error) {
	var maxID site.ID
	for _, current := range r.items {
		if current.ID > maxID {
			maxID = current.ID
		}
	}
	item.ID = maxID + 1
	r.items = append(r.items, item)
	return item, nil
}

func (r *publicationSiteRepository) Delete(
	_ context.Context,
	id site.ID,
) error {
	for index, item := range r.items {
		if item.ID != id {
			continue
		}
		r.items = append(r.items[:index], r.items[index+1:]...)
		return nil
	}
	return site.ErrNotFound
}

func (r *publicationSiteRepository) set(items ...site.Site) {
	r.items = append([]site.Site(nil), items...)
}

func (r repository) List(context.Context) ([]site.Site, error) {
	return []site.Site{
		{
			ID:          1,
			ProfileCode: "dev",
			Domain:      "example.com",
			Locale:      "ru-RU",
			IsPublic:    r.isPublic,
		},
	}, nil
}

func (repository) Update(
	context.Context,
	*security.UserID,
	site.Site,
) (site.Site, error) {
	return site.Site{}, nil
}

func (r repository) FindByID(_ context.Context, id site.ID) (site.Site, error) {
	items, _ := r.List(context.Background())
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return site.Site{}, site.ErrNotFound
}

func (r repository) FindByDomain(_ context.Context, domain string) (site.Site, error) {
	items, _ := r.List(context.Background())
	for _, item := range items {
		if item.Domain == domain {
			return item, nil
		}
	}
	return site.Site{}, site.ErrNotFound
}

func (r repository) ListPage(ctx context.Context, _ site.ListQuery) (site.Page, error) {
	items, err := r.List(ctx)
	return site.Page{Items: items, Total: len(items)}, err
}

func (repository) Create(_ context.Context, _ *security.UserID, item site.Site) (site.Site, error) {
	item.ID = 2
	return item, nil
}

func (repository) Delete(context.Context, site.ID) error { return nil }

type database struct {
	files     corefile.Repository
	sites     site.Repository
	resources resource.Repository
	access    coreaccess.Repository
}

func (database) ModuleCode() kernel.ModuleCode { return core.ModuleCode }
func (d database) Sites() site.Repository {
	if d.sites != nil {
		return d.sites
	}
	return repository{isPublic: true}
}
func (d database) Resources() resource.Repository {
	if d.resources != nil {
		return d.resources
	}
	return resourceRepository{}
}
func (d database) Files() corefile.Repository {
	if d.files != nil {
		return d.files
	}
	return fileRepository{}
}
func (database) Media() coremedia.Repository {
	return mediaRepository{}
}
func (database) Users() coreuser.Repository   { return userRepository{} }
func (database) Groups() coregroup.Repository { return groupRepository{} }
func (d database) Access() coreaccess.Repository {
	if d.access != nil {
		return d.access
	}
	return accessRepository{}
}

type mediaRepository struct {
	coremedia.Repository
}

type userRepository struct {
	coreuser.Repository
}

type groupRepository struct {
	coregroup.Repository
}

func (userRepository) ListPage(context.Context, coreuser.ListQuery) (coreuser.Page, error) {
	return coreuser.Page{}, nil
}

func (groupRepository) ListPage(context.Context, coregroup.ListQuery) (coregroup.Page, error) {
	return coregroup.Page{}, nil
}

type accessRepository struct{}

func (accessRepository) Subject(
	context.Context,
	security.UserID,
) (coreaccess.Subject, error) {
	return coreaccess.Subject{}, nil
}

func (accessRepository) GroupAllowed(
	context.Context,
	security.UserID,
	permission.Code,
) (bool, error) {
	return false, nil
}

func (accessRepository) GuestAllowed(
	_ context.Context,
	code permission.Code,
) (bool, error) {
	return code == permission.MustCode(
		"core",
		"site",
		permission.Read,
	) || code == permission.MustCode(
		"core",
		"resource",
		permission.Read,
	), nil
}

func (accessRepository) GuestPermissions(
	context.Context,
) ([]coreaccess.Grant, error) {
	return nil, nil
}

func (accessRepository) GrantGuest(
	context.Context,
	*security.UserID,
	permission.Code,
) (coreaccess.Grant, error) {
	return coreaccess.Grant{}, nil
}

func (accessRepository) RevokeGuest(
	context.Context,
	permission.Code,
) error {
	return nil
}

type deniedAccessRepository struct {
	accessRepository
}

func (deniedAccessRepository) GuestAllowed(
	context.Context,
	permission.Code,
) (bool, error) {
	return false, nil
}

type privilegedUserAccessRepository struct {
	accessRepository
}

func (privilegedUserAccessRepository) Subject(
	context.Context,
	security.UserID,
) (coreaccess.Subject, error) {
	return coreaccess.Subject{
		Exists:  true,
		Active:  true,
		IsSuper: true,
	}, nil
}

type knownUserAccessRepository struct {
	accessRepository
}

func (knownUserAccessRepository) Subject(
	context.Context,
	security.UserID,
) (coreaccess.Subject, error) {
	return coreaccess.Subject{
		Exists: true,
		Active: true,
	}, nil
}

type groupUserAccessRepository struct {
	accessRepository
	allowed bool
}

func (groupUserAccessRepository) Subject(
	context.Context,
	security.UserID,
) (coreaccess.Subject, error) {
	return coreaccess.Subject{
		Exists:    true,
		Active:    true,
		HasGroups: true,
	}, nil
}

func (r groupUserAccessRepository) GroupAllowed(
	context.Context,
	security.UserID,
	permission.Code,
) (bool, error) {
	return r.allowed, nil
}

type staticAccessTokens struct {
	verified  security.Actor
	verifyErr error
}

type siteManagementTestModule struct{ path string }
type siteManagementDuplicateModule struct{}
type siteManagementTestRuntime struct {
	moduleCode kernel.ModuleCode
	path       string
}

func (siteManagementTestModule) Code() kernel.ModuleCode { return "site_management_test" }
func (m siteManagementTestModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	path := m.path
	if path == "" {
		path = "feature"
	}
	return siteManagementTestRuntime{moduleCode: "site_management_test", path: path}, nil
}
func (siteManagementDuplicateModule) Code() kernel.ModuleCode { return "site_management_duplicate" }
func (siteManagementDuplicateModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return siteManagementTestRuntime{moduleCode: "site_management_duplicate", path: "feature"}, nil
}
func (r siteManagementTestRuntime) ModuleCode() kernel.ModuleCode { return r.moduleCode }
func (r siteManagementTestRuntime) SiteManagementHTTP() httptransport.SiteManagementContribution {
	router := chi.NewRouter()
	router.Get("/ping", func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusNoContent) })
	return httptransport.SiteManagementContribution{Path: r.path, Handler: router}
}

func TestOptionalSiteManagementHTTPIsRuntimeContributed(t *testing.T) {
	sites := &publicationSiteRepository{items: []site.Site{
		{ID: 1, ProfileCode: "with_feature", Domain: "feature.example.test", Locale: "ru-RU", IsPublic: true},
		{ID: 2, ProfileCode: "plain", Domain: "plain.example.test", Locale: "ru-RU", IsPublic: true},
	}}
	runtimeApp, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger: loggerFactory{}, PasswordHasher: argon2id.Factory{}, SiteAccessPolicy: admin.AllowAllSitesPolicy{}, EventBus: eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{Connector: connectorFactory{}, Adapters: []kernel.ModuleDatabaseFactory{databaseFactory{sites: sites, access: privilegedUserAccessRepository{}}}},
		Profiles: []kernel.Profile{
			{Code: "with_feature", Modules: []kernel.ProfileModule{{Module: core.Module{}}, {Module: admin.Module{}}, {Module: siteManagementTestModule{}}}},
			{Code: "plain", Modules: []kernel.ProfileModule{{Module: core.Module{}}, {Module: admin.Module{}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtimeApp.Close() }()
	if err := runtimeApp.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler, err := newTestHandler(runtimeApp, httpserver.WithAccessTokens(staticAccessTokens{verified: security.User(42)}))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path   string
		status int
	}{{"/api/sites/1/feature/ping", http.StatusNoContent}, {"/api/sites/2/feature/ping", http.StatusNotFound}} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set("Authorization", "Bearer signed")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s = %d, %s", test.path, response.Code, response.Body.String())
		}
	}
	for _, test := range []struct {
		path   string
		status int
	}{{"/api/sites/1/resources", http.StatusOK}, {"/api/sites/1/menu", http.StatusOK}, {"/api/sites/1/resources/999", http.StatusNotFound}, {"/api/sites/1/library-items/999", http.StatusInternalServerError}} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set("Authorization", "Bearer signed")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("Core route %s = %d, %s", test.path, response.Code, response.Body.String())
		}
		if response.Body.String() == "404 page not found\n" {
			t.Fatalf("Core route %s was shadowed by optional dispatch", test.path)
		}
	}
}

func TestOptionalSiteManagementHTTPRejectsCorePathCollisionAtCompileTime(t *testing.T) {
	sites := &publicationSiteRepository{items: []site.Site{{ID: 1, ProfileCode: "collision", Domain: "collision.example.test", Locale: "ru-RU", IsPublic: true}}}
	runtimeApp, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger: loggerFactory{}, PasswordHasher: argon2id.Factory{}, SiteAccessPolicy: admin.AllowAllSitesPolicy{}, EventBus: eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{Connector: connectorFactory{}, Adapters: []kernel.ModuleDatabaseFactory{databaseFactory{sites: sites, access: privilegedUserAccessRepository{}}}},
		Profiles:     []kernel.Profile{{Code: "collision", Modules: []kernel.ProfileModule{{Module: core.Module{}}, {Module: admin.Module{}}, {Module: siteManagementTestModule{path: "resources"}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtimeApp.Close() }()
	if err := runtimeApp.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestHandler(runtimeApp); err == nil || !strings.Contains(err.Error(), "owned by Core") {
		t.Fatalf("Core route collision = %v", err)
	}
}

func TestOptionalSiteManagementHTTPRejectsDuplicatePathAtCompileTime(t *testing.T) {
	sites := &publicationSiteRepository{items: []site.Site{{ID: 1, ProfileCode: "duplicate", Domain: "duplicate.example.test", Locale: "ru-RU", IsPublic: true}}}
	runtimeApp, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger: loggerFactory{}, PasswordHasher: argon2id.Factory{}, SiteAccessPolicy: admin.AllowAllSitesPolicy{}, EventBus: eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{Connector: connectorFactory{}, Adapters: []kernel.ModuleDatabaseFactory{databaseFactory{sites: sites, access: privilegedUserAccessRepository{}}}},
		Profiles:     []kernel.Profile{{Code: "duplicate", Modules: []kernel.ProfileModule{{Module: core.Module{}}, {Module: admin.Module{}}, {Module: siteManagementTestModule{}}, {Module: siteManagementDuplicateModule{}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtimeApp.Close() }()
	if err := runtimeApp.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestHandler(runtimeApp); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate optional route = %v", err)
	}
}

func (s staticAccessTokens) IssueAccessToken(
	context.Context,
	security.Actor,
) (security.AccessToken, error) {
	return security.AccessToken{
		Value:     "signed",
		ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}

func (s staticAccessTokens) VerifyAccessToken(
	context.Context,
	string,
) (security.Actor, error) {
	return s.verified, s.verifyErr
}

func newTestHandler(
	application *appkernel.App,
	options ...httpserver.Option,
) (*httpserver.Handler, error) {
	return httpserver.NewHandler(
		application,
		append(
			[]httpserver.Option{
				httpserver.WithAccessTokens(staticAccessTokens{}),
			},
			options...,
		)...,
	)
}

type resourceRepository struct {
	byPath map[string]resource.Resource
	byID   map[resource.ID]resource.Resource
}

func (resourceRepository) Create(
	context.Context,
	*security.UserID,
	resource.Resource,
	resource.ValidateImageMedia,
) (resource.Resource, error) {
	return resource.Resource{}, resource.ErrNotFound
}

func (r resourceRepository) ByID(
	_ context.Context,
	id resource.ID,
) (resource.Resource, error) {
	item, exists := r.byID[id]
	if exists {
		return resource.Clone(item), nil
	}
	return resource.Resource{}, resource.ErrNotFound
}

func (r resourceRepository) ByPath(
	_ context.Context,
	siteID site.ID,
	path string,
) (resource.Resource, error) {
	item, exists := r.byPath[path]
	if exists && item.SiteID == siteID {
		return resource.Clone(item), nil
	}
	return resource.Resource{}, resource.ErrNotFound
}

func (resourceRepository) ListBySite(
	context.Context,
	site.ID,
) ([]resource.Resource, error) {
	return nil, nil
}

func (r resourceRepository) ExistsInSite(
	_ context.Context,
	siteID site.ID,
	id resource.ID,
) (bool, error) {
	item, exists := r.byID[id]
	return exists && item.SiteID == siteID, nil
}

func (resourceRepository) ListChildren(
	context.Context,
	site.ID,
	*resource.ID,
) ([]resource.Child, error) {
	return nil, nil
}

func (resourceRepository) Update(
	context.Context,
	*security.UserID,
	resource.Resource,
	resource.Resource,
	resource.ValidateImageMedia,
) (resource.Resource, error) {
	return resource.Resource{}, resource.ErrNotFound
}

func (resourceRepository) Delete(
	context.Context,
	resource.ID,
) error {
	return resource.ErrNotFound
}

type databaseFactory struct {
	files     corefile.Repository
	sites     site.Repository
	resources resource.Repository
	access    coreaccess.Repository
}

func (databaseFactory) ModuleCode() kernel.ModuleCode { return core.ModuleCode }
func (f databaseFactory) Build(kernel.DBConnector) (kernel.ModuleDatabase, error) {
	return database{
		files:     f.files,
		sites:     f.sites,
		resources: f.resources,
		access:    f.access,
	}, nil
}

type fileRepository struct {
	item corefile.File
}

func (fileRepository) NameAvailable(
	context.Context,
	filesystem.Code,
	*corefile.FolderID,
	string,
) error {
	return nil
}
func (fileRepository) CreateFolder(
	context.Context,
	corefile.Folder,
) (corefile.Folder, error) {
	return corefile.Folder{}, corefile.ErrFolderNotFound
}
func (fileRepository) FolderByID(
	context.Context,
	corefile.FolderID,
) (corefile.Folder, error) {
	return corefile.Folder{}, corefile.ErrFolderNotFound
}
func (fileRepository) ListFolders(
	context.Context,
	filesystem.Code,
	*corefile.FolderID,
) ([]corefile.Folder, error) {
	return nil, nil
}
func (fileRepository) CreateFile(
	context.Context,
	corefile.File,
) (corefile.File, error) {
	return corefile.File{}, corefile.ErrNotFound
}
func (r fileRepository) FileByID(
	_ context.Context,
	id corefile.ID,
) (corefile.File, error) {
	if r.item.ID != id {
		return corefile.File{}, corefile.ErrNotFound
	}
	return r.item, nil
}
func (fileRepository) ListFiles(
	context.Context,
	filesystem.Code,
	*corefile.FolderID,
) ([]corefile.File, error) {
	return nil, nil
}
func (fileRepository) MoveFile(
	context.Context,
	*security.UserID,
	corefile.ID,
	*corefile.FolderID,
) (corefile.File, error) {
	return corefile.File{}, corefile.ErrNotFound
}
func (fileRepository) MoveFolder(
	context.Context,
	*security.UserID,
	corefile.FolderID,
	*corefile.FolderID,
) (corefile.Folder, error) {
	return corefile.Folder{}, corefile.ErrFolderNotFound
}
func (fileRepository) DeleteFile(
	context.Context,
	corefile.ID,
	corefile.DeletePhysical,
) error {
	return corefile.ErrNotFound
}
func (fileRepository) DeleteFolder(
	context.Context,
	corefile.FolderID,
	corefile.DeletePhysical,
) error {
	return corefile.ErrFolderNotFound
}

func TestHandlerLooksUpCompiledRuntimeByRequestHost(t *testing.T) {
	runtimeApp, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger:           loggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: connectorFactory{},
			Adapters:  []kernel.ModuleDatabaseFactory{databaseFactory{}},
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
	defer func() { _ = runtimeApp.Close() }()
	if err := runtimeApp.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := httpserver.NewHandler(runtimeApp); err == nil {
		t.Fatal("handler accepted a missing access token service")
	}
	handler, err := newTestHandler(runtimeApp)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/_cms/runtime", nil)
	request.Host = "EXAMPLE.COM.:8080"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/_cms/runtime", nil)
	request.Host = "missing.example.com"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown host status = %d", response.Code)
	}
}

func TestHandlerHidesRuntimeWithoutGuestPermissionOrPublicFlag(
	t *testing.T,
) {
	tests := []struct {
		name    string
		factory databaseFactory
	}{
		{
			name: "public site without guest grant",
			factory: databaseFactory{
				sites:  repository{isPublic: true},
				access: deniedAccessRepository{},
			},
		},
		{
			name: "private site with guest grant",
			factory: databaseFactory{
				sites: repository{isPublic: false},
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			runtimeApp, err := appkernel.New(
				context.Background(),
				appkernel.Definition{
					Logger:           loggerFactory{},
					PasswordHasher:   argon2id.Factory{},
					SiteAccessPolicy: admin.AllowAllSitesPolicy{},
					EventBus:         eventBusFactory{},
					MainDatabase: appkernel.DatabaseDefinition{
						Connector: connectorFactory{},
						Adapters: []kernel.ModuleDatabaseFactory{
							test.factory,
						},
					},
					Profiles: []kernel.Profile{{
						Code: "dev",
						Modules: []kernel.ProfileModule{
							{Module: core.Module{}},
							{Module: admin.Module{}},
						},
					}},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = runtimeApp.Close() }()
			if err := runtimeApp.Boot(context.Background()); err != nil {
				t.Fatal(err)
			}
			handler, err := newTestHandler(runtimeApp)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(
				http.MethodGet,
				"/_cms/runtime",
				nil,
			)
			request.Host = "example.com"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf(
					"status = %d, body = %q",
					response.Code,
					response.Body.String(),
				)
			}
		})
	}
}

func TestHandlerDeliversPublicAndSignedPrivateLocalFiles(t *testing.T) {
	tests := []struct {
		name       string
		code       filesystem.Code
		visibility filesystem.Visibility
		signingKey string
	}{
		{
			name:       "public",
			code:       "public",
			visibility: filesystem.VisibilityPublic,
		},
		{
			name:       "private",
			code:       "private",
			visibility: filesystem.VisibilityPrivate,
			signingKey: strings.Repeat("private-key", 4),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			key := "objects/2026/test"
			target := filepath.Join(root, filepath.FromSlash(key))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			content := "delivered content"
			if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}

			repository := fileRepository{item: corefile.File{
				ID:       1,
				Storage:  test.code,
				Name:     "hello.txt",
				MIMEType: "text/plain; charset=utf-8",
				Size:     int64(len(content)),
				Path:     key,
			}}
			runtimeApp, err := appkernel.New(
				context.Background(),
				appkernel.Definition{
					Logger:           loggerFactory{},
					PasswordHasher:   argon2id.Factory{},
					SiteAccessPolicy: admin.AllowAllSitesPolicy{},
					EventBus:         eventBusFactory{},
					MainDatabase: appkernel.DatabaseDefinition{
						Connector: connectorFactory{},
						Adapters: []kernel.ModuleDatabaseFactory{
							databaseFactory{files: repository},
						},
					},
					Filesystems: []filesystem.Factory{
						localstorage.Factory{Config: localstorage.Config{
							Code:       test.code,
							Visibility: test.visibility,
							Root:       root,
							BaseURL:    "https://cms.example.test",
							SigningKey: test.signingKey,
						}},
					},
					Profiles: []kernel.Profile{{
						Code: "dev",
						Modules: []kernel.ProfileModule{
							{Module: core.Module{}},
							{Module: admin.Module{}},
						},
					}},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = runtimeApp.Close() }()
			if err := runtimeApp.Boot(context.Background()); err != nil {
				t.Fatal(err)
			}

			rawURL := "https://cms.example.test/_cms/files/1"
			if test.visibility == filesystem.VisibilityPrivate {
				rawURL, err = runtimeApp.Files().TemporaryURL(
					context.Background(),
					security.System(),
					1,
					time.Now().Add(time.Hour),
				)
				if err != nil {
					t.Fatal(err)
				}
			}
			handler, err := newTestHandler(runtimeApp)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, rawURL, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK ||
				response.Body.String() != content ||
				response.Header().Get("Content-Type") !=
					"text/plain; charset=utf-8" {
				t.Fatalf(
					"response = %d, %q, %#v",
					response.Code,
					response.Body.String(),
					response.Header(),
				)
			}

			if test.visibility == filesystem.VisibilityPrivate {
				request = httptest.NewRequest(
					http.MethodGet,
					rawURL+"tampered",
					nil,
				)
				response = httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusNotFound {
					t.Fatalf("tampered URL status = %d", response.Code)
				}
			}
		})
	}
}

type transportResourceType struct {
	code resourcetype.Code
}

func (t transportResourceType) Code() resourcetype.Code {
	return t.code
}

func (transportResourceType) PathMode() resourcetype.PathMode {
	return resourcetype.PathRoute
}

func (transportResourceType) Metadata() resourcetype.Metadata {
	return resourcetype.Metadata{Label: "Transport", Capabilities: resourcetype.Capabilities{MutableType: true}, SettingsDefaults: map[string]any{}}
}

func (transportResourceType) Normalize(
	payload resourcetype.Payload,
) (resourcetype.Payload, error) {
	return payload, nil
}

type transportModule struct {
	code         kernel.ModuleCode
	resourceType resourcetype.Type
	order        *[]string
	widgets      []widget.Widget
}

type publicationModule struct {
	builds *int
}

func (publicationModule) Code() kernel.ModuleCode { return "publication" }

func (m publicationModule) Build(
	_ context.Context,
	ctx kernel.ModuleContext,
) (kernel.ModuleRuntime, error) {
	if m.builds != nil {
		*m.builds++
	}
	settings := ctx.Scope().Settings()
	broken, _ := settings["broken"].(bool)
	collision, _ := settings["collision"].(bool)
	return publicationRuntime{
		domain:    ctx.Scope().Domain(),
		broken:    broken,
		collision: collision,
	}, nil
}

type publicationRuntime struct {
	domain    string
	broken    bool
	collision bool
}

func (publicationRuntime) ModuleCode() kernel.ModuleCode {
	return "publication"
}

func (r publicationRuntime) HTTP() httptransport.Builder {
	return httptransport.BuilderFunc(func(
		context.Context,
	) (httptransport.Contribution, error) {
		if r.broken {
			return httptransport.Contribution{}, errors.New(
				"publication HTTP compile failed",
			)
		}
		return httptransport.Contribution{
			Routes: func(registrar httptransport.Registrar) error {
				if r.collision {
					for _, pattern := range []string{"/items/{id}", "/items/{slug}"} {
						if err := registrar.Route(httptransport.Route{Method: "GET", Pattern: pattern, Handler: http.NotFoundHandler()}); err != nil {
							return err
						}
					}
				}
				return registrar.Route(httptransport.Route{
					Method:  http.MethodGet,
					Pattern: "/version",
					Handler: http.HandlerFunc(func(
						response http.ResponseWriter,
						_ *http.Request,
					) {
						_, _ = response.Write([]byte(r.domain))
					}),
				})
			},
		}, nil
	})
}

func (m transportModule) Code() kernel.ModuleCode {
	return m.code
}

func (m transportModule) Registry() kernel.ModuleRegistry {
	return kernel.ModuleRegistry{
		ResourceTypes: []resourcetype.Type{m.resourceType},
	}
}

func (m transportModule) Build(
	context.Context,
	kernel.ModuleContext,
) (kernel.ModuleRuntime, error) {
	return &transportRuntime{
		code: m.code,
		resourceType: httptransport.ResourceHandlerCode(
			m.resourceType.Code(),
		),
		order:   m.order,
		widgets: append([]widget.Widget(nil), m.widgets...),
	}, nil
}

type transportRuntime struct {
	code         kernel.ModuleCode
	resourceType httptransport.ResourceHandlerCode
	order        *[]string
	widgets      []widget.Widget
}

func (r *transportRuntime) ModuleCode() kernel.ModuleCode {
	return r.code
}

func (r *transportRuntime) Widgets() []widget.Widget {
	return append([]widget.Widget(nil), r.widgets...)
}

type handlerWidget struct {
	definition widget.Definition
	new        func(map[string]any) (widget.Instance, error)
}

func (w handlerWidget) Definition() widget.Definition {
	return widget.CloneDefinition(w.definition)
}

func (w handlerWidget) New(
	values map[string]any,
) (widget.Instance, error) {
	return w.new(values)
}

type handlerWidgetInstance struct {
	render func(
		context.Context,
		widget.RenderInput,
	) (map[string]any, error)
}

func (i handlerWidgetInstance) Render(
	ctx context.Context,
	input widget.RenderInput,
) (map[string]any, error) {
	return i.render(ctx, input)
}

func (r *transportRuntime) HTTP() httptransport.Builder {
	return httptransport.BuilderFunc(func(
		context.Context,
	) (httptransport.Contribution, error) {
		return httptransport.Contribution{
			Middleware: []httptransport.MiddlewareDefinition{
				{
					Code:       "transport.profile",
					Scope:      httptransport.MiddlewareProfile,
					Middleware: recordHTTPOrder(r.order, "profile"),
				},
				{
					Code:       "transport.module",
					Scope:      httptransport.MiddlewareModule,
					Middleware: recordHTTPOrder(r.order, "module"),
				},
				{
					Code:       "transport.route",
					Scope:      httptransport.MiddlewareLocal,
					Middleware: recordHTTPOrder(r.order, "route"),
				},
				{
					Code:       "transport.resource",
					Scope:      httptransport.MiddlewareLocal,
					Middleware: recordHTTPOrder(r.order, "resource"),
				},
				{
					Code:       "transport.authenticated",
					Scope:      httptransport.MiddlewareLocal,
					Middleware: httptransport.RequireAuthenticated,
				},
			},
			Routes: func(registrar httptransport.Registrar) error {
				if err := registrar.Route(httptransport.Route{
					Name:    "transport.custom",
					Method:  http.MethodGet,
					Pattern: "/custom",
					Handler: http.HandlerFunc(func(
						response http.ResponseWriter,
						_ *http.Request,
					) {
						*r.order = append(*r.order, "handler")
						response.WriteHeader(http.StatusNoContent)
					}),
					Middleware: []httptransport.MiddlewareCode{
						"transport.route",
					},
				}); err != nil {
					return err
				}
				return registrar.Route(httptransport.Route{
					Name:    "transport.protected",
					Method:  http.MethodGet,
					Pattern: "/protected",
					Handler: http.HandlerFunc(func(
						response http.ResponseWriter,
						_ *http.Request,
					) {
						response.WriteHeader(http.StatusNoContent)
					}),
					Middleware: []httptransport.MiddlewareCode{
						"transport.authenticated",
					},
				})
			},
			ResourceHandlers: []httptransport.ResourceHandler{{
				Type: r.resourceType,
				Handler: http.HandlerFunc(func(
					response http.ResponseWriter,
					_ *http.Request,
				) {
					*r.order = append(*r.order, "handler")
					response.WriteHeader(http.StatusNoContent)
				}),
				Middleware: []httptransport.MiddlewareCode{
					"transport.resource",
				},
			}},
		}, nil
	})
}

func recordHTTPOrder(
	order *[]string,
	marker string,
) httptransport.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(
			response http.ResponseWriter,
			request *http.Request,
		) {
			*order = append(*order, marker)
			next.ServeHTTP(response, request)
		})
	}
}

func stringPointer(value string) *string {
	return &value
}

func newTransportTestApp(
	t *testing.T,
	module transportModule,
	resources resource.Repository,
	extraTemplates ...template.Definition,
) *appkernel.App {
	t.Helper()
	runtimeApp, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           loggerFactory{},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus:         eventBusFactory{},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: connectorFactory{},
				Adapters: []kernel.ModuleDatabaseFactory{
					databaseFactory{resources: resources},
				},
			},
			Profiles: []kernel.Profile{{
				Code: "dev",
				Templates: append([]template.Definition{
					{Code: "widgets", Label: "Widgets", Layout: template.Layout{Body: []template.Item{template.ResourceWidgets{}}}},
					{Code: "content", Label: "Content", Layout: template.Layout{Body: []template.Item{template.Widget{Widget: corewidgets.Content}}}},
					{Code: "empty", Label: "Empty"},
				}, extraTemplates...),
				Modules: []kernel.ProfileModule{
					{Module: core.Module{}},
					{Module: admin.Module{}},
					{Module: module},
				},
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtimeApp.Close() })
	if err := runtimeApp.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	return runtimeApp
}

func TestSiteHTTPPublicationIsAtomicAcrossUpdateCreateAndReload(t *testing.T) {
	ctx := context.Background()
	moduleBuilds := 0
	repository := &publicationSiteRepository{items: []site.Site{{
		ID:          1,
		ProfileCode: "dev",
		Domain:      "first.test",
		Locale:      "en-US",
		Settings:    map[string]any{"broken": false},
		IsPublic:    true,
	}}}
	application, err := appkernel.New(ctx, appkernel.Definition{
		Logger:           loggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: connectorFactory{},
			Adapters: []kernel.ModuleDatabaseFactory{
				databaseFactory{sites: repository},
			},
		},
		Profiles: []kernel.Profile{{
			Code: "dev",
			Modules: []kernel.ProfileModule{
				{Module: core.Module{}},
				{Module: admin.Module{}},
				{Module: publicationModule{builds: &moduleBuilds}},
			},
			Params: []field.Definition{{
				Key: "broken", Type: field.TypeCheckbox, Label: "Broken",
			}, {Key: "collision", Type: field.TypeCheckbox, Label: "Collision"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	if err := application.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	handler, err := newTestHandler(application)
	if err != nil {
		t.Fatal(err)
	}
	request := func(host string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/version", nil)
		req.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	assertResponse := func(host, body string) {
		t.Helper()
		response := request(host)
		if response.Code != http.StatusOK || response.Body.String() != body {
			t.Fatalf(
				"%s response = %d %q, want 200 %q",
				host,
				response.Code,
				response.Body.String(),
				body,
			)
		}
	}

	buildsBeforeRequests := moduleBuilds
	assertResponse("first.test", "first.test")
	assertResponse("first.test", "first.test")
	if moduleBuilds != buildsBeforeRequests {
		t.Fatalf("HTTP requests rebuilt module runtime: %d -> %d", buildsBeforeRequests, moduleBuilds)
	}
	initialRuntime, _ := application.Sites().RuntimeByID(1)
	_, err = application.Sites().Update(ctx, security.System(), site.UpdateInput{
		ID:          1,
		ProfileCode: "dev",
		Domain:      "broken.test",
		Locale:      "en-US",
		Settings:    map[string]any{"broken": true},
		IsPublic:    true,
	})
	if err == nil || !strings.Contains(err.Error(), "publication HTTP compile failed") {
		t.Fatalf("failed update error = %v", err)
	}
	if repository.updateCalls != 0 {
		t.Fatalf("failed preparation persisted update %d times", repository.updateCalls)
	}
	preserved, _ := application.Sites().RuntimeByID(1)
	if preserved != initialRuntime {
		t.Fatal("failed HTTP preparation replaced the site runtime")
	}
	assertResponse("first.test", "first.test")

	_, err = application.Sites().Update(ctx, security.System(), site.UpdateInput{
		ID: 1, ProfileCode: "dev", Domain: "collision.test", Locale: "en-US", IsPublic: true,
		Settings: map[string]any{"broken": false, "collision": true},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("collision update=%v", err)
	}
	preserved, _ = application.Sites().RuntimeByID(1)
	if preserved != initialRuntime || repository.updateCalls != 0 {
		t.Fatal("collision published or persisted candidate")
	}
	assertResponse("first.test", "first.test")
	updated, err := application.Sites().Update(ctx, security.System(), site.UpdateInput{
		ID:          1,
		ProfileCode: "dev",
		Domain:      "updated.test",
		Locale:      "en-US",
		Settings:    map[string]any{"broken": false},
		IsPublic:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated == initialRuntime {
		t.Fatal("successful update retained the old runtime")
	}
	assertResponse("updated.test", "updated.test")
	if response := request("first.test"); response.Code != http.StatusNotFound {
		t.Fatalf("old domain response = %d", response.Code)
	}

	created, err := application.Sites().Create(ctx, security.System(), site.CreateInput{
		ProfileCode: "dev",
		Domain:      "created.test",
		Locale:      "en-US",
		Settings:    map[string]any{"broken": false},
		IsPublic:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertResponse("created.test", "created.test")
	if _, err := application.Sites().Create(ctx, security.System(), site.CreateInput{
		ProfileCode: "dev",
		Domain:      "broken-create.test",
		Locale:      "en-US",
		Settings:    map[string]any{"broken": true},
		IsPublic:    true,
	}); err == nil {
		t.Fatal("create ignored HTTP preparation failure")
	}
	if _, exists := application.Sites().RuntimeByID(3); exists {
		t.Fatal("failed create published a site runtime")
	}
	if _, err := repository.FindByID(ctx, 3); !errors.Is(err, site.ErrNotFound) {
		t.Fatalf("failed create left a persisted site: %v", err)
	}
	assertResponse("created.test", "created.test")

	repository.set(
		site.Site{
			ID: 1, ProfileCode: "dev", Domain: "reloaded.test", Locale: "en-US",
			Settings: map[string]any{"broken": false}, IsPublic: true,
		},
		created.Site(),
	)
	if err := application.ReloadSites(ctx); err != nil {
		t.Fatal(err)
	}
	assertResponse("reloaded.test", "reloaded.test")
	reloaded, _ := application.Sites().RuntimeByID(1)
	repository.set(
		site.Site{
			ID: 1, ProfileCode: "dev", Domain: "broken-reload.test", Locale: "en-US",
			Settings: map[string]any{"broken": true}, IsPublic: true,
		},
		created.Site(),
	)
	if err := application.ReloadSites(ctx); err == nil {
		t.Fatal("reload ignored HTTP preparation failure")
	}
	afterFailure, _ := application.Sites().RuntimeByID(1)
	if afterFailure != reloaded {
		t.Fatal("failed reload replaced the working runtime")
	}
	assertResponse("reloaded.test", "reloaded.test")
}

func TestHandlerMiddlewareOrderForRoutesAndResources(t *testing.T) {
	order := make([]string, 0, 5)
	resourceType := transportResourceType{code: "transport_test"}
	module := transportModule{
		code:         "transport",
		resourceType: resourceType,
		order:        &order,
	}
	runtimeApp := newTransportTestApp(
		t,
		module,
		resourceRepository{
			byPath: map[string]resource.Resource{
				"/article": {
					ID:       1,
					SiteID:   1,
					Type:     resourceType.Code(),
					Path:     stringPointer("/article"),
					IsPublic: true,
				},
			},
		},
	)
	handler, err := newTestHandler(
		runtimeApp,
		httpserver.WithPlatformMiddleware(
			recordHTTPOrder(&order, "platform"),
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/custom", nil)
	request.Host = "example.com"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent ||
		strings.Join(order, ",") !=
			"platform,profile,module,route,handler" {
		t.Fatalf("custom response = %d, order = %v", response.Code, order)
	}

	order = order[:0]
	request = httptest.NewRequest(http.MethodGet, "/article", nil)
	request.Host = "example.com"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent ||
		strings.Join(order, ",") !=
			"platform,profile,module,resource,handler" {
		t.Fatalf("resource response = %d, order = %v", response.Code, order)
	}
}

func TestPageResourceRendersWidgetEnvelopeAndIsolatesErrors(
	t *testing.T,
) {
	widgetsTemplate := template.Code("widgets")
	emptyTemplate := template.Code("empty")
	contentTemplate := template.Code("content")
	presentation := widget.DefaultPresentation()
	required := true
	success := handlerWidget{
		definition: widget.Definition{
			Reference:   widget.NewRef("success"),
			Label:       "Success",
			Description: "Successful widget",
		},
		new: func(map[string]any) (widget.Instance, error) {
			return handlerWidgetInstance{
				render: func(
					context.Context,
					widget.RenderInput,
				) (map[string]any, error) {
					return map[string]any{"value": "ok"}, nil
				},
			}, nil
		},
	}
	invalidParams := handlerWidget{
		definition: widget.Definition{
			Reference:   widget.NewRef("params"),
			Label:       "Params",
			Description: "Widget with required params",
			Fields: []field.Definition{{
				Key:      "title",
				Type:     field.TypeString,
				Label:    "Title",
				Required: &required,
			}},
		},
		new: func(map[string]any) (widget.Instance, error) {
			return handlerWidgetInstance{}, nil
		},
	}
	instanceFailed := handlerWidget{
		definition: widget.Definition{
			Reference:   widget.NewRef("instance"),
			Label:       "Instance",
			Description: "Widget with a failed constructor",
		},
		new: func(map[string]any) (widget.Instance, error) {
			return nil, errors.New("private constructor failure")
		},
	}
	renderFailed := handlerWidget{
		definition: widget.Definition{
			Reference:   widget.NewRef("render"),
			Label:       "Render",
			Description: "Widget with a failed renderer",
		},
		new: func(map[string]any) (widget.Instance, error) {
			return handlerWidgetInstance{
				render: func(
					context.Context,
					widget.RenderInput,
				) (map[string]any, error) {
					return nil, errors.New("private render failure")
				},
			}, nil
		},
	}
	invalidResult := handlerWidget{
		definition: widget.Definition{
			Reference:   widget.NewRef("result"),
			Label:       "Result",
			Description: "Widget with invalid JSON data",
		},
		new: func(map[string]any) (widget.Instance, error) {
			return handlerWidgetInstance{
				render: func(
					context.Context,
					widget.RenderInput,
				) (map[string]any, error) {
					return map[string]any{"invalid": make(chan int)}, nil
				},
			}, nil
		},
	}

	order := make([]string, 0)
	module := transportModule{
		code:         "widgets",
		resourceType: transportResourceType{code: "widget_test"},
		order:        &order,
		widgets: []widget.Widget{
			success,
			invalidParams,
			instanceFailed,
			renderFailed,
			invalidResult,
		},
	}
	runtimeApp := newTransportTestApp(
		t,
		module,
		resourceRepository{
			byPath: map[string]resource.Resource{
				"/page": {
					ID:       7,
					SiteID:   1,
					Type:     resourcetype.Page,
					Template: &widgetsTemplate,
					Title:    "Page",
					Path:     stringPointer("/page"),
					Content:  "Content",
					IsPublic: true,
					Widgets: []widget.Binding{
						{ID: 1, Code: "widgets_success", Area: widget.AreaBody, Position: 0, Presentation: presentation},
						{ID: 2, Code: "missing_widget", Area: widget.AreaBody, Position: 1, Presentation: presentation},
						{ID: 3, Code: "widgets_params", Area: widget.AreaBody, Position: 2, Presentation: presentation},
						{ID: 4, Code: "widgets_instance", Area: widget.AreaBody, Position: 3, Presentation: presentation},
						{ID: 5, Code: "widgets_render", Area: widget.AreaBody, Position: 4, Presentation: presentation},
						{ID: 6, Code: "widgets_result", Area: widget.AreaBody, Position: 5, Presentation: presentation},
						{ID: 7, Code: "widgets_success", Area: widget.AreaBody, Position: 6, Presentation: presentation},
						{ID: 8, Code: "widgets_success", Area: widget.AreaBody, Position: 7, Presentation: widget.Presentation{Columns: 12, Enabled: false}},
					},
				},
				"/empty": {
					ID:       8,
					SiteID:   1,
					Type:     resourcetype.Page,
					Template: &emptyTemplate,
					Title:    "Empty",
					Path:     stringPointer("/empty"),
					IsPublic: true,
				},
				"/core-content": {
					ID:       9,
					SiteID:   1,
					Type:     resourcetype.Page,
					Template: &contentTemplate,
					Title:    "Core content",
					Path:     stringPointer("/core-content"),
					Content:  "Content from resource",
					IsPublic: true,
				},
			},
		},
	)
	handler, err := newTestHandler(runtimeApp)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/page", nil)
	request.Host = "example.com"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf(
			"status = %d, body = %q",
			response.Code,
			response.Body.String(),
		)
	}
	if response.Header().Get("Content-Type") !=
		"application/json; charset=utf-8" {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
	if strings.Contains(response.Body.String(), "private") {
		t.Fatalf("response leaked internal error: %s", response.Body.String())
	}

	var payload struct {
		Resource map[string]any `json:"resource"`
		Widgets  struct {
			Body    []map[string]any `json:"body"`
			Sidebar []map[string]any `json:"sidebar"`
		} `json:"widgets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Resource) != 7 ||
		payload.Resource["id"] != float64(7) ||
		payload.Resource["title"] != "Page" ||
		payload.Resource["annotation"] != "" {
		t.Fatalf("resource payload = %#v", payload.Resource)
	}
	if _, exists := payload.Resource["content"]; exists {
		t.Fatalf("resource payload contains content: %#v", payload.Resource)
	}
	if len(payload.Widgets.Body) != 7 || len(payload.Widgets.Sidebar) != 0 {
		t.Fatalf("widgets = %#v", payload.Widgets)
	}

	expectedErrors := map[int]string{
		1: "widget_unavailable",
		2: "invalid_params",
		3: "instance_failed",
		4: "render_failed",
		5: "invalid_result",
	}
	for index, current := range payload.Widgets.Body {
		if _, exists := current["params"]; exists {
			t.Fatalf("widget %d leaked params: %#v", index, current)
		}
		if current["view"] != "default" || current["columns"] != float64(12) {
			t.Fatalf("widget %d presentation = %#v", index, current)
		}
		expectedError, failed := expectedErrors[index]
		if !failed {
			data, ok := current["data"].(map[string]any)
			if !ok || data["value"] != "ok" {
				t.Fatalf("widget %d data = %#v", index, current)
			}
			continue
		}

		publicError, ok := current["error"].(map[string]any)
		if !ok || publicError["code"] != expectedError ||
			len(publicError) != 1 {
			t.Fatalf("widget %d error = %#v", index, current)
		}
		if _, exists := current["data"]; exists {
			t.Fatalf("failed widget %d has data: %#v", index, current)
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/empty", nil)
	request.Host = "example.com"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"widgets":{"body":[],"sidebar":[]}`) {
		t.Fatalf(
			"empty widgets response = %d, %s",
			response.Code,
			response.Body.String(),
		)
	}

	request = httptest.NewRequest(http.MethodGet, "/core-content", nil)
	request.Host = "example.com"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf(
			"core content status = %d, body = %q",
			response.Code,
			response.Body.String(),
		)
	}

	var contentPayload struct {
		Widgets struct {
			Body []struct {
				Code widget.Code    `json:"code"`
				Data map[string]any `json:"data"`
			} `json:"body"`
		} `json:"widgets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &contentPayload); err != nil {
		t.Fatal(err)
	}
	if len(contentPayload.Widgets.Body) != 1 ||
		contentPayload.Widgets.Body[0].Code != "core_content" ||
		contentPayload.Widgets.Body[0].Data["content"] !=
			"Content from resource" {
		t.Fatalf("core content payload = %#v", contentPayload)
	}
}

func TestResourceRootTrailingSlashAndQueryPolicy(t *testing.T) {
	order := make([]string, 0)
	resourceType := transportResourceType{code: "route_policy"}
	module := transportModule{
		code:         "transport",
		resourceType: resourceType,
		order:        &order,
	}
	repository := resourceRepository{
		byPath: map[string]resource.Resource{
			"/": {
				ID:       1,
				SiteID:   1,
				Type:     resourceType.Code(),
				Path:     stringPointer("/"),
				IsPublic: true,
			},
			"/section": {
				ID:       2,
				SiteID:   1,
				Type:     resourceType.Code(),
				Path:     stringPointer("/section"),
				IsPublic: true,
			},
			"/_cms/claimed": {
				ID:       3,
				SiteID:   1,
				Type:     resourceType.Code(),
				Path:     stringPointer("/_cms/claimed"),
				IsPublic: true,
			},
		},
	}
	handler, err := newTestHandler(
		newTransportTestApp(t, module, repository),
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path string
		want int
	}{
		{path: "/", want: http.StatusNoContent},
		{path: "/section", want: http.StatusNoContent},
		{path: "/section?from=query", want: http.StatusNoContent},
		{path: "/section/", want: http.StatusNotFound},
		{path: "/_cms/claimed", want: http.StatusNotFound},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Host = "example.com"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf(
				"path %q status = %d, want %d",
				test.path,
				response.Code,
				test.want,
			)
		}
	}
}

func TestPlatformRuntimeMethodMismatchKeeps405AndAllow(t *testing.T) {
	runtimeApp, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger:           loggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: connectorFactory{},
			Adapters:  []kernel.ModuleDatabaseFactory{databaseFactory{}},
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
	defer func() { _ = runtimeApp.Close() }()
	if err := runtimeApp.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler, err := newTestHandler(runtimeApp)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/_cms/runtime", nil)
	request.Host = "example.com"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed ||
		!strings.Contains(response.Header().Get("Allow"), http.MethodGet) {
		t.Fatalf(
			"status = %d, Allow = %q",
			response.Code,
			response.Header().Get("Allow"),
		)
	}
}

func TestPublicAndProtectedModuleRoutesUseJWTActor(t *testing.T) {
	order := make([]string, 0)
	module := transportModule{
		code:         "auth_routes",
		resourceType: transportResourceType{code: "auth_routes"},
		order:        &order,
	}
	runtimeApp, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger:           loggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: connectorFactory{},
			Adapters: []kernel.ModuleDatabaseFactory{databaseFactory{
				access: knownUserAccessRepository{},
			}},
		},
		Profiles: []kernel.Profile{{
			Code: "dev",
			Modules: []kernel.ProfileModule{
				{Module: core.Module{}},
				{Module: admin.Module{}},
				{Module: module},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtimeApp.Close() }()
	if err := runtimeApp.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler, err := newTestHandler(
		runtimeApp,
		httpserver.WithAccessTokens(staticAccessTokens{
			verified: security.User(42),
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/custom", nil)
	request.Host = "example.com"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf(
			"public route = %d, %q",
			response.Code,
			response.Body.String(),
		)
	}

	request = httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Host = "example.com"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized ||
		response.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf(
			"guest protected route = %d, %#v, %q",
			response.Code,
			response.Header(),
			response.Body.String(),
		)
	}

	request = httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Host = "example.com"
	request.Header.Set("Authorization", "Bearer signed")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf(
			"user protected route = %d, %q",
			response.Code,
			response.Body.String(),
		)
	}

	for _, path := range []string{"/api/sites", "/api/files/disks"} {
		request = httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = "example.com"
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("universal route %s = %d, %#v, %q", path, response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestLoginRouteIsAvailableBeforePrivateSiteResolution(t *testing.T) {
	runtimeApp, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger:           loggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: connectorFactory{},
			Adapters: []kernel.ModuleDatabaseFactory{databaseFactory{
				sites:  repository{isPublic: false},
				access: deniedAccessRepository{},
			}},
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
	defer func() { _ = runtimeApp.Close() }()
	if err := runtimeApp.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler, err := newTestHandler(runtimeApp)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/login",
		strings.NewReader("{"),
	)
	request.Host = "example.com"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf(
			"login route = %d, %q",
			response.Code,
			response.Body.String(),
		)
	}

	request = httptest.NewRequest(
		http.MethodGet,
		"/api/admin/session",
		nil,
	)
	request.Host = "example.com"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized ||
		response.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf(
			"admin session route = %d, %#v, %q",
			response.Code,
			response.Header(),
			response.Body.String(),
		)
	}
}

func TestPlatformMiddlewareCannotBypassJWTAuthentication(
	t *testing.T,
) {
	runtimeApp, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger:           loggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: connectorFactory{},
			Adapters: []kernel.ModuleDatabaseFactory{databaseFactory{
				sites:  repository{isPublic: false},
				access: privilegedUserAccessRepository{},
			}},
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
	defer func() { _ = runtimeApp.Close() }()
	if err := runtimeApp.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}

	handler, err := newTestHandler(
		runtimeApp,
		httpserver.WithPlatformMiddleware(
			func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(
					response http.ResponseWriter,
					request *http.Request,
				) {
					next.ServeHTTP(
						response,
						request.WithContext(
							httptransport.WithActor(
								request.Context(),
								security.User(42),
							),
						),
					)
				})
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/_cms/runtime", nil)
	request.Host = "example.com"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf(
			"status = %d, body = %q",
			response.Code,
			response.Body.String(),
		)
	}
}

func TestJWTAuthenticationActorPrecedesPrivateSiteResolution(
	t *testing.T,
) {
	tests := []struct {
		name       string
		access     coreaccess.Repository
		wantStatus int
	}{
		{
			name:       "user without permission",
			access:     groupUserAccessRepository{allowed: false},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "user with permission",
			access:     groupUserAccessRepository{allowed: true},
			wantStatus: http.StatusOK,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeApp, err := appkernel.New(
				context.Background(),
				appkernel.Definition{
					Logger:           loggerFactory{},
					PasswordHasher:   argon2id.Factory{},
					SiteAccessPolicy: admin.AllowAllSitesPolicy{},
					EventBus:         eventBusFactory{},
					MainDatabase: appkernel.DatabaseDefinition{
						Connector: connectorFactory{},
						Adapters: []kernel.ModuleDatabaseFactory{
							databaseFactory{
								sites:  repository{isPublic: false},
								access: test.access,
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
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = runtimeApp.Close() }()
			if err := runtimeApp.Boot(context.Background()); err != nil {
				t.Fatal(err)
			}

			handler, err := newTestHandler(
				runtimeApp,
				httpserver.WithAccessTokens(staticAccessTokens{
					verified: security.User(42),
				}),
			)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(
				http.MethodGet,
				"/_cms/runtime",
				nil,
			)
			request.Host = "example.com"
			request.Header.Set("Authorization", "Bearer signed")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, body = %q",
					response.Code,
					response.Body.String(),
				)
			}
		})
	}
}

func TestStandardLinkResourceHandlersRedirect(t *testing.T) {
	externalURL := "https://example.org/article"
	externalPath := "/external"
	shortcutPath := "/shortcut"
	targetPath := "/target"
	targetID := resource.ID(3)
	resources := resourceRepository{
		byPath: map[string]resource.Resource{
			externalPath: {
				ID:          1,
				SiteID:      1,
				Type:        resourcetype.Link,
				Path:        &externalPath,
				ExternalURL: &externalURL,
				IsPublic:    true,
			},
			shortcutPath: {
				ID:               2,
				SiteID:           1,
				Type:             resourcetype.ResourceLink,
				Path:             &shortcutPath,
				TargetResourceID: &targetID,
				IsPublic:         true,
			},
		},
		byID: map[resource.ID]resource.Resource{
			targetID: {
				ID:       targetID,
				SiteID:   1,
				Type:     resourcetype.Page,
				Path:     &targetPath,
				IsPublic: true,
			},
		},
	}
	runtimeApp, err := appkernel.New(context.Background(), appkernel.Definition{
		Logger:           loggerFactory{},
		PasswordHasher:   argon2id.Factory{},
		SiteAccessPolicy: admin.AllowAllSitesPolicy{},
		EventBus:         eventBusFactory{},
		MainDatabase: appkernel.DatabaseDefinition{
			Connector: connectorFactory{},
			Adapters: []kernel.ModuleDatabaseFactory{
				databaseFactory{resources: resources},
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
	defer func() { _ = runtimeApp.Close() }()
	if err := runtimeApp.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler, err := newTestHandler(runtimeApp)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path     string
		location string
	}{
		{path: externalPath, location: externalURL},
		{path: shortcutPath, location: targetPath},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Host = "example.com"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusFound ||
			response.Header().Get("Location") != test.location {
			t.Fatalf(
				"path %q response = %d, Location = %q",
				test.path,
				response.Code,
				response.Header().Get("Location"),
			)
		}
	}
}

func TestHTTPAccessLogsSafeStructuredMetadataAndLevels(t *testing.T) {
	var logs bytes.Buffer
	runtimeApp, err := appkernel.New(
		context.Background(),
		appkernel.Definition{
			Logger:           loggerFactory{writer: &logs},
			PasswordHasher:   argon2id.Factory{},
			SiteAccessPolicy: admin.AllowAllSitesPolicy{},
			EventBus:         eventBusFactory{},
			MainDatabase: appkernel.DatabaseDefinition{
				Connector: connectorFactory{},
				Adapters: []kernel.ModuleDatabaseFactory{
					databaseFactory{
						access: groupUserAccessRepository{allowed: true},
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
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtimeApp.Close() }()
	if err := runtimeApp.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}

	queryReachedHandler := false
	testEndpoints := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(
			response http.ResponseWriter,
			request *http.Request,
		) {
			switch request.URL.Path {
			case "/warn":
				http.Error(response, "warn", http.StatusBadRequest)
			case "/rate":
				http.Error(response, "rate", http.StatusTooManyRequests)
			case "/error":
				http.Error(response, "error", http.StatusServiceUnavailable)
			case "/redirect":
				response.WriteHeader(http.StatusFound)
			case "/options":
				response.WriteHeader(http.StatusNoContent)
			case "/panic":
				panic("panic-secret")
			default:
				if request.URL.Query().Get("private") == "query-secret" {
					queryReachedHandler = true
				}
				next.ServeHTTP(response, request)
			}
		})
	}
	handler, err := httpserver.NewHandler(
		runtimeApp,
		httpserver.WithAccessTokens(staticAccessTokens{
			verified: security.User(42),
		}),
		httpserver.WithPlatformMiddleware(testEndpoints),
	)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/_cms/runtime?private=query-secret",
		strings.NewReader("body-secret"),
	)
	request.Host = "example.com"
	request.Header.Set("Authorization", "Bearer token-secret")
	request.Header.Set("Cookie", "session=cookie-secret")
	request.Header.Set("User-Agent", "agent-secret")
	request.Header.Set("Referer", "https://referer-secret.example")
	request.Header.Set("X-Private", "header-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if !queryReachedHandler {
		t.Fatal("query was not available to the HTTP middleware")
	}

	for path, expectedStatus := range map[string]int{
		"/warn":     http.StatusBadRequest,
		"/rate":     http.StatusTooManyRequests,
		"/error":    http.StatusServiceUnavailable,
		"/redirect": http.StatusFound,
		"/panic":    http.StatusInternalServerError,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(
			response,
			httptest.NewRequest(http.MethodGet, path, nil),
		)
		if response.Code != expectedStatus {
			t.Fatalf("%s status = %d", path, response.Code)
		}
	}
	optionsResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		optionsResponse,
		httptest.NewRequest(http.MethodOptions, "/options", nil),
	)
	if optionsResponse.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d", optionsResponse.Code)
	}

	records := accessLogRecords(t, logs.String())
	if len(records) != 7 {
		t.Fatalf("access records = %d: %s", len(records), logs.String())
	}
	byPath := make(map[string]map[string]any, len(records))
	for _, record := range records {
		path, _ := record["url.path"].(string)
		byPath[path] = record
	}

	success := byPath["/_cms/runtime"]
	if success["level"] != "INFO" ||
		success["http.request.method"] != http.MethodGet ||
		success["http.response.status_code"] != float64(http.StatusOK) ||
		success["actor.kind"] != "user" ||
		success["actor.user.id"] != float64(42) ||
		success["client.address"] == "" ||
		success["http.request.id"] == "" ||
		success["http.route"] != "/_cms/runtime" ||
		success["http.server.request.duration"] == nil ||
		success["http.request.body.size"] == nil ||
		success["http.response.body.size"] == nil {
		t.Fatalf("success access record = %#v", success)
	}
	if byPath["/warn"]["level"] != "WARN" {
		t.Fatalf("warn access record = %#v", byPath["/warn"])
	}
	if byPath["/rate"]["level"] != "WARN" {
		t.Fatalf("rate-limit access record = %#v", byPath["/rate"])
	}
	if byPath["/error"]["level"] != "ERROR" {
		t.Fatalf("error access record = %#v", byPath["/error"])
	}
	if byPath["/redirect"]["level"] != "INFO" ||
		byPath["/options"]["level"] != "INFO" {
		t.Fatalf(
			"success access records = redirect %#v, OPTIONS %#v",
			byPath["/redirect"],
			byPath["/options"],
		)
	}
	panicRecord := byPath["/panic"]
	if panicRecord["level"] != "ERROR" ||
		panicRecord["exception.stacktrace"] == nil {
		t.Fatalf("panic access record = %#v", panicRecord)
	}

	serialized, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"query-secret",
		"body-secret",
		"token-secret",
		"cookie-secret",
		"agent-secret",
		"referer-secret",
		"header-secret",
		"panic-secret",
		"Authorization",
		"Cookie",
	} {
		if bytes.Contains(serialized, []byte(secret)) {
			t.Fatalf("access logs leaked %q: %s", secret, serialized)
		}
	}
}

func accessLogRecords(
	t *testing.T,
	output string,
) []map[string]any {
	t.Helper()
	var records []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode log: %v; line = %q", err, scanner.Text())
		}
		if record["msg"] == "HTTP request completed" {
			records = append(records, record)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestPageWidgetBindingsResolveCurrentResourceAndIsolateInvalidValues(t *testing.T) {
	ref := widget.NewRef("bound")
	required := true
	echo := widget.Functional{Description: widget.Definition{Reference: ref, Label: "Bound", Description: "Bound values", Fields: []field.Definition{{Key: "text", Label: "Text", Type: field.TypeString, Required: &required, Rules: []string{"min=3"}}}}, Render: func(_ context.Context, _ widget.RenderInput, params map[string]any) (map[string]any, error) {
		return params, nil
	}}
	order := []string{}
	module := transportModule{code: "binding", resourceType: transportResourceType{code: "binding_test"}, order: &order, widgets: []widget.Widget{echo}}
	code := template.Code("bound_page")
	repo := resourceRepository{byPath: map[string]resource.Resource{"/bound": {ID: 7, SiteID: 1, Type: resourcetype.Page, Template: &code, Title: "First title", Path: stringPointer("/bound"), IsPublic: true, Fields: map[string]any{"headline": "First field"}, Widgets: []widget.Binding{{ID: 1, Code: "binding_bound", Area: widget.AreaBody, Presentation: widget.DefaultPresentation(), ParamBindings: widget.ParamBindings{"text": widget.ResourceField("headline")}}}}}}
	app := newTransportTestApp(t, module, repo, template.Definition{Code: code, Label: "Bound", Fields: []field.Definition{{Key: "headline", Label: "Headline", Type: field.TypeString}}, Layout: template.Layout{Body: []template.Item{template.Widget{Widget: ref, ParamBindings: widget.ParamBindings{"text": widget.ResourceProperty("title")}}, template.ResourceWidgets{}}}})
	handler, err := newTestHandler(app)
	if err != nil {
		t.Fatal(err)
	}
	for _, values := range []struct {
		title, headline string
		invalid         bool
	}{{"First title", "First field", false}, {"Next title", "Next field", false}, {"Still valid", "x", true}} {
		current := repo.byPath["/bound"]
		current.Title = values.title
		current.Fields["headline"] = values.headline
		repo.byPath["/bound"] = current
		request := httptest.NewRequest(http.MethodGet, "/bound", nil)
		request.Host = "example.com"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var payload struct {
			Widgets struct {
				Body []struct {
					Data  map[string]any `json:"data"`
					Error *struct {
						Code string `json:"code"`
					} `json:"error"`
				} `json:"body"`
			} `json:"widgets"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Widgets.Body) != 2 || payload.Widgets.Body[0].Data["text"] != values.title {
			t.Fatalf("template widget: %s", response.Body.String())
		}
		bound := payload.Widgets.Body[1]
		if values.invalid {
			if bound.Error == nil || bound.Error.Code != "invalid_params" {
				t.Fatalf("error not isolated: %s", response.Body.String())
			}
		} else if bound.Error != nil || bound.Data["text"] != values.headline {
			t.Fatalf("resource widget: %s", response.Body.String())
		}
		if strings.Contains(response.Body.String(), "param_bindings") {
			t.Fatal("configuration leaked into public envelope")
		}
	}
}
