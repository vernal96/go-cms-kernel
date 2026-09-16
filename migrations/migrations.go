package migrations

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	migrate "github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	migratesource "github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

const DefaultHistoryTable = "schema_migrations"

type Source struct {
	ID     string
	Schema string
	FS     fs.FS
	Path   string
}

type Provider interface {
	MigrationSources() []Source
}

type Target interface {
	OpenMigrationDriver(
		ctx context.Context,
		schema string,
		historyTable string,
	) (migratedb.Driver, error)
}

type Plan struct {
	Connection string
	Target     Target
	Source     Source
}

// ValidateHistories rejects independent sources sharing one physical history.
// A Manager uses the same history-table name for all its plans.
func ValidateHistories(plans []Plan) error {
	type key struct{ connection, schema string }
	seen := make(map[key]string)
	for _, plan := range plans {
		identity := key{plan.Connection, plan.Source.Schema}
		if previous, exists := seen[identity]; exists {
			return fmt.Errorf("migration sources %q and %q on connection %q share history in schema %q", previous, plan.Source.ID, plan.Connection, plan.Source.Schema)
		}
		seen[identity] = plan.Source.ID
	}
	return nil
}

type MigrationStatus struct {
	Version    uint   `json:"version"`
	Identifier string `json:"identifier"`
	Applied    bool   `json:"applied"`
}

type Status struct {
	Connection        string            `json:"connection"`
	Source            string            `json:"source"`
	CurrentVersion    uint              `json:"current_version"`
	HasCurrentVersion bool              `json:"has_current_version"`
	Dirty             bool              `json:"dirty"`
	Migrations        []MigrationStatus `json:"migrations"`
}

type Manager struct {
	historyTable string
}

func NewManager() *Manager {
	return NewManagerWithHistoryTable(DefaultHistoryTable)
}

func NewManagerWithHistoryTable(historyTable string) *Manager {
	return &Manager{historyTable: historyTable}
}

func (m *Manager) Up(
	ctx context.Context,
	plan Plan,
) error {
	if ctx == nil {
		return errors.New("migration context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validatePlan(plan); err != nil {
		return err
	}
	return m.withInstance(
		ctx,
		plan,
		func(instance *migrate.Migrate) error {
			return runWithContext(ctx, instance, instance.Up)
		},
	)
}

func (m *Manager) UpAll(
	ctx context.Context,
	plans []Plan,
) error {
	if err := ValidateHistories(plans); err != nil {
		return err
	}
	for _, plan := range plans {
		if err := m.Up(ctx, plan); err != nil {
			return fmt.Errorf(
				"migrate %s/%s up: %w",
				plan.Connection,
				plan.Source.ID,
				err,
			)
		}
	}

	return nil
}

func (m *Manager) Down(
	ctx context.Context,
	plan Plan,
	steps int,
) error {
	if steps <= 0 {
		return errors.New("migration down steps must be positive")
	}

	return m.withInstance(
		ctx,
		plan,
		func(instance *migrate.Migrate) error {
			return runWithContext(
				ctx,
				instance,
				func() error {
					return instance.Steps(-steps)
				},
			)
		},
	)
}

// DownAll rolls modules back in the reverse order of their application.
func (m *Manager) DownAll(
	ctx context.Context,
	plans []Plan,
	steps int,
) error {
	if err := ValidateHistories(plans); err != nil {
		return err
	}
	for index := len(plans) - 1; index >= 0; index-- {
		plan := plans[index]

		if err := m.Down(ctx, plan, steps); err != nil {
			return fmt.Errorf(
				"migrate %s/%s down: %w",
				plan.Connection,
				plan.Source.ID,
				err,
			)
		}
	}

	return nil
}

func (m *Manager) Version(
	ctx context.Context,
	plan Plan,
) (
	version uint,
	hasVersion bool,
	dirty bool,
	err error,
) {
	err = m.withInstance(
		ctx,
		plan,
		func(instance *migrate.Migrate) error {
			version, dirty, err = instance.Version()

			if errors.Is(err, migrate.ErrNilVersion) {
				version = 0
				dirty = false
				hasVersion = false
				return nil
			}

			hasVersion = err == nil
			return err
		},
	)

	return
}

func (m *Manager) Force(
	ctx context.Context,
	plan Plan,
	version int,
) error {
	if version < -1 {
		return errors.New(
			"forced migration version cannot be less than -1",
		)
	}

	return m.withInstance(
		ctx,
		plan,
		func(instance *migrate.Migrate) error {
			if err := ctx.Err(); err != nil {
				return err
			}

			return instance.Force(version)
		},
	)
}

func (m *Manager) Status(
	ctx context.Context,
	plan Plan,
) (_ Status, err error) {
	status := Status{
		Connection: plan.Connection,
		Source:     plan.Source.ID,
	}

	status.Migrations, err = migrationFiles(plan.Source)
	if err != nil {
		return Status{}, err
	}

	version, hasVersion, dirty, err := m.Version(ctx, plan)
	if err != nil {
		return Status{}, err
	}

	status.CurrentVersion = version
	status.HasCurrentVersion = hasVersion
	status.Dirty = dirty

	for index := range status.Migrations {
		status.Migrations[index].Applied =
			hasVersion &&
				status.Migrations[index].Version <= version
	}

	return status, nil
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
				"migration status %s/%s: %w",
				plan.Connection,
				plan.Source.ID,
				err,
			)
		}

		result = append(result, status)
	}

	return result, nil
}

