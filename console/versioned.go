package console

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strconv"

	"github.com/vernal96/go-cms-kernel/migrations"
)

type versionedManager interface {
	UpAll(context.Context, []migrations.Plan) error
	Down(context.Context, migrations.Plan, int) error
	Force(context.Context, migrations.Plan, int) error
	StatusAll(context.Context, []migrations.Plan) ([]migrations.Status, error)
}

type versionedCommand struct {
	name        string
	description string
	plans       func() []migrations.Plan
	manager     versionedManager
}

func newMigrationsCommand(application Application) Command {
	return &versionedCommand{
		name:        "migrations",
		description: "manage database schema migrations",
		plans:       application.MigrationPlans,
		manager:     migrations.NewManager(),
	}
}

func (c *versionedCommand) Name() string {
	return c.name
}

func (c *versionedCommand) Description() string {
	return c.description
}

func (c *versionedCommand) Run(
	ctx context.Context,
	args []string,
	streams IO,
) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := fmt.Fprintf(
			streams.Out,
			"Usage: console %s <up|down|status|version|force> [flags]\n",
			c.name,
		)
		return err
	}

	switch args[0] {
	case "up":
		plans, err := c.parseSelection("up", args[1:], streams, false)
		if err != nil {
			return err
		}

		if err := c.manager.UpAll(ctx, plans); err != nil {
			return err
		}

		return writeJSON(streams, map[string]any{
			"command": c.name + " up",
			"plans":   planNames(plans),
		})

	case "status":
		plans, err := c.parseSelection("status", args[1:], streams, false)
		if err != nil {
			return err
		}

		statuses, err := c.manager.StatusAll(ctx, plans)
		if err != nil {
			return err
		}

		return writeJSON(streams, statuses)

	case "version":
		plans, err := c.parseSelection("version", args[1:], streams, false)
		if err != nil {
			return err
		}

		statuses, err := c.manager.StatusAll(ctx, plans)
		if err != nil {
			return err
		}

		type versionResult struct {
			Connection        string `json:"connection"`
			Module            string `json:"module"`
			Version           uint   `json:"version"`
			HasCurrentVersion bool   `json:"has_current_version"`
			Dirty             bool   `json:"dirty"`
		}

		result := make([]versionResult, 0, len(statuses))
		for _, status := range statuses {
			result = append(result, versionResult{
				Connection:        status.Connection,
				Module:            status.Source,
				Version:           status.CurrentVersion,
				HasCurrentVersion: status.HasCurrentVersion,
				Dirty:             status.Dirty,
			})
		}

		return writeJSON(streams, result)

	case "down":
		flags := flag.NewFlagSet(c.name+" down", flag.ContinueOnError)
		flags.SetOutput(streams.Err)
		connection := flags.String("connection", "", "connection code")
		module := flags.String("module", "", "module code")
		steps := flags.Int("steps", 1, "number of versions to roll back")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected arguments: %v", flags.Args())
		}
		if *connection == "" || *module == "" {
			return errors.New("down requires -connection and -module")
		}

		plans, err := selectPlans(c.plans(), *connection, *module)
		if err != nil {
			return err
		}
		if err := c.manager.Down(ctx, plans[0], *steps); err != nil {
			return err
		}

		return writeJSON(streams, map[string]any{
			"command": c.name + " down",
			"plan":    planNames(plans)[0],
			"steps":   *steps,
		})

	case "force":
		flags := flag.NewFlagSet(c.name+" force", flag.ContinueOnError)
		flags.SetOutput(streams.Err)
		connection := flags.String("connection", "", "connection code")
		module := flags.String("module", "", "module code")
		version := flags.String("version", "", "version to force, or -1")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected arguments: %v", flags.Args())
		}
		if *connection == "" || *module == "" || *version == "" {
			return errors.New(
				"force requires -connection, -module and -version",
			)
		}

		forcedVersion, err := strconv.Atoi(*version)
		if err != nil {
			return fmt.Errorf("parse force version: %w", err)
		}

		plans, err := selectPlans(c.plans(), *connection, *module)
		if err != nil {
			return err
		}
		if err := c.manager.Force(ctx, plans[0], forcedVersion); err != nil {
			return err
		}

		return writeJSON(streams, map[string]any{
			"command": c.name + " force",
			"plan":    planNames(plans)[0],
			"version": forcedVersion,
		})

	default:
		return fmt.Errorf("unknown %s subcommand %q", c.name, args[0])
	}
}

func (c *versionedCommand) parseSelection(
	subcommand string,
	args []string,
	streams IO,
	requireBoth bool,
) ([]migrations.Plan, error) {
	flags := flag.NewFlagSet(c.name+" "+subcommand, flag.ContinueOnError)
	flags.SetOutput(streams.Err)
	connection := flags.String("connection", "", "optional connection code")
	module := flags.String("module", "", "optional module code")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if requireBoth && (*connection == "" || *module == "") {
		return nil, fmt.Errorf(
			"%s requires -connection and -module",
			subcommand,
		)
	}

	return selectPlans(c.plans(), *connection, *module)
}

func selectPlans(
	plans []migrations.Plan,
	connection string,
	module string,
) ([]migrations.Plan, error) {
	selected := make([]migrations.Plan, 0, len(plans))
	for _, plan := range plans {
		if connection != "" && plan.Connection != connection {
			continue
		}
		if module != "" && plan.Source.ID != module {
			continue
		}
		selected = append(selected, plan)
	}

	if len(selected) == 0 {
		if len(plans) == 0 && connection == "" && module == "" {
			return selected, nil
		}

		return nil, fmt.Errorf(
			"no plans match connection=%q module=%q",
			connection,
			module,
		)
	}

	return selected, nil
}

func planNames(plans []migrations.Plan) []string {
	result := make([]string, 0, len(plans))
	for _, plan := range plans {
		result = append(result, plan.Connection+"/"+plan.Source.ID)
	}
	return result
}

func writeJSON(streams IO, value any) error {
	encoder := json.NewEncoder(streams.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
