package postgres

import (
	"errors"

	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/search"
)

type Database struct{ engine *Engine }
type DatabaseFactory struct{}

func (DatabaseFactory) ModuleCode() kernel.ModuleCode { return search.ModuleCode }
func (DatabaseFactory) Build(connector kernel.DBConnector) (kernel.ModuleDatabase, error) {
	value, ok := connector.(*connectorpostgres.Connector)
	if !ok {
		return nil, errors.New("search PostgreSQL adapter requires *postgres.Connector")
	}
	return NewDatabase(value)
}
func NewDatabase(connector *connectorpostgres.Connector) (*Database, error) {
	engine, err := NewEngine(connector)
	if err != nil {
		return nil, err
	}
	return &Database{engine: engine}, nil
}
func (*Database) ModuleCode() kernel.ModuleCode { return search.ModuleCode }
func (d *Database) Search() search.Engine       { return d.engine }

var _ search.Database = (*Database)(nil)
var _ kernel.ModuleDatabaseFactory = DatabaseFactory{}
