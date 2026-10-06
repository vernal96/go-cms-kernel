package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func (b *ProfileBlueprint) Build(
	ctx context.Context,
	scope RuntimeScope,
) (*ProfileRuntime, error) {
	if b == nil || b.factory == nil {
		return nil, errors.New("profile blueprint is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("profile runtime context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope.SiteID() == "" {
		return nil, errors.New("profile runtime site scope is empty")
	}

	profile := b.profile
	registry := b.registry.cloneDefinitions()
	hooks := entityhooks.NewRegistry(entityhooks.Site, scope.SiteID())
	widgetSources := make([]widget.Source, 0, len(profile.Modules))
	for _, module := range profile.Modules {
		dependencies, err := resolveModuleDependencies(module, registry)
		if err != nil {
			return nil, fmt.Errorf(
				"resolve dependencies for module %q in profile %q: %w",
				module.Code(),
				profile.Code,
				err,
			)
		}

		moduleCaches, err := cache.NewRuntimeModuleManager(
			b.factory.services.Caches,
			cache.RuntimeScope{
				Profile: string(profile.Code),
				Site:    scope.SiteID(),
			},
			string(module.Code()),
			moduleCacheBindings(module),
		)
		if err != nil {
			return nil, fmt.Errorf(
				"configure caches for module %q in profile %q: %w",
				module.Code(),
				profile.Code,
				err,
			)
		}

		moduleFilesystems, err := filesystem.NewModuleManager(
			b.factory.services.Filesystems,
			moduleFilesystemBindings(module),
		)
		if err != nil {
			return nil, fmt.Errorf(
				"configure filesystems for module %q in profile %q: %w",
				module.Code(),
				profile.Code,
				err,
			)
		}

		logger := b.factory.services.Logger.With(
			slog.String("profile.code", string(profile.Code)),
			slog.String("module.code", string(module.Code())),
		)
		if scope.SiteID() != "" {
			logger = logger.With(slog.String("site.id", scope.SiteID()))
		}
		moduleContext := newModuleContext(
			b.factory.resolver,
			module.Code(),
			b.factory.applications[module.Code()],
			profile,
			registry,
			scope,
			RuntimeServices{
				Caches:             b.factory.services.Caches,
				Filesystems:        b.factory.services.Filesystems,
				EventBus:           b.factory.services.EventBus,
				Logger:             logger,
				ModuleApplications: b.factory.services.ModuleApplications,
			},
			dependencies,
			moduleCaches,
			moduleFilesystems,
		)

		var hookDependencies []string
		if provider, ok := module.(DependencyProvider); ok {
			for _, code := range provider.Dependencies() {
				hookDependencies = append(hookDependencies, string(code))
			}
		}
		moduleContext.hooks = hooks.ForModule(string(module.Code()), hookDependencies)
		runtime, err := module.Build(ctx, moduleContext)
		if err != nil {
			return nil, fmt.Errorf(
				"build module %q for profile %q: %w",
				module.Code(),
				profile.Code,
				err,
			)
		}
		if runtime == nil || isNilValue(runtime) {
			return nil, fmt.Errorf("module %q returned nil runtime", module.Code())
		}
		if runtime.ModuleCode() != module.Code() {
			return nil, fmt.Errorf(
				"module %q returned runtime for module %q",
				module.Code(),
				runtime.ModuleCode(),
			)
		}
		if err := registry.add(runtime); err != nil {
			return nil, err
		}
		if provider, ok := runtime.(widget.Provider); ok {
			descriptor := ModuleDescriptor{Label: string(module.Code())}
			if provider, ok := module.(ModuleDescriptorProvider); ok {
				descriptor = provider.ModuleDescriptor()
			}
			widgetSources = append(widgetSources, widget.Source{
				Module: widget.ModuleDescriptor{
					Code: string(module.Code()), Label: descriptor.Label,
					Description: descriptor.Description,
				},
				Widgets: provider.Widgets(),
			})
		}
	}

	for _, moduleRuntime := range registry.Modules() {
		finalizer, ok := moduleRuntime.(RuntimeBuildFinalizer)
		if !ok {
			continue
		}
		if err := finalizer.FinalizeRuntimeBuild(ctx); err != nil {
			return nil, fmt.Errorf(
				"finalize module runtime %q for profile %q: %w",
				moduleRuntime.ModuleCode(),
				profile.Code,
				err,
			)
		}
	}

	if err := hooks.Seal(); err != nil {
		return nil, err
	}
	widgets, err := widget.Compile(widgetSources, profile.WidgetViews, registry)
	if err != nil {
		return nil, fmt.Errorf(
			"compile widgets for profile %q: %w",
			profile.Code,
			err,
		)
	}
	templates, err := b.templates.CompileWidgets(widgets)
	if err != nil {
		return nil, fmt.Errorf(
			"compile template widgets for profile %q: %w",
			profile.Code,
			err,
		)
	}
	return &ProfileRuntime{
		hooks:     hooks,
		blueprint: b,
		registry:  registry,
		widgets:   widgets,
		templates: templates,
	}, nil
}

func validateModuleDependencyOrder(profile Profile) error {
	available := make(map[ModuleCode]struct{}, len(profile.Modules))
	for _, module := range profile.Modules {
		provider, ok := module.(DependencyProvider)
		if ok {
			declared := make(map[ModuleCode]struct{})
			for index, dependency := range provider.Dependencies() {
				if dependency == "" {
					return fmt.Errorf(
						"profile %q module %q dependency at index %d has empty code",
						profile.Code,
						module.Code(),
						index,
					)
				}
				if dependency == module.Code() {
					return fmt.Errorf(
						"profile %q module %q declares itself as dependency",
						profile.Code,
						module.Code(),
					)
				}
				if _, exists := declared[dependency]; exists {
					return fmt.Errorf(
						"profile %q module %q declares dependency %q more than once",
						profile.Code,
						module.Code(),
						dependency,
					)
				}
				if _, exists := available[dependency]; !exists {
					return fmt.Errorf(
						"profile %q module %q dependency %q is unavailable; dependencies must be declared earlier in the profile",
						profile.Code,
						module.Code(),
						dependency,
					)
				}
				declared[dependency] = struct{}{}
			}
		}
		available[module.Code()] = struct{}{}
	}
	return nil
}

func resolveModuleDependencies(
	module Module,
	registry Registry,
) (map[ModuleCode]ModuleRuntime, error) {
	provider, ok := module.(DependencyProvider)
	if !ok {
		return nil, nil
	}
	result := make(map[ModuleCode]ModuleRuntime)
	for index, code := range provider.Dependencies() {
		if code == "" {
			return nil, fmt.Errorf("dependency at index %d has empty code", index)
		}
		if code == module.Code() {
			return nil, fmt.Errorf("module declares itself as dependency")
		}
		if _, exists := result[code]; exists {
			return nil, fmt.Errorf("dependency %q is declared more than once", code)
		}
		runtime, exists := registry.Module(code)
		if !exists {
			return nil, fmt.Errorf(
				"dependency %q is unavailable; dependencies must be declared earlier in the profile",
				code,
			)
		}
		result[code] = runtime
	}
	return result, nil
}

func cloneProfile(profile Profile) Profile {
	profile.Modules = append(
		[]Module(nil),
		profile.Modules...,
	)
	profile.Params = field.CloneDefinitions(profile.Params)
	profile.EditorTabs = field.CloneEditorTabs(profile.EditorTabs)
	profile.Templates = template.CloneDefinitions(profile.Templates)
	profile.WidgetViews = widget.CloneViews(profile.WidgetViews)

	return profile
}
