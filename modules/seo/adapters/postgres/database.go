package postgres

import (
	"embed"
	"errors"

	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/seo"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Database struct {
	metadata seo.Repository
}

type DatabaseFactory struct{}

func (DatabaseFactory) ModuleCode() kernel.ModuleCode { return seo.ModuleCode }

func (DatabaseFactory) Build(
	connector kernel.DBConnector,
) (kernel.ModuleDatabase, error) {
	postgresConnector, ok := connector.(*connectorpostgres.Connector)
	if !ok {
		return nil, errors.New("SEO postgres adapter requires *postgres.Connector")
	}
	return NewDatabase(postgresConnector)
}

func NewDatabase(connector *connectorpostgres.Connector) (*Database, error) {
	repository, err := NewRepository(connector)
	if err != nil {
		return nil, err
	}
	return &Database{metadata: repository}, nil
}

func (*Database) ModuleCode() kernel.ModuleCode { return seo.ModuleCode }

func (d *Database) ResourceMetadata() seo.Repository { return d.metadata }

func (*Database) MigrationSources() []migrations.Source {
	return []migrations.Source{{
		ID:     string(seo.ModuleCode),
		Schema: "seo",
		FS:     migrationFiles,
		Path:   "migrations",
	}}
}

var _ seo.Database = (*Database)(nil)
var _ kernel.ModuleDatabaseFactory = DatabaseFactory{}
var _ migrations.Provider = (*Database)(nil)
