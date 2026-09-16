package postgres

import (
	"embed"
	"errors"

	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/mail"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Database struct{ repository *Repository }

type DatabaseFactory struct{}

func (DatabaseFactory) ModuleCode() kernel.ModuleCode { return mail.ModuleCode }

func (DatabaseFactory) Build(connector kernel.DBConnector) (kernel.ModuleDatabase, error) {
	postgresConnector, ok := connector.(*connectorpostgres.Connector)
	if !ok {
		return nil, errors.New("mail postgres adapter requires *postgres.Connector")
	}
	return NewDatabase(postgresConnector)
}

func NewDatabase(connector *connectorpostgres.Connector) (*Database, error) {
	repository, err := NewRepository(connector)
	if err != nil {
		return nil, err
	}
	return &Database{repository: repository}, nil
}

func (*Database) ModuleCode() kernel.ModuleCode { return mail.ModuleCode }
func (d *Database) Mail() mail.Repository       { return d.repository }

func (*Database) MigrationSources() []migrations.Source {
	return []migrations.Source{{ID: string(mail.ModuleCode), Schema: "mail", FS: migrationFiles, Path: "migrations"}}
}

var _ mail.Database = (*Database)(nil)
var _ kernel.ModuleDatabaseFactory = DatabaseFactory{}
var _ migrations.Provider = (*Database)(nil)
