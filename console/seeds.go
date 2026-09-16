package console

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/vernal96/go-cms-kernel/seeds"
)

type seedManager interface {
	UpAll(context.Context, []seeds.Plan) error
	Down(context.Context, seeds.Plan, int) error
	Force(context.Context, seeds.Plan, int) error
	StatusAll(context.Context, []seeds.Plan) ([]seeds.Status, error)
}

type seedsCommand struct {
	plans   func() []seeds.Plan
	manager seedManager
}

type seedSelection struct {
	connection string
	module     string
	source     string
	tags       []seeds.Tag
	hasTags    bool
}

func newSeedsCommand(application Application) Command {
	return &seedsCommand{
		plans:   application.SeedPlans,
		manager: seeds.NewManager(),
	}
}

func (*seedsCommand) Name() string {
	return "seeds"
}

func (*seedsCommand) Description() string {
	return "manage tagged versioned database seeds"
}

func (c *seedsCommand) Run(
	ctx context.Context,
	args []string,
	streams IO,
) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := fmt.Fprintln(
			streams.Out,
			"Usage: console seeds <up|down|status|version|force> [flags]",
		)
		return err
	}

	switch args[0] {
	case "up":
		plans, err := c.parseSelection("up", args[1:], streams, true)
		if err != nil {
			return err
		}
		if err := c.manager.UpAll(ctx, plans); err != nil {
			return err
		}

		return writeJSON(streams, map[string]any{
			"command": "seeds up",
			"plans":   seedPlanNames(plans),
		})

	case "status":
		plans, err := c.parseSelection(
			"status",
			args[1:],
			streams,
			false,
		)
		if err != nil {
			return err
		}

		statuses, err := c.manager.StatusAll(ctx, plans)
		if err != nil {
			return err
		}

		return writeJSON(streams, statuses)

	case "version":
		plans, err := c.parseSelection(
			"version",
			args[1:],
			streams,
			false,
		)
		if err != nil {
			return err
		}

		statuses, err := c.manager.StatusAll(ctx, plans)
		if err != nil {
			return err
		}

		type versionResult struct {
			Connection        string      `json:"connection"`
			Module            string      `json:"module"`
			Source            string      `json:"source"`
			Tags              []seeds.Tag `json:"tags"`
			Version           uint        `json:"version"`
			HasCurrentVersion bool        `json:"has_current_version"`
			Dirty             bool        `json:"dirty"`
		}

		result := make([]versionResult, 0, len(statuses))
		for _, status := range statuses {
			result = append(result, versionResult{
				Connection:        status.Connection,
				Module:            string(status.Module),
				Source:            status.Source,
				Tags:              status.Tags,
				Version:           status.CurrentVersion,
				HasCurrentVersion: status.HasCurrentVersion,
				Dirty:             status.Dirty,
			})
		}

		return writeJSON(streams, result)

	case "down":
		return c.runDown(ctx, args[1:], streams)

	case "force":
		return c.runForce(ctx, args[1:], streams)

	default:
		return fmt.Errorf("unknown seeds subcommand %q", args[0])
	}
}

func (c *seedsCommand) parseSelection(
	subcommand string,
	args []string,
	streams IO,
	requireTags bool,
) ([]seeds.Plan, error) {
	flags := flag.NewFlagSet("seeds "+subcommand, flag.ContinueOnError)
	flags.SetOutput(streams.Err)
	connection := flags.String("connection", "", "optional connection code")
	module := flags.String("module", "", "optional module code")
	source := flags.String("source", "", "optional seed source id")
	rawTags := flags.String("tags", "", "comma-separated seed tags")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}

	hasTags := flagWasSet(flags, "tags")
	tags, err := parseSeedTags(*rawTags, hasTags)
	if err != nil {
		return nil, err
	}
	if requireTags && !hasTags {
		return nil, errors.New("seeds up requires -tags")
	}

	return selectSeedPlans(c.plans(), seedSelection{
		connection: *connection,
		module:     *module,
		source:     *source,
		tags:       tags,
		hasTags:    hasTags,
	})
}

func (c *seedsCommand) runDown(
	ctx context.Context,
	args []string,
	streams IO,
) error {
	flags := flag.NewFlagSet("seeds down", flag.ContinueOnError)
	flags.SetOutput(streams.Err)
	connection := flags.String("connection", "", "connection code")
	module := flags.String("module", "", "module code")
	source := flags.String("source", "", "seed source id")
	rawTags := flags.String("tags", "", "comma-separated seed tags")
	steps := flags.Int("steps", 1, "number of versions to roll back")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *connection == "" || *module == "" || *source == "" {
		return errors.New(
			"seeds down requires -connection, -module and -source",
		)
	}

	plans, err := c.selectExact(
		flags,
		*connection,
		*module,
		*source,
		*rawTags,
	)
	if err != nil {
		return err
	}
	if err := c.manager.Down(ctx, plans[0], *steps); err != nil {
		return err
	}

	return writeJSON(streams, map[string]any{
		"command": "seeds down",
		"plan":    seedPlanNames(plans)[0],
		"steps":   *steps,
	})
}

