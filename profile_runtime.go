package kernel

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

type ProfileRuntime struct {
	hooks     *entityhooks.Registry
	blueprint *ProfileBlueprint
	registry  Registry
	widgets   *widget.Catalog
	templates *template.Catalog
}

type ProfileBlueprint struct {
	profile     Profile
	registry    *RuntimeRegistry
	paramSchema *field.Schema
	templates   *template.Catalog
	factory     *ProfileRuntimeFactory
}

func (b *ProfileBlueprint) Profile() Profile {
	if b == nil {
		return Profile{}
	}
	return cloneProfile(b.profile)
}

func (b *ProfileBlueprint) Registry() DefinitionRegistry {
	if b == nil {
		return nil
	}
	return b.registry
}

func (b *ProfileBlueprint) ParamSchema() *field.Schema {
	if b == nil {
		return nil
	}
	return b.paramSchema
}

func (b *ProfileBlueprint) Template(
	code template.Code,
) (*template.Runtime, bool) {
	if b == nil || b.templates == nil {
		return nil, false
	}
	return b.templates.Template(code)
}

func (b *ProfileBlueprint) Templates() []template.Definition {
	if b == nil || b.templates == nil {
		return nil
	}
	return b.templates.Definitions()
}

func (r *ProfileRuntime) Profile() Profile {
	if r == nil || r.blueprint == nil {
		return Profile{}
	}
	return r.blueprint.Profile()
}

func (r *ProfileRuntime) Blueprint() *ProfileBlueprint {
	if r == nil {
		return nil
	}
	return r.blueprint
}

func (r *ProfileRuntime) EntityHooks() *entityhooks.Registry { return r.hooks }

func (r *ProfileRuntime) Registry() Registry {
	return r.registry
}

func (r *ProfileRuntime) Modules() []ModuleRuntime {
	if r == nil || r.registry == nil {
		return nil
	}
	return r.registry.Modules()
}

func (r *ProfileRuntime) ParamSchema() *field.Schema {
	if r == nil || r.blueprint == nil {
		return nil
	}
	return r.blueprint.ParamSchema()
}

func (r *ProfileRuntime) Template(
	code template.Code,
) (*template.Runtime, bool) {
	if r == nil || r.templates == nil {
		return nil, false
	}
	return r.templates.Template(code)
}

func (r *ProfileRuntime) Templates() []template.Definition {
	if r == nil || r.templates == nil {
		return nil
	}
	return r.templates.Definitions()
}

func (r *ProfileRuntime) Widget(
	code widget.Code,
) (*widget.Runtime, bool) {
	if r == nil || r.widgets == nil {
		return nil, false
	}

	return r.widgets.Widget(code)
}

func (r *ProfileRuntime) Widgets() []widget.Definition {
	if r == nil || r.widgets == nil {
		return nil
	}

	return r.widgets.Definitions()
}

type ProfileRuntimeFactory struct {
	resolver     DatabaseResolver
	services     RuntimeServices
	applications map[ModuleCode]ModuleApplication
}

type RuntimeServices struct {
	Caches             cache.Resolver
	Filesystems        filesystem.Resolver
	EventBus           eventbus.Bus
	Logger             *slog.Logger
	ModuleApplications []ModuleApplication
}

func ModuleDependencyFrom[T ModuleRuntime](
	ctx ModuleContext,
	moduleCode ModuleCode,
) (T, error) {
	var zero T
	runtime, exists := ctx.Dependency(moduleCode)
	if !exists {
		return zero, fmt.Errorf(
			"module %q dependency %q is unavailable or undeclared",
			ctx.ModuleCode(),
			moduleCode,
		)
	}
	dependency, ok := runtime.(T)
	if !ok {
		return zero, fmt.Errorf(
			"module %q dependency %q has invalid runtime type %T",
			ctx.ModuleCode(),
			moduleCode,
			runtime,
		)
	}
	return dependency, nil
}

func NewProfileRuntimeFactory(
	resolver DatabaseResolver,
	services RuntimeServices,
) (*ProfileRuntimeFactory, error) {
	if resolver == nil {
		return nil, errors.New("database resolver is nil")
	}

	if services.Logger == nil {
		return nil, errors.New("runtime logger is nil")
	}
	if services.EventBus == nil || isNilValue(services.EventBus) {
		return nil, errors.New("runtime event bus is nil")
	}
	applications := make(map[ModuleCode]ModuleApplication, len(services.ModuleApplications))
	for index, application := range services.ModuleApplications {
		if application == nil || isNilValue(application) {
			return nil, fmt.Errorf("module application at index %d is nil", index)
		}
		moduleCode := application.ModuleCode()
		if moduleCode == "" {
			return nil, fmt.Errorf("module application at index %d has empty module code", index)
		}
		if _, exists := applications[moduleCode]; exists {
			return nil, fmt.Errorf("module application %q is defined more than once", moduleCode)
		}
		applications[moduleCode] = application
	}
	services.ModuleApplications = append([]ModuleApplication(nil), services.ModuleApplications...)
	return &ProfileRuntimeFactory{
		resolver:     resolver,
		services:     services,
		applications: applications,
	}, nil
}
