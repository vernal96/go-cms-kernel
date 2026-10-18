package kernel

import (
	"context"
	"errors"
	"fmt"
)

// ModuleDefinitions is a fresh, module-owned definition target for one profile.
// FinalizeDefinitionBuild must validate and seal all mutable registries it owns.
type ModuleDefinitions interface {
	ModuleCode() ModuleCode
	FinalizeDefinitionBuild(context.Context) error
}

// DefinitionProvider creates a new target on every profile definition build.
type DefinitionProvider interface {
	NewDefinitions(context.Context, DefinitionContext) (ModuleDefinitions, error)
}

// DefinitionCatalogProvider exposes the existing schema compiler's read contract.
// New feature extension contracts belong to their owner, not this catalog.
type DefinitionCatalogProvider interface {
	DefinitionCatalog() DefinitionRegistry
}

type DefinitionContribution interface {
	TargetModule() ModuleCode
	Apply(context.Context, ModuleDefinitions) error
}

type DefinitionContributionProvider interface {
	DefinitionContributions() []DefinitionContribution
}

type RuntimeContribution interface {
	TargetModule() ModuleCode
	Apply(context.Context, ModuleRuntime) error
}

// RuntimeContributionProvider is an optional capability of a site module runtime.
// Contributions may capture services of that runtime and are never shared by sites.
type RuntimeContributionProvider interface {
	RuntimeContributions() []RuntimeContribution
}

// DefinitionContext exposes build data without application or site runtime services.
type DefinitionContext struct {
	moduleCode ModuleCode
	profile    Profile
	registry   DefinitionRegistry
}

func (c DefinitionContext) ModuleCode() ModuleCode       { return c.moduleCode }
func (c DefinitionContext) Profile() Profile             { return cloneProfile(c.profile) }
func (c DefinitionContext) Registry() DefinitionRegistry { return c.registry }

type definitionContribution[T ModuleDefinitions] struct {
	target ModuleCode
	apply  func(context.Context, T) error
}

// DefineFor narrows the target to its owner's explicit registration contract.
func DefineFor[T ModuleDefinitions](target ModuleCode, apply func(context.Context, T) error) DefinitionContribution {
	return definitionContribution[T]{target: target, apply: apply}
}
func (c definitionContribution[T]) TargetModule() ModuleCode { return c.target }
func (c definitionContribution[T]) Apply(ctx context.Context, target ModuleDefinitions) error {
	if c.apply == nil {
		return errors.New("definition contribution callback is nil")
	}
	typed, ok := target.(T)
	if !ok {
		return fmt.Errorf("definition target %q has incompatible type %T", c.target, target)
	}
	return c.apply(ctx, typed)
}

type runtimeContribution[T ModuleRuntime] struct {
	target ModuleCode
	apply  func(context.Context, T) error
}

// ContributeTo narrows a runtime to the extension contract declared by its owner.
func ContributeTo[T ModuleRuntime](target ModuleCode, apply func(context.Context, T) error) RuntimeContribution {
	return runtimeContribution[T]{target: target, apply: apply}
}
func (c runtimeContribution[T]) TargetModule() ModuleCode { return c.target }
func (c runtimeContribution[T]) Apply(ctx context.Context, target ModuleRuntime) error {
	if c.apply == nil {
		return errors.New("runtime contribution callback is nil")
	}
	typed, ok := target.(T)
	if !ok {
		return fmt.Errorf("runtime target %q has incompatible type %T", c.target, target)
	}
	return c.apply(ctx, typed)
}

func validateContributionTarget(source Module, target ModuleCode, available map[ModuleCode]int) error {
	if target == "" {
		return errors.New("contribution target module code is empty")
	}
	targetIndex, exists := available[target]
	if !exists {
		return fmt.Errorf("target module %q is absent from the profile", target)
	}
	if target == source.Code() {
		return nil
	}
	provider, ok := source.(DependencyProvider)
	if ok {
		for _, dependency := range provider.Dependencies() {
			if dependency == target {
				if targetIndex >= available[source.Code()] {
					return fmt.Errorf("target module %q must precede contributor %q", target, source.Code())
				}
				return nil
			}
		}
	}
	return fmt.Errorf("module %q cannot contribute to module %q without a declared dependency", source.Code(), target)
}

func applyRuntimeContributions(ctx context.Context, profile Profile, registry *RuntimeRegistry) error {
	type registration struct {
		module       Module
		index        int
		contribution RuntimeContribution
	}
	positions := make(map[ModuleCode]int, len(profile.Modules))
	var registrations []registration
	for index, module := range profile.Modules {
		positions[module.Code()] = index
	}
	for _, module := range profile.Modules {
		if err := ctx.Err(); err != nil {
			return err
		}
		source, _ := registry.Module(module.Code())
		provider, ok := source.(RuntimeContributionProvider)
		if !ok {
			continue
		}
		for index, contribution := range provider.RuntimeContributions() {
			registrations = append(registrations, registration{module: module, index: index, contribution: contribution})
		}
	}
	for _, entry := range registrations {
		if err := ctx.Err(); err != nil {
			return err
		}
		contribution := entry.contribution
		if contribution == nil || isNilValue(contribution) {
			return fmt.Errorf("profile %q module %q runtime contribution %d is nil", profile.Code, entry.module.Code(), entry.index)
		}
		targetCode := contribution.TargetModule()
		if err := validateContributionTarget(entry.module, targetCode, positions); err != nil {
			return fmt.Errorf("profile %q module %q runtime contribution %d: %w", profile.Code, entry.module.Code(), entry.index, err)
		}
		target, _ := registry.Module(targetCode)
		if err := contribution.Apply(ctx, target); err != nil {
			return fmt.Errorf("profile %q module %q runtime contribution %d to module %q: %w", profile.Code, entry.module.Code(), entry.index, targetCode, err)
		}
	}
	return nil
}

// ModuleDefinitionsFrom reads an owner's sealed definitions through the same
// explicit dependency boundary used for runtime dependencies.
func ModuleDefinitionsFrom[T ModuleDefinitions](environment moduleEnvironment, code ModuleCode) (T, error) {
	var zero T
	moduleContext := environment.moduleContext()
	var source Module
	positions := make(map[ModuleCode]int, len(moduleContext.profile.Modules))
	for index, module := range moduleContext.profile.Modules {
		positions[module.Code()] = index
		if module.Code() == moduleContext.moduleCode {
			source = module
		}
	}
	if source == nil {
		return zero, errors.New("module definition source is unavailable")
	}
	if err := validateContributionTarget(source, code, positions); err != nil {
		return zero, err
	}
	registry, ok := moduleContext.registry.(*RuntimeRegistry)
	if !ok {
		return zero, errors.New("module definitions are unavailable")
	}
	target, exists := registry.definitions[code]
	if !exists {
		return zero, fmt.Errorf("module %q definitions are unavailable", code)
	}
	typed, ok := target.(T)
	if !ok {
		return zero, fmt.Errorf("module %q definitions have incompatible type %T", code, target)
	}
	return typed, nil
}
