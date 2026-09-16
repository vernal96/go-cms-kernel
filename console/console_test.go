package console_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/console"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/seeds"
)

type application struct {
	providers []console.Provider
}

func (application) MigrationPlans() []migrations.Plan { return nil }
func (application) SeedPlans() []seeds.Plan           { return nil }
func (application) MainModuleDatabase(
	kernel.ModuleCode,
) (kernel.ModuleDatabase, bool) {
	return nil, false
}
func (application) ModuleDatabase(
	kernel.ConnectionCode,
	kernel.ModuleCode,
) (kernel.ModuleDatabase, bool) {
	return nil, false
}
func (a application) CommandProviders() []console.Provider {
	return a.providers
}

type provider struct{ commands []console.Command }

func (p provider) Commands() []console.Command { return p.commands }

type command struct{ name string }

func (c command) Name() string      { return c.name }
func (command) Description() string { return "custom command" }
func (c command) Run(
	_ context.Context,
	_ []string,
	streams console.IO,
) error {
	_, err := streams.Out.Write([]byte(c.name))
	return err
}

type bootCommand struct{ command }

func (bootCommand) RequiresBoot() bool { return true }

type bootApplication struct {
	application
	boots int
}

func (a *bootApplication) Boot(context.Context) error {
	a.boots++
	return nil
}

func TestConsoleRegistersBuiltinsAndProviderCommands(t *testing.T) {
	runner, err := console.New(application{
		providers: []console.Provider{
			provider{commands: []console.Command{command{name: "custom"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runner.Run(
		context.Background(),
		[]string{"help"},
		console.IO{Out: &output},
	); err != nil {
		t.Fatal(err)
	}

	help := output.String()
	for _, name := range []string{"migrations", "seeds", "custom"} {
		if !strings.Contains(help, name) {
			t.Fatalf("help does not contain %q: %s", name, help)
		}
	}

	output.Reset()
	if err := runner.Run(
		context.Background(),
		[]string{"custom"},
		console.IO{Out: &output},
	); err != nil {
		t.Fatal(err)
	}
	if output.String() != "custom" {
		t.Fatalf("custom output = %q", output.String())
	}
}

func TestConsoleRejectsCommandNameConflict(t *testing.T) {
	_, err := console.New(application{
		providers: []console.Provider{
			provider{commands: []console.Command{command{name: "migrations"}}},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate command error")
	}
}

func TestEmptySeedCollectionIsAValidNoOp(t *testing.T) {
	runner, err := console.New(application{})
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runner.Run(
		context.Background(),
		[]string{"seeds", "status"},
		console.IO{Out: &output},
	); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != "[]" {
		t.Fatalf("empty seed status = %q", output.String())
	}
}

func TestConsoleBootsOnlyCommandsThatRequireServices(t *testing.T) {
	application := &bootApplication{
		application: application{
			providers: []console.Provider{
				provider{commands: []console.Command{
					command{name: "plain"},
					bootCommand{command{name: "secured"}},
				}},
			},
		},
	}
	runner, err := console.New(application)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(
		context.Background(),
		[]string{"plain"},
		console.IO{},
	); err != nil {
		t.Fatal(err)
	}
	if application.boots != 0 {
		t.Fatalf("plain command boot count = %d", application.boots)
	}
	if err := runner.Run(
		context.Background(),
		[]string{"secured"},
		console.IO{},
	); err != nil {
		t.Fatal(err)
	}
	if application.boots != 1 {
		t.Fatalf("secured command boot count = %d", application.boots)
	}
}
