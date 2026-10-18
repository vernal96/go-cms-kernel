package kernel

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/vernal96/go-cms-kernel/permission"
)

// ProfileDefinitions is an immutable profile-scoped definition snapshot. It can
// be reused after binding application services without executing contributions again.
type ProfileDefinitions struct {
	profileCode  ProfileCode
	moduleCodes  []ModuleCode
	dependencies [][]ModuleCode
	registry     *RuntimeRegistry
}

func (d *ProfileDefinitions) Registry() DefinitionRegistry {
	if d == nil {
		return nil
	}
	return d.registry
}

// CompileProfileDefinitions applies definitions before application services or
// site runtimes exist. Owners must finish validation and seal their registries.
func CompileProfileDefinitions(ctx context.Context, profile Profile) (*ProfileDefinitions, error) {
	if ctx == nil {
		return nil, errors.New("profile definition context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	profile = cloneProfile(profile)
	if err := validateProfileModules(profile); err != nil {
		return nil, err
	}
	registry := newRuntimeRegistry()
	positions := make(map[ModuleCode]int, len(profile.Modules))
	declarations := make([]ModuleRegistry, len(profile.Modules))
	for index, module := range profile.Modules {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		positions[module.Code()] = index
		if provider, ok := module.(DefinitionProvider); ok {
			target, err := provider.NewDefinitions(ctx, DefinitionContext{moduleCode: module.Code(), profile: profile, registry: registry})
			if err != nil {
				return nil, fmt.Errorf("profile %q create definitions for module %q: %w", profile.Code, module.Code(), err)
			}
			if target == nil || isNilValue(target) {
				return nil, fmt.Errorf("profile %q module %q returned nil definitions", profile.Code, module.Code())
			}
			if target.ModuleCode() != module.Code() {
				return nil, fmt.Errorf("profile %q module %q returned definitions for module %q", profile.Code, module.Code(), target.ModuleCode())
			}
			registry.definitions[module.Code()] = target
			registry.definitionOrder = append(registry.definitionOrder, module.Code())
			continue
		}
		declaration, err := RegistryForModule(module)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", profile.Code, err)
		}
		declarations[index] = declaration
		// Gather every field and validator before compiling any resource type.
		for itemIndex, fieldType := range declaration.FieldTypes {
			if err := registry.addFieldType(fieldType); err != nil {
				return nil, fmt.Errorf("register field type at index %d from module %q: %w", itemIndex, module.Code(), err)
			}
		}
		for itemIndex, validator := range declaration.ValidatorTypes {
			if err := registry.addValidatorType(validator); err != nil {
				return nil, fmt.Errorf("register validator type at index %d from module %q: %w", itemIndex, module.Code(), err)
			}
		}
		if len(declaration.PermissionEntities) == 0 {
			continue
		}
		permissions, err := permission.Definitions(string(module.Code()), declaration.PermissionEntities)
		if err != nil {
			return nil, fmt.Errorf("register permissions from module %q: %w", module.Code(), err)
		}
		for _, definition := range permissions {
			if err := registry.addPermission(definition); err != nil {
				return nil, fmt.Errorf("register permission from module %q: %w", module.Code(), err)
			}
		}
	}
	type registration struct {
		module       Module
		index        int
		contribution DefinitionContribution
	}
	var registrations []registration
	for _, module := range profile.Modules {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		provider, ok := module.(DefinitionContributionProvider)
		if !ok {
			continue
		}
		for index, contribution := range provider.DefinitionContributions() {
			registrations = append(registrations, registration{module: module, index: index, contribution: contribution})
		}
	}
	for _, entry := range registrations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		contribution := entry.contribution
		if contribution == nil || isNilValue(contribution) {
			return nil, fmt.Errorf("profile %q module %q definition contribution %d is nil", profile.Code, entry.module.Code(), entry.index)
		}
		targetCode := contribution.TargetModule()
		if err := validateContributionTarget(entry.module, targetCode, positions); err != nil {
			return nil, fmt.Errorf("profile %q module %q definition contribution %d: %w", profile.Code, entry.module.Code(), entry.index, err)
		}
		target, exists := registry.definitions[targetCode]
		if !exists {
			return nil, fmt.Errorf("profile %q module %q definition contribution %d target module %q has no definition target", profile.Code, entry.module.Code(), entry.index, targetCode)
		}
		if err := contribution.Apply(ctx, target); err != nil {
			return nil, fmt.Errorf("profile %q module %q definition contribution %d to module %q: %w", profile.Code, entry.module.Code(), entry.index, targetCode, err)
		}
	}

	for _, code := range registry.definitionOrder {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := registry.definitions[code].FinalizeDefinitionBuild(ctx); err != nil {
			return nil, fmt.Errorf("profile %q finalize definitions for module %q: %w", profile.Code, code, err)
		}
	}
	if err := registry.snapshotDefinitionCatalogs(); err != nil {
		return nil, fmt.Errorf("profile %q: %w", profile.Code, err)
	}
	for index, declaration := range declarations {
		for itemIndex, resourceType := range declaration.ResourceTypes {
			if err := registry.addResourceType(resourceType); err != nil {
				return nil, fmt.Errorf("register resource type at index %d from module %q: %w", itemIndex, profile.Modules[index].Code(), err)
			}
		}
	}
	result := &ProfileDefinitions{profileCode: profile.Code, registry: registry}
	for _, module := range profile.Modules {
		result.moduleCodes = append(result.moduleCodes, module.Code())
		var dependencies []ModuleCode
		if provider, ok := module.(DependencyProvider); ok {
			dependencies = slices.Clone(provider.Dependencies())
		}
		result.dependencies = append(result.dependencies, dependencies)
	}
	return result, nil
}

func validateProfileModules(profile Profile) error {
	if profile.Code == "" {
		return errors.New("profile code is empty")
	}
	codes := make(map[ModuleCode]struct{}, len(profile.Modules))
	for index, module := range profile.Modules {
		if module == nil || isNilValue(module) {
			return fmt.Errorf("profile %q module at index %d is nil", profile.Code, index)
		}
		code := module.Code()
		if code == "" {
			return fmt.Errorf("profile %q module at index %d has empty code", profile.Code, index)
		}
		if _, exists := codes[code]; exists {
			return fmt.Errorf("profile %q contains duplicate module %q", profile.Code, code)
		}
		codes[code] = struct{}{}
	}
	return validateModuleDependencyOrder(profile)
}

func (d *ProfileDefinitions) validateProfile(profile Profile) error {
	if d == nil || d.registry == nil {
		return errors.New("profile definitions are unavailable")
	}
	if d.profileCode != profile.Code || len(d.moduleCodes) != len(profile.Modules) {
		return fmt.Errorf("definitions do not belong to profile %q", profile.Code)
	}
	for index, module := range profile.Modules {
		if d.moduleCodes[index] != module.Code() {
			return fmt.Errorf("profile %q definition module order differs at index %d", profile.Code, index)
		}
		var dependencies []ModuleCode
		if provider, ok := module.(DependencyProvider); ok {
			dependencies = provider.Dependencies()
		}
		if !slices.Equal(d.dependencies[index], dependencies) {
			return fmt.Errorf("profile %q module %q definition dependencies differ", profile.Code, module.Code())
		}
	}
	return nil
}
