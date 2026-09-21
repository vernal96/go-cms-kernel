package postgres

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConnectionLifetime(t *testing.T) {
	for _, test := range []struct {
		name  string
		input time.Duration
		want  time.Duration
	}{
		{name: "default", want: time.Hour},
		{name: "explicit", input: 5 * time.Minute, want: 5 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			connector, err := New(context.Background(), Config{
				Code: "test", Host: "localhost", Port: 5432, Database: "test",
				User: "test", SSLMode: "disable", MaxConns: 1,
				ConnectTimeout: time.Second, ConnMaxLifetime: test.input,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connector.Close() })
			if got := connector.Pool().Config().MaxConnLifetime; got != test.want {
				t.Fatalf("pool lifetime = %s, want %s", got, test.want)
			}
			if got := connector.config.ConnMaxLifetime; got != test.want {
				t.Fatalf("migration connection lifetime = %s, want %s", got, test.want)
			}
		})
	}
}

func TestNegativeConnectionLifetimeIsRejected(t *testing.T) {
	_, err := New(context.Background(), Config{
		Code: "test", Host: "localhost", Port: 5432, Database: "test",
		User: "test", SSLMode: "disable", MaxConns: 1,
		ConnectTimeout: time.Second, ConnMaxLifetime: -time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "max lifetime cannot be negative") {
		t.Fatalf("unexpected error: %v", err)
	}
}
