package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/console"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/logging"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core"
	corecommands "github.com/vernal96/go-cms-kernel/modules/core/commands"
	coremanagement "github.com/vernal96/go-cms-kernel/modules/core/management"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	coreuser "github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/outbox"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/seeds"
)

var (
	ErrClosed    = errors.New("app is closed")
	ErrNotBooted = errors.New("app is not booted")
)

var requiredModuleCodes = [...]kernel.ModuleCode{
	core.ModuleCode,
	admin.ModuleCode,
}

type ConnectorFactory = kernel.ConnectorFactory
type ModuleDatabaseFactory = kernel.ModuleDatabaseFactory

// ModuleSeedSource attaches project data to a module on a database binding.
type ModuleSeedSource struct {
	Module kernel.ModuleCode
	Source seeds.Source
}

type DatabaseDefinition struct {
	Connector ConnectorFactory
	Adapters  []ModuleDatabaseFactory
	Seeds     []ModuleSeedSource
}

type Definition struct {
	Logger              logging.Factory
	EventBus            eventbus.Factory
	MainDatabase        DatabaseDefinition
	AdditionalDatabases []DatabaseDefinition
	Filesystems         []filesystem.Factory
	Caches              []cache.Factory
	ModuleApplications  []kernel.ModuleApplication
	Profiles            []kernel.Profile
	PasswordHasher      coreuser.PasswordHasherFactory
	SiteAccessPolicy    coremanagement.SiteAccessPolicy
	MaxUploadSize       int64
	UploadTimeout       time.Duration
	AvatarStorage       filesystem.Code
	AvatarMaxSize       int64
	OutboxPublisher     outbox.PublisherConfig
}

type bindingRuntime struct {
	connector kernel.DBConnector
	adapters  map[kernel.ModuleCode]kernel.ModuleDatabase
}

type App struct {
	hookSources      []entityhooks.Source
	applicationHooks *entityhooks.Registry
	definition       Definition

	loggerConnector logging.Connector
	logger          *slog.Logger
	eventBus        eventbus.Connector
	main            *bindingRuntime
	additional      map[kernel.ConnectionCode]*bindingRuntime
	connectors      []kernel.DBConnector
	filesystems     *filesystem.Manager
	caches          *cache.Manager
	coreDatabase    core.Database
	migrationPlan   []migrations.Plan
	seedPlan        []seeds.Plan
	providers       []console.Provider
	console         *console.Console
	outboxSources   []outbox.Source
	outboxPublisher *outbox.Publisher
	workerCancel    context.CancelFunc
	workers         sync.WaitGroup

	profileBlueprints map[kernel.ProfileCode]*kernel.ProfileBlueprint
	sites             *site.Catalog
	services          Services
	permissions       *permission.Catalog
	cmsSites          *coremanagement.Sites
	cmsResources      *coremanagement.Resources
	cmsFiles          *coremanagement.Files
	adminManagement   *admin.Management
	siteAccessPolicy  coremanagement.SiteAccessPolicy

	bootOnce sync.Once
	bootErr  error
	booted   atomic.Bool
	closed   atomic.Bool

	lifecycleMu sync.RWMutex
	closeOnce   sync.Once
	closeErr    error
}

func New(
	ctx context.Context,
	definition Definition,
) (_ *App, resultErr error) {
	if ctx == nil {
		return nil, errors.New("app context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	definition = cloneDefinition(definition)
	if err := validateDefinition(definition); err != nil {
		return nil, err
	}

	application := &App{
		definition: definition,
		additional: make(map[kernel.ConnectionCode]*bindingRuntime),
	}

	defer func() {
		if resultErr == nil {
			return
		}

		reported := application.logger != nil
		if application.logger != nil {
			application.logger.ErrorContext(
				context.WithoutCancel(ctx),
				"application initialization failed",
				slog.String("event", "app.initialization.failed"),
				slog.Any("error", resultErr),
			)
		}
		resultErr = errors.Join(resultErr, application.Close())
		if reported {
			resultErr = logging.Reported(resultErr)
		}
	}()

	loggerConnector, err := definition.Logger.Open(ctx)
	if loggerConnector != nil {
		application.loggerConnector = loggerConnector
	}
	if err != nil {
		return nil, fmt.Errorf("open project logger: %w", err)
	}
	if loggerConnector == nil {
		return nil, errors.New("logger factory returned nil connector")
	}
	application.logger = loggerConnector.Logger()
	if application.logger == nil {
		return nil, errors.New("logger connector returned nil logger")
	}
	if err := loggerConnector.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping project logger: %w", err)
	}
	application.logger.InfoContext(
		ctx,
		"application initialization started",
		slog.String("event", "app.initialization.started"),
	)

	eventBusConnector, err := definition.EventBus.Open(ctx)
	if eventBusConnector != nil {
		application.eventBus = eventBusConnector
	}
	if err != nil {
		return nil, fmt.Errorf("open project event bus: %w", err)
	}
	if eventBusConnector == nil {
		return nil, errors.New("event bus factory returned nil connector")
	}
	if err := eventBusConnector.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping project event bus: %w", err)
	}

	filesystems, err := filesystem.NewManager(ctx, definition.Filesystems)
	if err != nil {
		return nil, err
	}
	application.filesystems = filesystems

	caches, err := cache.NewManager(
		ctx,
		definition.Caches,
		cache.Dependencies{
			Filesystems: filesystems,
			Observer:    cache.NewSlogObserver(application.logger),
		},
	)
	if err != nil {
		return nil, err
	}
	application.caches = caches

	definitions := make(
		[]DatabaseDefinition,
		0,
		len(definition.AdditionalDatabases)+1,
	)
	definitions = append(definitions, definition.MainDatabase)
	definitions = append(definitions, definition.AdditionalDatabases...)

	for index, databaseDefinition := range definitions {
		binding, err := application.openBinding(ctx, databaseDefinition)
		if err != nil {
			return nil, err
		}

		if index == 0 {
			application.main = binding
		} else {
			application.additional[binding.connector.Code()] = binding
		}
	}

	coreAdapter, exists := application.main.adapters[core.ModuleCode]
	if !exists {
		return nil, errors.New(
			"main database binding does not contain core database",
		)
	}

	coreDatabase, ok := coreAdapter.(core.Database)
	if !ok {
		return nil, fmt.Errorf(
			"main database adapter %q does not implement core.Database",
			core.ModuleCode,
		)
	}
	application.coreDatabase = coreDatabase

	permissionCatalog, err := buildPermissionCatalog(definition.Profiles)
	if err != nil {
		return nil, err
	}
	application.permissions = permissionCatalog

	application.collectModuleCommandProviders()
	application.addProvider(
		"cache:maintenance",
		cacheMaintenanceProvider{manager: application.caches},
	)
	application.addProvider(
		"core:identity-commands",
		corecommands.New(application),
	)
	runner, err := console.New(application)
	if err != nil {
		return nil, err
	}
	application.console = runner

	application.logger.InfoContext(
		ctx,
		"application initialized",
		slog.String("event", "app.initialization.completed"),
	)
	return application, nil
}

var _ kernel.DatabaseResolver = (*App)(nil)
var _ console.Application = (*App)(nil)
