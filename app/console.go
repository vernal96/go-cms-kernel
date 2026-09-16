package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/console"
)

func (a *App) CommandProviders() []console.Provider {
	if a == nil {
		return nil
	}

	return append([]console.Provider(nil), a.providers...)
}

func (a *App) collectModuleCommandProviders() {
	for _, profile := range a.definition.Profiles {
		for _, profileModule := range profile.Modules {
			if profileModule.Module == nil {
				continue
			}

			a.addProvider(
				"module:"+string(profileModule.Module.Code()),
				profileModule.Module,
			)
		}
	}
}

func (a *App) addProvider(key string, candidate any) {
	provider, ok := candidate.(console.Provider)
	if !ok {
		return
	}

	for _, registered := range a.providers {
		if providerIdentity(registered) == key {
			return
		}
	}

	a.providers = append(a.providers, keyedProvider{
		key:      key,
		Provider: provider,
	})
}

type keyedProvider struct {
	key string
	console.Provider
}

func providerIdentity(provider console.Provider) string {
	if keyed, ok := provider.(keyedProvider); ok {
		return keyed.key
	}
	return ""
}

type cacheMaintenanceProvider struct {
	manager *cache.Manager
}

func (p cacheMaintenanceProvider) Commands() []console.Command {
	if p.manager == nil {
		return nil
	}
	return []console.Command{cacheMaintenanceCommand{manager: p.manager}}
}

type cacheMaintenanceCommand struct {
	manager *cache.Manager
}

func (cacheMaintenanceCommand) Name() string { return "cache" }

func (cacheMaintenanceCommand) Description() string {
	return "prune or clear application cache stores"
}

func (c cacheMaintenanceCommand) Run(
	ctx context.Context,
	args []string,
	streams console.IO,
) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := fmt.Fprintln(streams.Out, "Usage: console cache <prune|clear>")
		return err
	}
	if len(args) != 1 {
		return errors.New("cache command accepts one subcommand")
	}
	switch args[0] {
	case "prune":
		return c.manager.Prune(ctx)
	case "clear":
		return c.manager.Flush(ctx)
	default:
		return fmt.Errorf("unknown cache subcommand %q", args[0])
	}
}
