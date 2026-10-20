package postgres

import (
	"embed"
	"errors"

	"github.com/vernal96/go-cms-kernel"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/auth"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Database struct{ repository *Repository }
type DatabaseFactory struct{}

func (DatabaseFactory) ModuleCode() kernel.ModuleCode { return auth.ModuleCode }
func (DatabaseFactory) Build(connector kernel.DBConnector) (kernel.ModuleDatabase, error) {
	value, ok := connector.(*connectorpostgres.Connector)
	if !ok {
		return nil, errors.New("Auth PostgreSQL adapter requires *postgres.Connector")
	}
	return NewDatabase(value)
}

func NewDatabase(connector *connectorpostgres.Connector) (*Database, error) {
	if connector == nil || connector.Pool() == nil {
		return nil, errors.New("authentication PostgreSQL connector is nil")
	}
	return &Database{repository: &Repository{connector: connector}}, nil
}

func (*Database) ModuleCode() kernel.ModuleCode              { return auth.ModuleCode }
func (d *Database) AuthenticationFlows() auth.FlowRepository { return d.repository }
func (*Database) MigrationSources() []migrations.Source {
	return []migrations.Source{{ID: string(auth.ModuleCode), Schema: "auth", FS: migrationFiles, Path: "migrations"}}
}

var _ auth.Database = (*Database)(nil)
var _ kernel.ModuleDatabaseFactory = DatabaseFactory{}
var _ migrations.Provider = (*Database)(nil)
