package app

import (
	"context"
	"fmt"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/outbox"
	"github.com/vernal96/go-cms-kernel/seeds"
)

func (a *App) MainModuleDatabase(
	moduleCode kernel.ModuleCode,
) (kernel.ModuleDatabase, bool) {
	if a == nil || a.main == nil || a.closed.Load() {
		return nil, false
	}

	database, exists := a.main.adapters[moduleCode]
	return database, exists
}

func (a *App) ModuleDatabase(
	connectionCode kernel.ConnectionCode,
	moduleCode kernel.ModuleCode,
) (kernel.ModuleDatabase, bool) {
	if a == nil || a.closed.Load() {
		return nil, false
	}

	if connectionCode == "" || connectionCode == a.main.connector.Code() {
		return a.MainModuleDatabase(moduleCode)
	}

	binding, exists := a.additional[connectionCode]
	if !exists {
		return nil, false
	}

	database, exists := binding.adapters[moduleCode]
	return database, exists
}

func (a *App) openBinding(
	ctx context.Context,
	definition DatabaseDefinition,
) (*bindingRuntime, error) {
	connector, err := definition.Connector.Open(ctx)
	if connector != nil {
		a.connectors = append(a.connectors, connector)
	}
	if err != nil {
		return nil, fmt.Errorf(
			"open database connector %q: %w",
			definition.Connector.Code(),
			err,
		)
	}
	if connector == nil {
		return nil, fmt.Errorf(
			"database connector factory %q returned nil connector",
			definition.Connector.Code(),
		)
	}
	if connector.Code() != definition.Connector.Code() {
		return nil, fmt.Errorf(
			"database connector factory %q returned connector %q",
			definition.Connector.Code(),
			connector.Code(),
		)
	}
	if err := connector.Ping(ctx); err != nil {
		return nil, fmt.Errorf(
			"ping database connector %q: %w",
			connector.Code(),
			err,
		)
	}

	a.addProvider("connector:"+string(connector.Code()), connector)

	binding := &bindingRuntime{
		connector: connector,
		adapters:  make(map[kernel.ModuleCode]kernel.ModuleDatabase),
	}
	migrationSourceIDs := make(map[string]struct{})
	seedSourceIDs := make(map[string]struct{})
	seedHistories := make(map[string]string)

	for _, factory := range definition.Adapters {
		database, err := factory.Build(connector)
		if err != nil {
			return nil, fmt.Errorf(
				"build database adapter %q on connection %q: %w",
				factory.ModuleCode(),
				connector.Code(),
				err,
			)
		}
		if database == nil {
			return nil, fmt.Errorf(
				"database adapter factory %q returned nil",
				factory.ModuleCode(),
			)
		}
		if database.ModuleCode() != factory.ModuleCode() {
			return nil, fmt.Errorf(
				"database adapter factory %q returned adapter %q",
				factory.ModuleCode(),
				database.ModuleCode(),
			)
		}

		moduleCode := database.ModuleCode()
		binding.adapters[moduleCode] = database
		a.addProvider("database:"+string(moduleCode), database)

		if provider, ok := database.(migrations.Provider); ok {
			plans, err := migrationPlans(
				connector,
				moduleCode,
				provider.MigrationSources(),
				migrationSourceIDs,
			)
			if err != nil {
				return nil, err
			}
			a.migrationPlan = append(a.migrationPlan, plans...)
		}

		if provider, ok := database.(seeds.Provider); ok {
			plans, err := seedPlans(
				connector,
				moduleCode,
				provider.SeedSources(),
				seedSourceIDs,
				seedHistories,
			)
			if err != nil {
				return nil, err
			}
			a.seedPlan = append(a.seedPlan, plans...)
		}
		if provider, ok := database.(entityhooks.Provider); ok {
			a.hookSources = append(a.hookSources, provider.EntityHookSources()...)
		}
		if provider, ok := database.(outbox.Provider); ok {
			a.outboxSources = append(a.outboxSources, provider.OutboxSources()...)
		}
	}

	for _, source := range definition.Seeds {
		plans, err := seedPlans(connector, source.Module, []seeds.Source{source.Source}, seedSourceIDs, seedHistories)
		if err != nil {
			return nil, err
		}
		a.seedPlan = append(a.seedPlan, plans...)
	}
	if err := migrations.ValidateHistories(a.migrationPlan); err != nil {
		return nil, err
	}
	return binding, nil
}
