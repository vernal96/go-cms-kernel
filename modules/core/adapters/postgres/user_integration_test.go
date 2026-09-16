package postgres

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
)

func TestPostgresUserCreateWithGroupsIsAtomic(t *testing.T) {
	host := os.Getenv("CMS_TEST_POSTGRES_HOST")
	if host == "" {
		t.Skip("set CMS_TEST_POSTGRES_HOST to run the PostgreSQL integration test")
	}

	port := 5432
	if value := os.Getenv("CMS_TEST_POSTGRES_PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("parse CMS_TEST_POSTGRES_PORT: %v", err)
		}
		port = parsed
	}
	sslMode := os.Getenv("CMS_TEST_POSTGRES_SSL_MODE")
	if sslMode == "" {
		sslMode = "disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connector, err := connectorpostgres.New(ctx, connectorpostgres.Config{
		Code:            kernel.ConnectionCode("user-integration"),
		Host:            host,
		Port:            port,
		Database:        os.Getenv("CMS_TEST_POSTGRES_DB"),
		User:            os.Getenv("CMS_TEST_POSTGRES_USER"),
		Password:        os.Getenv("CMS_TEST_POSTGRES_PASSWORD"),
		SSLMode:         sslMode,
		MaxConns:        4,
		MinConns:        0,
		ConnMaxLifetime: time.Minute,
		ConnectTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connector.Close() })

	database, err := NewDatabase(connector)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewManager().Up(ctx, migrations.Plan{
		Connection: string(connector.Code()),
		Target:     connector,
		Source:     database.MigrationSources()[0],
	}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	cleanup := func(cleanupCtx context.Context) {
		_, _ = connector.Pool().Exec(cleanupCtx, `
DELETE FROM core.users
WHERE login IN (
    'console-atomic-user',
    'console-rollback-user',
    'other-console-user'
);
DELETE FROM core.groups
WHERE code IN ('console-group-one', 'console-group-two');
`)
	}
	cleanup(ctx)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()
		cleanup(cleanupCtx)
	})

	firstGroup, err := database.Groups().Create(ctx, nil, group.Group{
		Code: "console-group-one",
		Name: "Console Group One",
	}, nil, nil)
	if err != nil {
		t.Fatalf("create first group: %v", err)
	}
	secondGroup, err := database.Groups().Create(ctx, nil, group.Group{
		Code: "console-group-two",
		Name: "Console Group Two",
	}, nil, nil)
	if err != nil {
		t.Fatalf("create second group: %v", err)
	}

	userRepository := database.Users()
	created, err := userRepository.Create(
		ctx,
		nil,
		user.Record{
			User: user.User{
				Login:       "console-atomic-user",
				Email:       "console-atomic-user@example.test",
				Name:        "Console Atomic User",
				ColorScheme: user.ColorSchemeSystem,
				AccentColor: user.AccentColorBlue,
			},
			PasswordHash: "integration-password-hash",
		},
		[]group.ID{firstGroup.ID, secondGroup.ID},
		nil,
	)
	if err != nil {
		t.Fatalf("create atomic user: %v", err)
	}

	var memberships int
	if err := connector.Pool().QueryRow(ctx, `
SELECT count(*)
FROM core.user_groups
WHERE user_id = $1;
`, created.ID).Scan(&memberships); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if memberships != 2 {
		t.Fatalf("memberships = %d", memberships)
	}

	if _, err := userRepository.Block(ctx, nil, created.ID); err != nil {
		t.Fatalf("block user: %v", err)
	}
	if _, err := userRepository.Create(
		ctx,
		nil,
		user.Record{
			User: user.User{
				Login:       "console-atomic-user",
				Email:       "other-console-user@example.test",
				Name:        "Duplicate Login",
				ColorScheme: user.ColorSchemeSystem,
				AccentColor: user.AccentColorBlue,
			},
			PasswordHash: "integration-password-hash",
		},
		nil,
		nil,
	); !errors.Is(err, user.ErrLoginExists) ||
		!errors.Is(err, user.ErrConflict) {
		t.Fatalf("reserved login error = %v", err)
	}
	if _, err := userRepository.Create(
		ctx,
		nil,
		user.Record{
			User: user.User{
				Login:       "other-console-user",
				Email:       "console-atomic-user@example.test",
				Name:        "Duplicate Email",
				ColorScheme: user.ColorSchemeSystem,
				AccentColor: user.AccentColorBlue,
			},
			PasswordHash: "integration-password-hash",
		},
		nil,
		nil,
	); !errors.Is(err, user.ErrEmailExists) ||
		!errors.Is(err, user.ErrConflict) {
		t.Fatalf("reserved email error = %v", err)
	}

	if _, err := userRepository.Create(
		ctx,
		nil,
		user.Record{
			User: user.User{
				Login:       "console-rollback-user",
				Email:       "console-rollback-user@example.test",
				Name:        "Console Rollback User",
				ColorScheme: user.ColorSchemeSystem,
				AccentColor: user.AccentColorBlue,
			},
			PasswordHash: "integration-password-hash",
		},
		[]group.ID{firstGroup.ID, group.ID(1 << 60)},
		nil,
	); !errors.Is(err, group.ErrNotFound) {
		t.Fatalf("missing group error = %v", err)
	}
	if _, err := userRepository.ByIdentifier(
		ctx,
		"console-rollback-user",
	); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("atomic rollback left user behind: %v", err)
	}

	stats := connector.Pool().Stat()
	if stats.AcquiredConns() != 0 {
		t.Fatalf(
			"repository leaked %d database connections",
			stats.AcquiredConns(),
		)
	}
}
