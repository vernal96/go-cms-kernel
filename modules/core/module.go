package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

type Module struct {
	services *Services
}

// BindServices returns the core module declaration bound to the
// application-scoped core services assembled by the composition root.
func BindServices(module kernel.Module, services *Services) (kernel.Module, error) {
	coreModule, ok := module.(Module)
	if !ok {
		return nil, fmt.Errorf("core module has invalid type %T", module)
	}
	if services == nil {
		return nil, errors.New("core services are nil")
	}
	coreModule.services = services
	return coreModule, nil
}

func (Module) Code() kernel.ModuleCode {
	return ModuleCode
}

func (Module) ModuleDescriptor() kernel.ModuleDescriptor {
	return kernel.ModuleDescriptor{
		Label:       "Core",
		Description: "Базовые возможности управления содержимым",
	}
}

func (Module) Registry() kernel.ModuleRegistry {
	return kernel.ModuleRegistry{
		FieldTypes:    field.StandardTypes(),
		ResourceTypes: resourcetype.StandardTypes(),
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

func (m Module) Build(
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

	if database.Sites() == nil {
		return nil, errors.New("core site repository is nil")
	}
	if database.Resources() == nil {
		return nil, errors.New("core resource repository is nil")
	}
	if database.Files() == nil {
		return nil, errors.New("core file repository is nil")
	}
	if database.Media() == nil {
		return nil, errors.New("core media repository is nil")
	}
	if database.Users() == nil {
		return nil, errors.New("core user repository is nil")
	}
	if database.Groups() == nil {
		return nil, errors.New("core group repository is nil")
	}
	if database.Access() == nil {
		return nil, errors.New("core access repository is nil")
	}
	if ModuleCode != ctx.ModuleCode() {
		return nil, errors.New("core module context has invalid code")
	}
	if m.services == nil {
		return nil, errors.New("core services are not bound")
	}

	config, err := kernel.ModuleConfigFrom[Config](ctx)
	if err != nil {
		return nil, err
	}
	if config.RepositoryCacheTTL == 0 {
		config.RepositoryCacheTTL = defaultRepositoryCacheTTL
	}
	if config.RepositoryCacheTTL < 0 {
		return nil, errors.New("core repository cache TTL is invalid")
	}
	if config.MenuCacheTTL == 0 {
		config.MenuCacheTTL = 5 * time.Minute
	}
	if config.MenuCacheTTL < 0 {
		return nil, errors.New("core menu cache TTL is invalid")
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
	var durableStore cache.Store
	if caches := ctx.Caches(); caches != nil {
		store, exists := caches.Store(DurableCacheAlias)
		if exists {
			durableStore = store
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
			database = newCachedDatabase(
				database,
				store,
				config.RepositoryCacheTTL,
				m.services.cachePolicy,
			)
		}
	}

	runtime := &Runtime{
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
	if err := buildWidgets(runtime, durableStore, ctx.Registry().ResourceTypes(), ctx.Profile().Templates); err != nil {
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

var _ kernel.Module = Module{}
var _ kernel.ModuleDescriptorProvider = Module{}
var _ kernel.RegistryProvider = Module{}
var _ kernel.ModuleRuntime = (*Runtime)(nil)

func (Module) EntityHookEventNames() []string {
	return []string{resource.EventCreated, resource.EventUpdated, resource.EventDeleted, "user.created", "user.updated"}
}

func (r *Runtime) MediaSettings() *media.SettingsService { return r.mediaSettings }

func (m Module) RegistryForConfig(value any) (kernel.ModuleRegistry, error) {
	config := Config{}
	if value != nil {
		var ok bool
		config, ok = value.(Config)
		if !ok {
			return kernel.ModuleRegistry{}, fmt.Errorf("invalid core config %T", value)
		}
	}
	settings, err := media.SettingsFields(config.MediaSettings)
	if err != nil {
		return kernel.ModuleRegistry{}, err
	}
	registry := m.Registry()
	for i, t := range registry.FieldTypes {
		if t.Code() == field.TypeMedia {
			registry.FieldTypes[i] = field.MediaType(settings)
		}
	}
	return registry, nil
}

// CloneModuleConfig detaches mutable declarations from the caller and runtime readers.
func (c Config) CloneModuleConfig() any {
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
