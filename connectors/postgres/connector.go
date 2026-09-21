package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	migratedb "github.com/golang-migrate/migrate/v4/database"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/migrations"
)

var identifierPattern = regexp.MustCompile(
	`^[a-z][a-z0-9_]*$`,
)

type Config struct {
	Code            kernel.ConnectionCode
	Host            string
	Port            int
	Database        string
	User            string
	Password        string
	SSLMode         string
	MaxConns        int32
	MinConns        int32
	ConnMaxLifetime time.Duration // Zero defaults to one hour; negative values are invalid.
	ConnectTimeout  time.Duration
}

type Connector struct {
	code   kernel.ConnectionCode
	pool   *pgxpool.Pool
	dsn    string
	config Config

	closeOnce sync.Once
}

type Factory struct {
	Config Config
}

func (f Factory) Code() kernel.ConnectionCode {
	return f.Config.Code
}

func (f Factory) Open(
	ctx context.Context,
) (kernel.DBConnector, error) {
	return New(ctx, f.Config)
}

func New(
	ctx context.Context,
	config Config,
) (*Connector, error) {
	if ctx == nil {
		return nil, errors.New("postgres connector context is nil")
	}

	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if config.ConnMaxLifetime == 0 {
		config.ConnMaxLifetime = time.Hour
	}

	dsn := connectionString(config)

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}

	poolConfig.MaxConns = config.MaxConns
	poolConfig.MinConns = config.MinConns
	poolConfig.MaxConnLifetime = config.ConnMaxLifetime
	poolConfig.ConnConfig.ConnectTimeout = config.ConnectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	connector := &Connector{
		code:   config.Code,
		pool:   pool,
		dsn:    dsn,
		config: config,
	}

	return connector, nil
}

func (c *Connector) Code() kernel.ConnectionCode {
	return c.code
}

func (c *Connector) Pool() *pgxpool.Pool {
	return c.pool
}

func (c *Connector) Ping(ctx context.Context) error {
	if ctx == nil {
		return errors.New("postgres ping context is nil")
	}

	if c == nil || c.pool == nil {
		return errors.New("postgres connector is not initialized")
	}

	if err := c.pool.Ping(ctx); err != nil {
		return fmt.Errorf(
			"ping postgres connector %q: %w",
			c.code,
			err,
		)
	}

	return nil
}

func (c *Connector) OpenMigrationDriver(
	ctx context.Context,
	schema string,
	historyTable string,
) (migratedb.Driver, error) {
	if ctx == nil {
		return nil, errors.New("migration context is nil")
	}

	if !identifierPattern.MatchString(schema) {
		return nil, fmt.Errorf(
			"invalid postgres migration schema %q",
			schema,
		)
	}

	if !identifierPattern.MatchString(historyTable) {
		return nil, fmt.Errorf(
			"invalid postgres migration table %q",
			historyTable,
		)
	}

	if c == nil || c.pool == nil {
		return nil, errors.New("postgres connector is not initialized")
	}

	if _, err := c.pool.Exec(
		ctx,
		"CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize(),
	); err != nil {
		return nil, fmt.Errorf(
			"create postgres schema %q: %w",
			schema,
			err,
		)
	}

	database, err := sql.Open("pgx", c.dsn)
	if err != nil {
		return nil, fmt.Errorf(
			"open postgres migration connection: %w",
			err,
		)
	}

	database.SetMaxOpenConns(int(c.config.MaxConns))
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(
		c.config.ConnMaxLifetime,
	)

	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}

	driver, err := migratepgx.WithInstance(
		database,
		&migratepgx.Config{
			SchemaName:            schema,
			MigrationsTable:       historyTable,
			MultiStatementEnabled: true,
		},
	)
	if err != nil {
		_ = database.Close()
		return nil, err
	}

	return driver, nil
}

func (c *Connector) Close() error {
	if c == nil {
		return nil
	}

	c.closeOnce.Do(func() {
		if c.pool != nil {
			c.pool.Close()
		}
	})

	return nil
}

func connectionString(config Config) string {
	connectionURL := &url.URL{
		Scheme: "postgres",
		User: url.UserPassword(
			config.User,
			config.Password,
		),
		Host: net.JoinHostPort(
			config.Host,
			strconv.Itoa(config.Port),
		),
		Path: config.Database,
	}

	query := connectionURL.Query()
	query.Set("sslmode", config.SSLMode)
	connectionURL.RawQuery = query.Encode()

	return connectionURL.String()
}

func validateConfig(config Config) error {
	switch {
	case config.Code == "":
		return errors.New("postgres connection code is empty")
	case config.Host == "":
		return errors.New("postgres host is empty")
	case config.Port <= 0:
		return errors.New("postgres port must be positive")
	case config.Database == "":
		return errors.New("postgres database is empty")
	case config.User == "":
		return errors.New("postgres user is empty")
	case config.SSLMode == "":
		return errors.New("postgres ssl mode is empty")
	case config.MaxConns <= 0:
		return errors.New("postgres max connections must be positive")
	case config.MinConns < 0:
		return errors.New("postgres min connections cannot be negative")
	case config.MinConns > config.MaxConns:
		return errors.New(
			"postgres min connections cannot exceed max connections",
		)
	case config.ConnMaxLifetime < 0:
		return errors.New("postgres connection max lifetime cannot be negative")
	case config.ConnectTimeout <= 0:
		return errors.New(
			"postgres connect timeout must be positive",
		)
	default:
		return nil
	}
}

var _ kernel.DBConnector = (*Connector)(nil)
var _ kernel.ConnectorFactory = Factory{}
var _ migrations.Target = (*Connector)(nil)
