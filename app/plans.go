package app

import (
	"fmt"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/seeds"
)

func (a *App) MigrationPlans() []migrations.Plan {
	if a == nil {
		return nil
	}

	return append([]migrations.Plan(nil), a.migrationPlan...)
}

func (a *App) SeedPlans() []seeds.Plan {
	if a == nil {
		return nil
	}

	plans := append([]seeds.Plan(nil), a.seedPlan...)
	for index := range plans {
		plans[index].Source.Tags = append(
			[]seeds.Tag(nil),
			plans[index].Source.Tags...,
		)
	}

	return plans
}

func migrationPlans(
	connector kernel.DBConnector,
	moduleCode kernel.ModuleCode,
	sources []migrations.Source,
	used map[string]struct{},
) ([]migrations.Plan, error) {
	if len(sources) == 0 {
		return nil, nil
	}

	target, ok := connector.(migrations.Target)
	if !ok {
		return nil, fmt.Errorf(
			"database connector %q does not support migrations required by module %q",
			connector.Code(),
			moduleCode,
		)
	}

	plans := make([]migrations.Plan, 0, len(sources))
	for _, source := range sources {
		if err := validateSource(connector.Code(), moduleCode, source.ID, used); err != nil {
			return nil, err
		}

		plans = append(plans, migrations.Plan{
			Connection: string(connector.Code()),
			Target:     target,
			Source:     source,
		})
	}

	return plans, nil
}

func seedPlans(
	connector kernel.DBConnector,
	moduleCode kernel.ModuleCode,
	sources []seeds.Source,
	used map[string]struct{},
	histories map[string]string,
) ([]seeds.Plan, error) {
	if len(sources) == 0 {
		return nil, nil
	}

	target, ok := connector.(seeds.Target)
	if !ok {
		return nil, fmt.Errorf(
			"database connector %q does not support seeds required by module %q",
			connector.Code(),
			moduleCode,
		)
	}

	plans := make([]seeds.Plan, 0, len(sources))
	for _, source := range sources {
		if err := validateSeedSource(
			connector.Code(),
			moduleCode,
			source,
			used,
			histories,
		); err != nil {
			return nil, err
		}

		source.Tags = append([]seeds.Tag(nil), source.Tags...)
		plans = append(plans, seeds.Plan{
			Connection: string(connector.Code()),
			Module:     moduleCode,
			Target:     target,
			Source:     source,
		})
	}

	return plans, nil
}

func validateSeedSource(
	connectionCode kernel.ConnectionCode,
	moduleCode kernel.ModuleCode,
	source seeds.Source,
	used map[string]struct{},
	histories map[string]string,
) error {
	if err := seeds.ValidateSource(source); err != nil {
		return fmt.Errorf(
			"database binding %q module %q: %w",
			connectionCode,
			moduleCode,
			err,
		)
	}

	sourceKey := string(moduleCode) + "/" + source.ID
	if _, exists := used[sourceKey]; exists {
		return fmt.Errorf(
			"database binding %q module %q contains duplicate seed source %q",
			connectionCode,
			moduleCode,
			source.ID,
		)
	}

	historyKey := source.Schema + "/" + seeds.HistoryTable(source.ID)
	if existing, exists := histories[historyKey]; exists {
		return fmt.Errorf(
			"database binding %q seed sources %q and %q share history %q",
			connectionCode,
			existing,
			sourceKey,
			historyKey,
		)
	}

	used[sourceKey] = struct{}{}
	histories[historyKey] = sourceKey
	return nil
}

func validateSource(
	connectionCode kernel.ConnectionCode,
	moduleCode kernel.ModuleCode,
	sourceID string,
	used map[string]struct{},
) error {
	if sourceID != string(moduleCode) {
		return fmt.Errorf(
			"database binding %q module %q returned source %q",
			connectionCode,
			moduleCode,
			sourceID,
		)
	}
	if _, exists := used[sourceID]; exists {
		return fmt.Errorf(
			"database binding %q contains duplicate source %q",
			connectionCode,
			sourceID,
		)
	}

	used[sourceID] = struct{}{}
	return nil
}
