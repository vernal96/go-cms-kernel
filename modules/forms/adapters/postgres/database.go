package postgres

import (
	"embed"
	"errors"

	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/forms"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Database struct{ repository *Repository }
type DatabaseFactory struct{}

func (DatabaseFactory) ModuleCode() kernel.ModuleCode { return forms.ModuleCode }
func (DatabaseFactory) Build(connector kernel.DBConnector) (kernel.ModuleDatabase, error) {
	value, ok := connector.(*connectorpostgres.Connector)
	if !ok {
		return nil, errors.New("Forms PostgreSQL adapter requires *postgres.Connector")
	}
	return NewDatabase(value)
}
func NewDatabase(connector *connectorpostgres.Connector) (*Database, error) {
	repository, err := NewRepository(connector)
	if err != nil {
		return nil, err
	}
	return &Database{repository: repository}, nil
}
func (*Database) ModuleCode() kernel.ModuleCode { return forms.ModuleCode }
func (d *Database) Forms() forms.Repository     { return d.repository }
func (*Database) MigrationSources() []migrations.Source {
	return []migrations.Source{{ID: string(forms.ModuleCode), Schema: "forms", FS: migrationFiles, Path: "migrations"}}
}

var _ forms.Database = (*Database)(nil)
var _ kernel.ModuleDatabaseFactory = DatabaseFactory{}
var _ migrations.Provider = (*Database)(nil)
