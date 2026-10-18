package kernel

import (
	"context"
	"errors"
	"fmt"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
)

func (f *ProfileRuntimeFactory) Compile(
	ctx context.Context,
	profile Profile,
) (*ProfileBlueprint, error) {
	definitions, err := CompileProfileDefinitions(ctx, profile)
	if err != nil {
		return nil, err
	}
	return f.CompileWithDefinitions(ctx, profile, definitions)
}

// CompileWithDefinitions reuses a frozen snapshot after application services
// have been bound; it never executes definition contributions a second time.
func (f *ProfileRuntimeFactory) CompileWithDefinitions(ctx context.Context, profile Profile, definitions *ProfileDefinitions) (*ProfileBlueprint, error) {
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
	if err := validateProfileFileDisks(profile, f.services.Filesystems); err != nil {
		return nil, err
	}

	if err := validateProfileModules(profile); err != nil {
		return nil, err
	}
	if err := definitions.validateProfile(profile); err != nil {
		return nil, err
	}
	registry := definitions.registry

	if image := profile.Settings.FileSystem.ResourceImage; image != nil {
		if image.Multiple {
			return nil, errors.New("resource image must be a single Media value")
		}
		if _, err := field.Compile([]field.Definition{{Key: "image_media_id", Label: "Изображение", Type: field.TypeMedia, Options: *image}}, registry); err != nil {
			return nil, fmt.Errorf("profile %q resource image: %w", profile.Code, err)
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
