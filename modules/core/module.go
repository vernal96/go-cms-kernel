package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

const ModuleCode kernel.ModuleCode = "core"

const (
	DurableCacheAlias   cache.Alias = "durable"
	HotCacheAlias       cache.Alias = "hot"
	ThumbnailCacheAlias cache.Alias = "thumbnails"
)

const defaultRepositoryCacheTTL = 5 * time.Minute

type Config struct {
	Caches             []cache.Binding
	MediaSettings      []media.SettingsDefinition
	Images             *image.Limits
	RepositoryCacheTTL time.Duration
	MenuCacheTTL       time.Duration
	ResourcePreview    resource.PreviewPolicy
	ResourceRevisions  *resource.RevisionPolicy
}

type RepositoryCacheDescriptor struct {
	Code      cache.Code
	Namespace string
	TTL       time.Duration
}

// Database is the persistence boundary required by the core module.
// Its concrete implementation is selected by the main application binding.
type Database interface {
	kernel.ModuleDatabase
	Sites() site.Repository
	Resources() resource.Repository
	Files() file.Repository
	Media() media.Repository
	Users() user.Repository
	Groups() group.Repository
	Access() access.Repository
}

type module struct {
	config   Config
	services *Services
}

// BindServices returns the core module declaration bound to the
// application-scoped core services assembled by the composition root.
func BindServices(declaration kernel.Module, services *Services) (kernel.Module, error) {
	coreModule, ok := declaration.(module)
	if !ok {
		return nil, fmt.Errorf("core module has invalid type %T", declaration)
	}
	if services == nil {
		return nil, errors.New("core services are nil")
	}
	coreModule.services = services
	return coreModule, nil
}

func (module) Code() kernel.ModuleCode {
	return ModuleCode
}

func (module) ModuleDescriptor() kernel.ModuleDescriptor {
	return kernel.ModuleDescriptor{
		Label:       "Core",
		Description: "Базовые возможности управления содержимым",
	}
}

func (module) baseRegistry() kernel.ModuleRegistry {
	return kernel.ModuleRegistry{
		FieldTypes:     field.StandardTypes(),
		ValidatorTypes: field.StandardValidatorTypes(),
		ResourceTypes:  resourcetype.StandardTypes(),
		PermissionEntities: []permission.Entity{
			{Code: "site"},
			{Code: "resource"},
			{Code: "resource_history", Actions: []permission.Action{permission.Read, permission.Delete}},
			{Code: "file"},
			{Code: "media"},
			{Code: "user"},
			{Code: "group"},
		},
	}
}

func (m module) Build(
	_ context.Context,
	ctx kernel.ModuleContext,
) (kernel.ModuleRuntime, error) {
	database, err := kernel.ModuleDatabaseFrom[Database](
		ctx,
		"",
		ModuleCode,
	)
	if err != nil {
		return nil, err
	}
	if err := validateModuleDatabase(database); err != nil {
		return nil, err
	}

	if ModuleCode != ctx.ModuleCode() {
		return nil, errors.New("core module context has invalid code")
	}
	if m.services == nil {
		return nil, errors.New("core services are not bound")
	}

	config, err := normalizeModuleConfig(m.config)
	if err != nil {
		return nil, err
	}
	var hotStore cache.Store
	if caches := ctx.Caches(); caches != nil {
		hotStore, _ = caches.Store(HotCacheAlias)
	}
	revisionPolicy := resource.DefaultRevisionPolicy()
	if config.ResourceRevisions != nil {
		revisionPolicy = *config.ResourceRevisions
	}

	var descriptor *RepositoryCacheDescriptor
	if caches := ctx.Caches(); caches != nil {
		store, exists := caches.Store(DurableCacheAlias)
		if exists {
			binding, bindingExists := caches.Binding(DurableCacheAlias)
			if !bindingExists {
				return nil, errors.New(
					"core repository cache binding is unavailable",
				)
			}
			descriptor = &RepositoryCacheDescriptor{
				Code:      binding.Code,
				Namespace: binding.Namespace,
				TTL:       config.RepositoryCacheTTL,
			}
			siteID, err := strconv.ParseInt(ctx.Scope().SiteID(), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("repository cache requires site scope: %w", err)
			}
			database = newCachedDatabase(
				database,
				store,
				config.RepositoryCacheTTL,
				m.services.cachePolicy,
				site.ID(siteID),
			)
		}
	}

	runtime := &Runtime{
		resultStore:     hotStore,
		database:        database,
		repositoryCache: descriptor,
		services:        m.services,
		authorization:   m.services.Authorization,
		resourcePreview: config.ResourcePreview,
		revisionPolicy:  revisionPolicy,
		logger:          ctx.Logger(),
		menuStore:       hotStore,
		menuTTL:         config.MenuCacheTTL,
	}
	if err := buildWidgets(runtime, ctx.Registry().ResourceTypes(), ctx.Profile().Templates); err != nil {
		return nil, fmt.Errorf("build core widgets: %w", err)
	}
	catalog, err := media.CompileSettings(config.MediaSettings, ctx.Registry())
	if err != nil {
		return nil, err
	}
	if len(config.MediaSettings) > 0 {
		runtime.mediaSettings, err = media.NewSettingsService(catalog, database.Media(), m.services.Files, m.services.Authorization)
		if err != nil {
			return nil, err
		}
	}
	return runtime, nil
}

