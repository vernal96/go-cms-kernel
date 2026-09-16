package config_test

import (
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
