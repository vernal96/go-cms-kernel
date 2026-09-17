package config_test

import (
	"os"
	"path/filepath"
	"testing"

	configloader "github.com/vernal96/go-cms-kernel/config"
)

type databaseConfig struct {
	Host string `envconfig:"HOST" required:"true"`
	Port int    `envconfig:"PORT" default:"5432"`
}

type applicationConfig struct {
	Database databaseConfig `envconfig:"DATABASE"`
}

type dotenvConfig struct {
	Host string `envconfig:"HOST" required:"true"`
	Port int    `envconfig:"PORT" default:"8080"`
}

func TestLoadSupportsNestedAndExplicitPrefixes(t *testing.T) {
	t.Setenv("DATABASE_HOST", "nested-host")
	t.Setenv("CUSTOM_HOST", "custom-host")
	t.Setenv("CUSTOM_PORT", "6432")

	application, err := configloader.Load[applicationConfig]("")
	if err != nil {
		t.Fatal(err)
	}
	if application.Database.Host != "nested-host" || application.Database.Port != 5432 {
		t.Fatalf("nested config = %#v", application.Database)
	}

	database, err := configloader.Load[databaseConfig]("CUSTOM")
	if err != nil {
		t.Fatal(err)
	}
	if database.Host != "custom-host" || database.Port != 6432 {
		t.Fatalf("prefixed config = %#v", database)
	}
}

func TestLoadRejectsNonStructType(t *testing.T) {
	if _, err := configloader.Load[int](""); err == nil {
		t.Fatal("expected non-struct config error")
	}
}

func TestLoadDotEnvReadsDefaultFileAndPreservesEnvironment(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(
		filepath.Join(".", ".env"),
		[]byte("APP_HOST=dotenv-host\r\nAPP_PORT=9090\r\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_HOST", "environment-host")
	if err := os.Unsetenv("APP_PORT"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("APP_PORT") })

	config, err := configloader.LoadDotEnv[dotenvConfig]("APP")
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "environment-host" || config.Port != 9090 {
		t.Fatalf("dotenv config = %#v", config)
	}
}

func TestLoadDotEnvAllowsMissingFile(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("APP_HOST", "environment-host")

	config, err := configloader.LoadDotEnv[dotenvConfig]("APP")
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "environment-host" || config.Port != 8080 {
		t.Fatalf("dotenv config = %#v", config)
	}
}

func TestLoadDotEnvReportsReadErrors(t *testing.T) {
	directory := t.TempDir()

	if _, err := configloader.LoadDotEnv[dotenvConfig]("APP", directory); err == nil {
		t.Fatal("expected dotenv read error")
	}
}
