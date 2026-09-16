package kernel

import (
	"context"
	"fmt"
	"sort"

	"github.com/vernal96/go-cms-kernel/entityhooks"
)

// BuildApplicationEntityHooks builds one global registry, never one per site.
// Application contributors declare dependencies through DependencyProvider.
func BuildApplicationEntityHooks(ctx context.Context, applications []ModuleApplication, knownModules []ModuleCode) (*entityhooks.Registry, error) {
	registry := entityhooks.NewRegistry(entityhooks.Application, "")
	known := map[ModuleCode]bool{}
	for _, code := range knownModules {
		known[code] = true
	}
	items := map[ModuleCode]ModuleApplication{}
	for _, application := range applications {
		if application == nil || isNilValue(application) || application.ModuleCode() == "" {
			return nil, fmt.Errorf("invalid module application")
		}
		code := application.ModuleCode()
		if _, exists := items[code]; exists {
			return nil, fmt.Errorf("duplicate module application %q", code)
		}
		items[code] = application
		known[code] = true
	}
	codes := make([]string, 0, len(items))
	for code := range items {
		codes = append(codes, string(code))
	}
	sort.Strings(codes)
	state := map[ModuleCode]int{}
	var build func(ModuleCode) error
	build = func(code ModuleCode) error {
		if state[code] == 2 {
			return nil
		}
		if state[code] == 1 {
			return fmt.Errorf("cyclic application hook dependency %q", code)
		}
		state[code] = 1
		application := items[code]
		var dependencies []string
		if provider, ok := application.(DependencyProvider); ok {
			seen := map[ModuleCode]bool{}
			for _, dependency := range provider.Dependencies() {
				if dependency == code || !known[dependency] || seen[dependency] {
					return fmt.Errorf("invalid application hook dependency %q for %q", dependency, code)
				}
				seen[dependency] = true
				dependencies = append(dependencies, string(dependency))
				if _, exists := items[dependency]; exists {
					if err := build(dependency); err != nil {
						return err
					}
				}
			}
		}
		if provider, ok := application.(entityhooks.ApplicationProvider); ok {
			if err := provider.RegisterEntityHooks(ctx, registry.ForModule(string(code), dependencies)); err != nil {
				return fmt.Errorf("register application hooks %q: %w", code, err)
			}
		}
		state[code] = 2
		return nil
	}
	for _, code := range codes {
		if err := build(ModuleCode(code)); err != nil {
			return nil, err
		}
	}
	if err := registry.Seal(); err != nil {
		return nil, err
	}
	return registry, nil
}