func (m *Manager) withInstance(
	ctx context.Context,
	plan Plan,
	action func(*migrate.Migrate) error,
) (resultErr error) {
	if ctx == nil {
		return errors.New("migration context is nil")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := validatePlan(plan); err != nil {
		return err
	}

	sourceDriver, err := iofs.New(
		plan.Source.FS,
		plan.Source.Path,
	)
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}

	historyTable := m.historyTable
	if historyTable == "" {
		historyTable = DefaultHistoryTable
	}

	databaseDriver, err := plan.Target.OpenMigrationDriver(
		ctx,
		plan.Source.Schema,
		historyTable,
	)
	if err != nil {
		_ = sourceDriver.Close()
		return fmt.Errorf("create migration target: %w", err)
	}

	instance, err := migrate.NewWithInstance(
		"iofs",
		sourceDriver,
		plan.Connection+"/"+plan.Source.ID,
		databaseDriver,
	)
	if err != nil {
		_ = sourceDriver.Close()
		_ = databaseDriver.Close()
		return fmt.Errorf("create migration manager: %w", err)
	}

	defer func() {
		sourceErr, databaseErr := instance.Close()

		resultErr = errors.Join(
			resultErr,
			sourceErr,
			databaseErr,
		)
	}()

	resultErr = action(instance)

	if errors.Is(resultErr, migrate.ErrNoChange) {
		resultErr = nil
	}

	return resultErr
}

func runWithContext(
	ctx context.Context,
	instance *migrate.Migrate,
	action func() error,
) error {
	result := make(chan error, 1)

	go func() {
		result <- action()
	}()

	select {
	case err := <-result:
		return err

	case <-ctx.Done():
		select {
		case instance.GracefulStop <- true:
		default:
		}

		<-result
		return ctx.Err()
	}
}

func migrationFiles(source Source) ([]MigrationStatus, error) {
	entries, err := fs.ReadDir(source.FS, source.Path)
	if err != nil {
		return nil, err
	}

	type pair struct {
		up         bool
		down       bool
		identifier string
	}

	versions := make(map[uint]*pair)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		migration, err := migratesource.DefaultParse(entry.Name())
		if err != nil {
			continue
		}

		current, exists := versions[migration.Version]
		if !exists {
			current = &pair{
				identifier: migration.Identifier,
			}
			versions[migration.Version] = current
		}

		switch migration.Direction {
		case migratesource.Up:
			if current.up {
				return nil, fmt.Errorf(
					"migration %d has more than one up file",
					migration.Version,
				)
			}
			current.up = true
		case migratesource.Down:
			if current.down {
				return nil, fmt.Errorf(
					"migration %d has more than one down file",
					migration.Version,
				)
			}
			current.down = true
		}

		if current.identifier != migration.Identifier {
			return nil, fmt.Errorf(
				"migration %d up/down identifiers do not match",
				migration.Version,
			)
		}
	}

	result := make([]MigrationStatus, 0, len(versions))

	for version, pair := range versions {
		if !pair.up || !pair.down {
			return nil, fmt.Errorf(
				"migration %d must have both up and down files",
				version,
			)
		}

		result = append(result, MigrationStatus{
			Version:    version,
			Identifier: pair.identifier,
		})
	}

	sort.Slice(result, func(left, right int) bool {
		return result[left].Version < result[right].Version
	})

	return result, nil
}

func validatePlan(plan Plan) error {
	switch {
	case plan.Connection == "":
		return errors.New("migration connection is empty")
	case plan.Target == nil:
		return errors.New("migration target is nil")
	case plan.Source.ID == "":
		return errors.New("migration source id is empty")
	case plan.Source.Schema == "":
		return errors.New("migration schema is empty")
	case plan.Source.FS == nil:
		return errors.New("migration filesystem is nil")
	case plan.Source.Path == "":
		return errors.New("migration path is empty")
	default:
		files, err := migrationFiles(plan.Source)
		if err != nil {
			return err
		}

		if len(files) == 0 {
			return errors.New("migration source has no up/down files")
		}

		return nil
	}
}