func (c *seedsCommand) runForce(
	ctx context.Context,
	args []string,
	streams IO,
) error {
	flags := flag.NewFlagSet("seeds force", flag.ContinueOnError)
	flags.SetOutput(streams.Err)
	connection := flags.String("connection", "", "connection code")
	module := flags.String("module", "", "module code")
	source := flags.String("source", "", "seed source id")
	rawTags := flags.String("tags", "", "comma-separated seed tags")
	version := flags.String("version", "", "version to force, or -1")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *connection == "" ||
		*module == "" ||
		*source == "" ||
		*version == "" {
		return errors.New(
			"seeds force requires -connection, -module, -source and -version",
		)
	}

	forcedVersion, err := strconv.Atoi(*version)
	if err != nil {
		return fmt.Errorf("parse force version: %w", err)
	}

	plans, err := c.selectExact(
		flags,
		*connection,
		*module,
		*source,
		*rawTags,
	)
	if err != nil {
		return err
	}
	if err := c.manager.Force(ctx, plans[0], forcedVersion); err != nil {
		return err
	}

	return writeJSON(streams, map[string]any{
		"command": "seeds force",
		"plan":    seedPlanNames(plans)[0],
		"version": forcedVersion,
	})
}

func (c *seedsCommand) selectExact(
	flags *flag.FlagSet,
	connection string,
	module string,
	source string,
	rawTags string,
) ([]seeds.Plan, error) {
	hasTags := flagWasSet(flags, "tags")
	tags, err := parseSeedTags(rawTags, hasTags)
	if err != nil {
		return nil, err
	}

	return selectSeedPlans(c.plans(), seedSelection{
		connection: connection,
		module:     module,
		source:     source,
		tags:       tags,
		hasTags:    hasTags,
	})
}

func parseSeedTags(
	value string,
	provided bool,
) ([]seeds.Tag, error) {
	if !provided {
		return nil, nil
	}

	parts := strings.Split(value, ",")
	result := make([]seeds.Tag, 0, len(parts))
	used := make(map[seeds.Tag]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New(
				"seed tags must be a comma-separated list of non-empty values",
			)
		}

		tag := seeds.Tag(part)
		if _, exists := used[tag]; exists {
			continue
		}
		used[tag] = struct{}{}
		result = append(result, tag)
	}

	return result, nil
}

func selectSeedPlans(
	plans []seeds.Plan,
	selection seedSelection,
) ([]seeds.Plan, error) {
	selected := make([]seeds.Plan, 0, len(plans))
	for _, plan := range plans {
		if selection.connection != "" &&
			plan.Connection != selection.connection {
			continue
		}
		if selection.module != "" &&
			string(plan.Module) != selection.module {
			continue
		}
		if selection.source != "" &&
			plan.Source.ID != selection.source {
			continue
		}
		if selection.hasTags &&
			!hasAnySeedTag(plan.Source.Tags, selection.tags) {
			continue
		}

		selected = append(selected, plan)
	}

	if len(selected) > 0 {
		return selected, nil
	}
	if len(plans) == 0 &&
		selection.connection == "" &&
		selection.module == "" &&
		selection.source == "" &&
		!selection.hasTags {
		return selected, nil
	}

	return nil, fmt.Errorf(
		"no seed plans match connection=%q module=%q source=%q tags=%q",
		selection.connection,
		selection.module,
		selection.source,
		seedTagStrings(selection.tags),
	)
}

func hasAnySeedTag(source []seeds.Tag, selected []seeds.Tag) bool {
	for _, sourceTag := range source {
		for _, selectedTag := range selected {
			if sourceTag == selectedTag {
				return true
			}
		}
	}

	return false
}

func seedPlanNames(plans []seeds.Plan) []string {
	result := make([]string, 0, len(plans))
	for _, plan := range plans {
		result = append(
			result,
			plan.Connection+"/"+string(plan.Module)+"/"+plan.Source.ID,
		)
	}
	return result
}

func seedTagStrings(tags []seeds.Tag) []string {
	result := make([]string, len(tags))
	for index, tag := range tags {
		result[index] = string(tag)
	}
	return result
}

func flagWasSet(flags *flag.FlagSet, name string) bool {
	found := false
	flags.Visit(func(current *flag.Flag) {
		if current.Name == name {
			found = true
		}
	})
	return found
}
