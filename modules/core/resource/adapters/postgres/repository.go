package postgres

import (
	"errors"

	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
)

type Repository struct {
	connector *connectorpostgres.Connector
}

func NewRepository(
	connector *connectorpostgres.Connector,
) (*Repository, error) {
	if connector == nil {
		return nil, errors.New("postgres connector is nil")
	}
	if connector.Pool() == nil {
		return nil, errors.New("postgres pool is nil")
	}

	return &Repository{connector: connector}, nil
}

var _ resource.Repository = (*Repository)(nil)

var _ resource.WidgetRepository = (*Repository)(nil)

var _ resource.ManagementRepository = (*Repository)(nil)

var _ resource.StatisticsRepository = (*Repository)(nil)

var _ resource.QueryRepository = (*Repository)(nil)
