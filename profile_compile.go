package kernel

import (
	"context"
	"errors"
	"fmt"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/permission"
)

func (f *ProfileRuntimeFactory) Compile(
	ctx context.Context,
	profile Profile,
) (*ProfileBlueprint, error) {
	if ctx == nil {
		return nil, errors.New("profile compile context is nil")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if profile.Code == "" {
		return nil, errors.New("profile code is empty")
	}

	profile = cloneProfile(profile)

	moduleCodes := make(
		map[ModuleCode]struct{},
		len(profile.Modules),
	)

	for index, profileModule := range profile.Modules {
		if profileModule == nil || isNilValue(profileModule) {
			return nil, fmt.Errorf(
				"profile %q module at index %d is nil",
				profile.Code,
				index,
			)
		}

		moduleCode := profileModule.Code()
		if moduleCode == "" {
			return nil, fmt.Errorf(
				"profile %q module at index %d has empty code",
				profile.Code,
				index,
			)
		}

		if _, exists := moduleCodes[moduleCode]; exists {
			return nil, fmt.Errorf(
				"profile %q contains duplicate module %q",
				profile.Code,
				moduleCode,
			)
		}

		moduleCodes[moduleCode] = struct{}{}
	}
	if err := validateModuleDependencyOrder(profile); err != nil {
		return nil, err
	}

	registry := newRuntimeRegistry()

	for _, profileModule := range profile.Modules {
		moduleRegistry, err := RegistryForModule(profileModule)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", profile.Code, err)
		}

		for index, fieldType := range moduleRegistry.FieldTypes {
			if err := registry.addFieldType(fieldType); err != nil {
				return nil, fmt.Errorf(
					"register field type at index %d from module %q: %w",
					index,
					profileModule.Code(),
					err,
				)
			}
		}
		for index, item := range moduleRegistry.ValidatorTypes {
			if err := registry.addValidatorType(item); err != nil {
				return nil, fmt.Errorf("register validator type at index %d from module %q: %w", index, profileModule.Code(), err)
			}
		}
		for index, resourceType := range moduleRegistry.ResourceTypes {
			if err := registry.addResourceType(resourceType); err != nil {
				return nil, fmt.Errorf(
					"register resource type at index %d from module %q: %w",
					index,
					profileModule.Code(),
					err,
				)
			}
		}
		if len(moduleRegistry.PermissionEntities) == 0 {
			continue
		}
		permissionDefinitions, err := permission.Definitions(
			string(profileModule.Code()),
			moduleRegistry.PermissionEntities,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"register permissions from module %q: %w",
				profileModule.Code(),
				err,
			)
		}
		for index, definition := range permissionDefinitions {
			if err := registry.addPermission(definition); err != nil {
				return nil, fmt.Errorf(
					"register permission at index %d from module %q: %w",
					index,
					profileModule.Code(),
					err,
				)
			}
		}
	}

	paramSchema, err := field.Compile(profile.Params, registry)
	if err != nil {
		return nil, fmt.Errorf(
			"compile params for profile %q: %w",
			profile.Code,
			err,
		)
	}
	if err := field.ValidateEditorTabs(profile.Params, profile.EditorTabs); err != nil {
		return nil, fmt.Errorf(
			"compile editor tabs for profile %q: %w",
			profile.Code,
			err,
		)
	}

	templates, err := template.Compile(
		profile.Templates,
		registry,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"compile templates for profile %q: %w",
			profile.Code,
			err,
		)
	}

	for _, module := range profile.Modules {
		moduleCaches, err := cache.NewModuleManager(f.services.Caches, string(profile.Code), string(module.Code()), moduleCacheBindings(module))
		if err != nil {
			return nil, fmt.Errorf("profile %q module %q caches: %w", profile.Code, module.Code(), err)
		}
		moduleFilesystems, err := filesystem.NewModuleManager(f.services.Filesystems, moduleFilesystemBindings(module))
		if err != nil {
			return nil, fmt.Errorf("profile %q module %q filesystems: %w", profile.Code, module.Code(), err)
		}
		validation := ModuleValidationContext{
			context: ModuleContext{
				resolver:    f.resolver,
				moduleCode:  module.Code(),
				application: f.applications[module.Code()],
				profile:     cloneProfile(profile),
				registry:    registry,
				caches:      moduleCaches,
				filesystems: moduleFilesystems,
			},
			disks: f.services.Filesystems,
		}
		if err := module.Validate(ctx, validation); err != nil {
			return nil, fmt.Errorf("profile %q module %q: %w", profile.Code, module.Code(), err)
		}
	}

	return &ProfileBlueprint{
		profile:     profile,
		registry:    registry,
		paramSchema: paramSchema,
		templates:   templates,
		factory:     f,
	}, nil
}
