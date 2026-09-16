package seeds

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/migrations"
)

const DefaultHistoryTable = "schema_seeds"

var sourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type Tag string

type Source struct {
	ID     string
	Tags   []Tag
	Schema string
	FS     fs.FS
	Path   string
}

type Target = migrations.Target
type MigrationStatus = migrations.MigrationStatus

type Plan struct {
	Connection string
	Module     kernel.ModuleCode
	Target     Target
	Source     Source
}

type Status struct {
	Connection        string            `json:"connection"`
	Module            kernel.ModuleCode `json:"module"`
	Source            string            `json:"source"`
	Tags              []Tag             `json:"tags"`
	CurrentVersion    uint              `json:"current_version"`
	HasCurrentVersion bool              `json:"has_current_version"`
	Dirty             bool              `json:"dirty"`
	Seeds             []MigrationStatus `json:"seeds"`
}

type Provider interface {
	SeedSources() []Source
}

type Manager struct{}

func NewManager() *Manager {
	return &Manager{}
}

func HistoryTable(sourceID string) string {
	return DefaultHistoryTable + "_" + sourceID
}

func ValidateSource(source Source) error {
	switch {
	case !sourceIDPattern.MatchString(source.ID):
		return fmt.Errorf(
			"seed source id %q must be a lower-snake identifier",
			source.ID,
		)
	case len(source.Tags) == 0:
		return fmt.Errorf("seed source %q has no tags", source.ID)
	case source.Schema == "":
		return fmt.Errorf("seed source %q schema is empty", source.ID)
	case source.FS == nil:
		return fmt.Errorf("seed source %q filesystem is nil", source.ID)
	case source.Path == "":
		return fmt.Errorf("seed source %q path is empty", source.ID)
	}

	usedTags := make(map[Tag]struct{}, len(source.Tags))
	for _, tag := range source.Tags {
		if tag == "" ||
			string(tag) != strings.TrimSpace(string(tag)) ||
			strings.ContainsRune(string(tag), ',') {
			return fmt.Errorf(
				"seed source %q contains invalid tag %q",
				source.ID,
				tag,
			)
		}
		if _, exists := usedTags[tag]; exists {
			return fmt.Errorf(
				"seed source %q contains duplicate tag %q",
				source.ID,
				tag,
			)
		}
		usedTags[tag] = struct{}{}
	}

	return nil
}

func (m *Manager) Up(ctx context.Context, plan Plan) error {
	manager, migrationPlan, err := migrationManager(plan)
	if err != nil {
		return err
	}

	return manager.Up(ctx, migrationPlan)
}

func (m *Manager) UpAll(ctx context.Context, plans []Plan) error {
	for _, plan := range plans {
		if err := m.Up(ctx, plan); err != nil {
			return fmt.Errorf("seed %s up: %w", planName(plan), err)
		}
	}

	return nil
}

func (m *Manager) Down(
	ctx context.Context,
	plan Plan,
	steps int,
) error {
	manager, migrationPlan, err := migrationManager(plan)
	if err != nil {
		return err
	}

	return manager.Down(ctx, migrationPlan, steps)
}

func (m *Manager) DownAll(
	ctx context.Context,
	plans []Plan,
	steps int,
) error {
	for index := len(plans) - 1; index >= 0; index-- {
		plan := plans[index]

		if err := m.Down(ctx, plan, steps); err != nil {
			return fmt.Errorf("seed %s down: %w", planName(plan), err)
		}
	}

	return nil
}

func (m *Manager) Version(
	ctx context.Context,
	plan Plan,
) (uint, bool, bool, error) {
	manager, migrationPlan, err := migrationManager(plan)
	if err != nil {
		return 0, false, false, err
	}

	return manager.Version(ctx, migrationPlan)
}

func (m *Manager) Force(
	ctx context.Context,
	plan Plan,
	version int,
) error {
	manager, migrationPlan, err := migrationManager(plan)
	if err != nil {
		return err
	}

	return manager.Force(ctx, migrationPlan, version)
}

func (m *Manager) Status(
	ctx context.Context,
	plan Plan,
) (Status, error) {
	manager, migrationPlan, err := migrationManager(plan)
	if err != nil {
		return Status{}, err
	}

	status, err := manager.Status(ctx, migrationPlan)
	if err != nil {
		return Status{}, err
	}

	return Status{
		Connection:        plan.Connection,
		Module:            plan.Module,
		Source:            plan.Source.ID,
		Tags:              append([]Tag(nil), plan.Source.Tags...),
		CurrentVersion:    status.CurrentVersion,
		HasCurrentVersion: status.HasCurrentVersion,
		Dirty:             status.Dirty,
		Seeds:             status.Migrations,
	}, nil
}

func (m *Manager) StatusAll(
	ctx context.Context,
	plans []Plan,
) ([]Status, error) {
	result := make([]Status, 0, len(plans))

	for _, plan := range plans {
		status, err := m.Status(ctx, plan)
		if err != nil {
			return nil, fmt.Errorf(
				"seed status %s: %w",
				planName(plan),
				err,
			)
		}

		result = append(result, status)
	}

	return result, nil
}

func migrationManager(
	plan Plan,
) (*migrations.Manager, migrations.Plan, error) {
	if err := validatePlan(plan); err != nil {
		return nil, migrations.Plan{}, err
	}

	return migrations.NewManagerWithHistoryTable(
			HistoryTable(plan.Source.ID),
		),
		migrations.Plan{
			Connection: plan.Connection,
			Target:     plan.Target,
			Source: migrations.Source{
				ID:     string(plan.Module) + "/" + plan.Source.ID,
				Schema: plan.Source.Schema,
				FS:     plan.Source.FS,
				Path:   plan.Source.Path,
			},
		},
		nil
}

func validatePlan(plan Plan) error {
	switch {
	case plan.Connection == "":
		return errors.New("seed connection is empty")
	case plan.Module == "":
		return errors.New("seed module is empty")
	case plan.Target == nil:
		return errors.New("seed target is nil")
	default:
		return ValidateSource(plan.Source)
	}
}

func planName(plan Plan) string {
	return plan.Connection + "/" + string(plan.Module) + "/" + plan.Source.ID
}
