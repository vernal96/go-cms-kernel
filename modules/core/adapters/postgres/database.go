package postgres

import (
	"context"
	"embed"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/security"

	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	hookspostgres "github.com/vernal96/go-cms-kernel/entityhooks/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	accesspostgres "github.com/vernal96/go-cms-kernel/modules/core/access/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	filepostgres "github.com/vernal96/go-cms-kernel/modules/core/file/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	grouppostgres "github.com/vernal96/go-cms-kernel/modules/core/group/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	mediapostgres "github.com/vernal96/go-cms-kernel/modules/core/media/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	resourcepostgres "github.com/vernal96/go-cms-kernel/modules/core/resource/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	sitepostgres "github.com/vernal96/go-cms-kernel/modules/core/site/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	userpostgres "github.com/vernal96/go-cms-kernel/modules/core/user/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/outbox"
	"github.com/vernal96/go-cms-kernel/seeds"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

//go:embed seeds/shared/*.sql
var seedFiles embed.FS

type Database struct {
	connector *connectorpostgres.Connector
	sites     site.Repository
	resources resource.Repository
	files     file.Repository
	media     media.Repository
	users     user.Repository
	groups    group.Repository
	access    access.Repository
	outbox    outbox.Source
}

type DatabaseFactory struct{}

func (DatabaseFactory) ModuleCode() kernel.ModuleCode {
	return core.ModuleCode
}

func (DatabaseFactory) Build(
	connector kernel.DBConnector,
) (kernel.ModuleDatabase, error) {
	postgresConnector, ok := connector.(*connectorpostgres.Connector)
	if !ok {
		return nil, errors.New(
			"core postgres adapter requires *postgres.Connector",
		)
	}

	return NewDatabase(postgresConnector)
}

func NewDatabase(
	connector *connectorpostgres.Connector,
) (*Database, error) {
	if connector == nil {
		return nil, errors.New("postgres connector is nil")
	}

	sites, err := sitepostgres.NewRepository(connector)
	if err != nil {
		return nil, err
	}

	resources, err := resourcepostgres.NewRepository(connector)
	if err != nil {
		return nil, err
	}

	files, err := filepostgres.NewRepository(connector, func(ctx context.Context, tx pgx.Tx, ids []int64, actorID *security.UserID) error {
		if err := resources.ClearMediaReferences(ctx, tx, ids, actorID); err != nil {
			return err
		}
		return userpostgres.ClearMediaReferences(ctx, tx, "core:"+string(connector.Code()), ids, actorID)
	})
	if err != nil {
		return nil, err
	}

	mediaRepository, err := mediapostgres.NewRepository(connector)
	if err != nil {
		return nil, err
	}
	userRepository, err := userpostgres.NewRepository(connector)
	if err != nil {
		return nil, err
	}
	groupRepository, err := grouppostgres.NewRepository(connector)
	if err != nil {
		return nil, err
	}
	accessRepository, err := accesspostgres.NewRepository(connector)
	if err != nil {
		return nil, err
	}

	return &Database{
		connector: connector,
		sites:     sites,
		resources: resources,
		files:     files,
		media:     mediaRepository,
		users:     userRepository,
		groups:    groupRepository,
		access:    accessRepository,
		outbox:    newOutboxSource(connector),
	}, nil
}

func (d *Database) ModuleCode() kernel.ModuleCode {
	return core.ModuleCode
}

func (d *Database) Sites() site.Repository {
	return d.sites
}

func (d *Database) Resources() resource.Repository {
	return d.resources
}

func (d *Database) Files() file.Repository {
	return d.files
}

func (d *Database) Media() media.Repository {
	return d.media
}

func (d *Database) Users() user.Repository {
	return d.users
}

func (d *Database) Groups() group.Repository {
	return d.groups
}

func (d *Database) Access() access.Repository {
	return d.access
}

func (d *Database) OutboxSources() []outbox.Source {
	if d == nil || d.outbox == nil {
		return nil
	}
	return []outbox.Source{d.outbox}
}

func (d *Database) MigrationSources() []migrations.Source {
	return []migrations.Source{
		{
			ID:     string(core.ModuleCode),
			Schema: "core",
			FS:     migrationFiles,
			Path:   "migrations",
		},
	}
}

func (d *Database) SeedSources() []seeds.Source {
	return []seeds.Source{
		{
			ID:     "identity_shared",
			Tags:   []seeds.Tag{"dev", "prod"},
			Schema: "core",
			FS:     seedFiles,
			Path:   "seeds/shared",
		},
	}
}

var _ core.Database = (*Database)(nil)
var _ kernel.ModuleDatabaseFactory = DatabaseFactory{}
var _ migrations.Provider = (*Database)(nil)
var _ seeds.Provider = (*Database)(nil)
var _ outbox.Provider = (*Database)(nil)

func (d *Database) EntityHookSources() []entityhooks.Source {
	return []entityhooks.Source{hookspostgres.NewSource(d.connector.Pool(), "core:"+string(d.connector.Code()), "core")}
}
