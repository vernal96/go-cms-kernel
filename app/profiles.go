package app

import (
	"fmt"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/permission"
)

func validateApplicationProfile(profile kernel.Profile) error {
	moduleCodes := make(map[kernel.ModuleCode]struct{}, len(profile.Modules))
	for moduleIndex, profileModule := range profile.Modules {
		if profileModule == nil || nilInterface(profileModule) {
			return fmt.Errorf(
				"profile %q module at index %d is nil",
				profile.Code,
				moduleIndex,
			)
		}
		moduleCode := profileModule.Code()
		if moduleCode == "" {
			return fmt.Errorf(
				"profile %q module at index %d has empty code",
				profile.Code,
				moduleIndex,
			)
		}
		if _, exists := moduleCodes[moduleCode]; exists {
			return fmt.Errorf(
				"profile %q contains duplicate module %q",
				profile.Code,
				moduleCode,
			)
		}
		moduleCodes[moduleCode] = struct{}{}
	}
	for _, required := range requiredModuleCodes {
		if _, exists := moduleCodes[required]; !exists {
			return fmt.Errorf(
				"profile %q does not contain required module %q",
				profile.Code,
				required,
			)
		}
	}
	if profile.Modules[0].Code() != core.ModuleCode {
		return fmt.Errorf(
			"profile %q must declare module %q first",
			profile.Code,
			core.ModuleCode,
		)
	}
	return nil
}

func buildPermissionCatalog(profiles []*kernel.ProfileDefinitions) (*permission.Catalog, error) {
	seenPermissions := make(map[permission.Code]permission.Definition)
	definitions := make([]permission.Definition, 0)
	for _, profile := range profiles {
		if profile == nil || profile.Registry() == nil {
			return nil, fmt.Errorf("profile definitions are unavailable for permission catalog")
		}
		registry := profile.Registry()
		for _, code := range registry.Permissions() {
			definition, exists := registry.Permission(code)
			if !exists {
				return nil, fmt.Errorf("permission %q is unavailable in profile definitions", code)
			}
			if previous, exists := seenPermissions[code]; exists {
				if previous != definition {
					return nil, fmt.Errorf("permission %q has inconsistent declarations across profiles", code)
				}
				continue
			}
			seenPermissions[code] = definition
			definitions = append(definitions, definition)
		}
	}
	return permission.NewCatalog(definitions)
}

type profileResolver map[kernel.ProfileCode]*kernel.ProfileBlueprint

func (r profileResolver) ProfileBlueprint(
	code kernel.ProfileCode,
) (*kernel.ProfileBlueprint, bool) {
	blueprint, exists := r[code]
	return blueprint, exists
}
