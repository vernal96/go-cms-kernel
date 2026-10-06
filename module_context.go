package kernel

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/filesystem"
)

type ModuleContext struct {
	hooks        entityhooks.Registrar
	resolver     DatabaseResolver
	moduleCode   ModuleCode
	application  ModuleApplication
	profile      Profile
	registry     DefinitionRegistry
	scope        RuntimeScope
	dependencies map[ModuleCode]ModuleRuntime
	caches       cache.ModuleManager
	filesystems  filesystem.ModuleManager
	eventBus     eventbus.Bus
	logger       *slog.Logger
}

func newModuleContext(
	resolver DatabaseResolver,
	moduleCode ModuleCode,
	application ModuleApplication,
	profile Profile,
	registry DefinitionRegistry,
	scope RuntimeScope,
	services RuntimeServices,
	dependencies map[ModuleCode]ModuleRuntime,
	caches cache.ModuleManager,
	filesystems filesystem.ModuleManager,
) ModuleContext {
	return ModuleContext{
		resolver:     resolver,
		moduleCode:   moduleCode,
		application:  application,
		profile:      cloneProfile(profile),
		registry:     registry,
		scope:        scope.clone(),
		dependencies: dependencies,
		caches:       caches,
		filesystems:  filesystems,
		eventBus:     services.EventBus,
		logger:       services.Logger,
	}
}

func (c ModuleContext) EntityHooks() entityhooks.Registrar { return c.hooks }

func (c ModuleContext) ModuleCode() ModuleCode {
	return c.moduleCode
}

func ModuleApplicationFrom[T ModuleApplication](environment moduleEnvironment) (T, error) {
	ctx := environment.moduleContext()
	var zero T
	if ctx.application == nil || isNilValue(ctx.application) {
		return zero, fmt.Errorf("module %q application dependency is unavailable", ctx.ModuleCode())
	}
	application, ok := ctx.application.(T)
	if !ok || isNilValue(application) {
		return zero, fmt.Errorf(
			"module %q application dependency has invalid type %T, expected %T",
			ctx.ModuleCode(),
			ctx.application,
			zero,
		)
	}
	return application, nil
}

func (c ModuleContext) Profile() Profile {
	return cloneProfile(c.profile)
}

func (c ModuleContext) Registry() DefinitionRegistry {
	return c.registry
}

func (c ModuleContext) Scope() RuntimeScope {
	return c.scope.clone()
}

func (c ModuleContext) Dependency(code ModuleCode) (ModuleRuntime, bool) {
	runtime, exists := c.dependencies[code]
	return runtime, exists
}

func (c ModuleContext) Caches() cache.ModuleManager {
	return c.caches
}

func (c ModuleContext) Filesystems() filesystem.ModuleManager {
	return c.filesystems
}

func (c ModuleContext) EventBus() eventbus.Bus {
	return c.eventBus
}

func (c ModuleContext) Logger() *slog.Logger {
	return c.logger
}

func ModuleDatabaseFrom[T ModuleDatabase](
	environment moduleEnvironment,
	connectionCode ConnectionCode,
	moduleCode ModuleCode,
) (T, error) {
	ctx := environment.moduleContext()
	var zero T
	if !environment.allowsDatabase(moduleCode) {
		return zero, errors.New("cannot validate another module database")
	}
	if ctx.resolver == nil {
		return zero, fmt.Errorf("database for module %q is unavailable", moduleCode)
	}

	var (
		database ModuleDatabase
		exists   bool
	)

	if connectionCode == "" {
		database, exists = ctx.resolver.MainModuleDatabase(
			moduleCode,
		)
	} else {
		database, exists = ctx.resolver.ModuleDatabase(
			connectionCode,
			moduleCode,
		)
	}

	if !exists {
		return zero, fmt.Errorf(
			"database for module %q on connection %q not found",
			moduleCode,
			connectionCode,
		)
	}

	result, ok := database.(T)
	if !ok || isNilValue(result) {
		return zero, fmt.Errorf(
			"database for module %q has invalid type %T",
			moduleCode,
			database,
		)
	}

	return result, nil
}

type ModuleValidationContext struct {
	context ModuleContext
	disks   filesystem.Resolver
}

type moduleEnvironment interface {
	moduleContext() ModuleContext
	allowsDatabase(ModuleCode) bool
}

func (c ModuleContext) moduleContext() ModuleContext { return c }

func (c ModuleValidationContext) moduleContext() ModuleContext { return c.context }

func (c ModuleValidationContext) Registry() DefinitionRegistry { return c.context.registry }

func (c ModuleValidationContext) Profile() Profile { return cloneProfile(c.context.profile) }

func (c ModuleValidationContext) Caches() cache.ModuleManager { return c.context.caches }

func (c ModuleValidationContext) Filesystems() filesystem.ModuleManager { return c.context.filesystems }

func (c ModuleValidationContext) Disk(code filesystem.Code) (filesystem.Disk, bool) {
	if c.disks == nil {
		return nil, false
	}
	return c.disks.Disk(code)
}

func (ModuleContext) allowsDatabase(ModuleCode) bool { return true }

func (c ModuleValidationContext) allowsDatabase(code ModuleCode) bool {
	return code == c.context.moduleCode
}