type Runtime struct {
	resultStore     cache.Store
	mediaSettings   *media.SettingsService
	menuStore       cache.Store
	menuTTL         time.Duration
	database        Database
	repositoryCache *RepositoryCacheDescriptor
	services        *Services
	authorization   security.Authorizer
	resourcePreview resource.PreviewPolicy
	revisionPolicy  resource.RevisionPolicy
	logger          *slog.Logger
	widgets         []widget.Widget
}

// ResourceRevisionPolicy exposes the profile's semantic history policy without
// leaking persistence details into resource services.
func (r *Runtime) ResourceRevisionPolicy() resource.RevisionPolicy {
	if r == nil {
		return resource.DefaultRevisionPolicy()
	}
	return r.revisionPolicy
}

func (r *Runtime) ModuleCode() kernel.ModuleCode {
	return ModuleCode
}

func (r *Runtime) Database() Database {
	return r.database
}

func (r *Runtime) Users() user.Service {
	if r == nil || r.services == nil {
		return nil
	}
	return r.services.Users
}

func (r *Runtime) Authorization() security.Authorizer {
	if r == nil || r.services == nil {
		return nil
	}
	return r.services.Authorization
}

func (r *Runtime) Files() file.ManagementService {
	if r == nil || r.services == nil {
		return nil
	}
	return r.services.Files
}

func (r *Runtime) RepositoryCache() (
	RepositoryCacheDescriptor,
	bool,
) {
	if r == nil || r.repositoryCache == nil {
		return RepositoryCacheDescriptor{}, false
	}
	return *r.repositoryCache, true
}

var _ kernel.Module = module{}
var _ kernel.ModuleDescriptorProvider = module{}
var _ kernel.RegistryProvider = module{}
var _ kernel.ModuleRuntime = (*Runtime)(nil)

func (module) EntityHookEventNames() []string {
	return []string{resource.EventCreated, resource.EventUpdated, resource.EventDeleted, "user.created", "user.updated"}
}

func (r *Runtime) MediaSettings() *media.SettingsService { return r.mediaSettings }

func (m module) Registry() (kernel.ModuleRegistry, error) {
	settings, err := media.SettingsFields(m.config.MediaSettings)
	if err != nil {
		return kernel.ModuleRegistry{}, err
	}
	registry := m.baseRegistry()
	for i, t := range registry.FieldTypes {
		if t.Code() == field.TypeMedia {
			registry.FieldTypes[i] = field.MediaType(settings)
		}
	}
	return registry, nil
}

// clone detaches mutable declarations from the caller.
func (c Config) clone() Config {
	c.Caches = append([]cache.Binding(nil), c.Caches...)
	c.MediaSettings = append([]media.SettingsDefinition(nil), c.MediaSettings...)
	for i := range c.MediaSettings {
		c.MediaSettings[i].Fields = field.CloneDefinitions(c.MediaSettings[i].Fields)
	}
	if c.Images != nil {
		v := *c.Images
		c.Images = &v
	}
	if c.ResourceRevisions != nil {
		v := *c.ResourceRevisions
		c.ResourceRevisions = &v
	}
	return c
}

func (m module) CacheBindings() []cache.Binding {
	return append([]cache.Binding(nil), m.config.Caches...)
}

// New declares an immutable module with its own typed configuration.
func New(config Config) kernel.Module {
	return module{config: config.clone()}
}

func (m module) Validate(ctx context.Context, environment kernel.ModuleValidationContext) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	database, err := kernel.ModuleDatabaseFrom[Database](environment, "", ModuleCode)
	if err != nil {
		return err
	}
	if err := validateModuleDatabase(database); err != nil {
		return err
	}
	if _, err := normalizeModuleConfig(m.config); err != nil {
		return err
	}
	_, err = media.CompileSettings(m.config.MediaSettings, environment.Registry())
	return err
}

func validateModuleDatabase(database Database) error {
	if database.Sites() == nil {
		return errors.New("core site repository is nil")
	}
	if database.Resources() == nil {
		return errors.New("core resource repository is nil")
	}
	if database.Files() == nil {
		return errors.New("core file repository is nil")
	}
	if database.Media() == nil {
		return errors.New("core media repository is nil")
	}
	if database.Users() == nil {
		return errors.New("core user repository is nil")
	}
	if database.Groups() == nil {
		return errors.New("core group repository is nil")
	}
	if database.Access() == nil {
		return errors.New("core access repository is nil")
	}
	return nil
}

func normalizeModuleConfig(config Config) (Config, error) {
	if config.RepositoryCacheTTL == 0 {
		config.RepositoryCacheTTL = defaultRepositoryCacheTTL
	}
	if config.RepositoryCacheTTL < 0 {
		return Config{}, errors.New("core repository cache TTL is invalid")
	}
	if config.MenuCacheTTL == 0 {
		config.MenuCacheTTL = 5 * time.Minute
	}
	if config.MenuCacheTTL < 0 {
		return Config{}, errors.New("core menu cache TTL is invalid")
	}
	if config.Images != nil {
		if err := config.Images.Validate(); err != nil {
			return Config{}, err
		}
	}
	for _, binding := range config.Caches {
		if binding.Alias != DurableCacheAlias && binding.Alias != HotCacheAlias && binding.Alias != ThumbnailCacheAlias {
			return Config{}, fmt.Errorf("unknown core cache alias %q", binding.Alias)
		}
	}
	return config, nil
}
