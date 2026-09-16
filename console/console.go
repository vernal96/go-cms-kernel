package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/seeds"
)

var commandNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

func StandardIO() IO {
	return IO{
		In:  os.Stdin,
		Out: os.Stdout,
		Err: os.Stderr,
	}
}

type Command interface {
	Name() string
	Description() string
	Run(context.Context, []string, IO) error
}

type Provider interface {
	Commands() []Command
}

// RequiresBoot marks commands that need the application's services, as
// opposed to schema and seed commands which intentionally run before Boot.
type RequiresBoot interface {
	RequiresBoot() bool
}

type bootableApplication interface {
	Boot(context.Context) error
}

type Application interface {
	kernel.DatabaseResolver
	MigrationPlans() []migrations.Plan
	SeedPlans() []seeds.Plan
	CommandProviders() []Provider
}

type Console struct {
	application Application
	commands    map[string]Command
}

func New(application Application) (*Console, error) {
	if application == nil {
		return nil, errors.New("console application is nil")
	}

	runner := &Console{
		application: application,
		commands:    make(map[string]Command),
	}

	commands := []Command{
		newMigrationsCommand(application),
		newSeedsCommand(application),
	}

	for _, provider := range application.CommandProviders() {
		if provider == nil {
			return nil, errors.New("console command provider is nil")
		}

		commands = append(commands, provider.Commands()...)
	}

	for _, command := range commands {
		if err := runner.register(command); err != nil {
			return nil, err
		}
	}

	return runner, nil
}

func (c *Console) Application() Application {
	if c == nil {
		return nil
	}

	return c.application
}

func (c *Console) Run(
	ctx context.Context,
	args []string,
	streams IO,
) error {
	if c == nil {
		return errors.New("console is nil")
	}

	if ctx == nil {
		return errors.New("console context is nil")
	}

	streams = normalizeIO(streams)

	if len(args) == 0 || args[0] == "help" {
		return c.writeHelp(streams.Out)
	}

	command, exists := c.commands[args[0]]
	if !exists {
		return fmt.Errorf("unknown console command %q", args[0])
	}

	if bootCommand, ok := command.(RequiresBoot); ok &&
		bootCommand.RequiresBoot() {
		application, ok := c.application.(bootableApplication)
		if !ok {
			return fmt.Errorf(
				"command %q requires a bootable application",
				command.Name(),
			)
		}
		if err := application.Boot(ctx); err != nil {
			return fmt.Errorf(
				"boot application for command %q: %w",
				command.Name(),
				err,
			)
		}
	}

	return command.Run(ctx, args[1:], streams)
}

func (c *Console) register(command Command) error {
	if command == nil {
		return errors.New("console command is nil")
	}

	name := command.Name()
	if !commandNamePattern.MatchString(name) {
		return fmt.Errorf("invalid console command name %q", name)
	}

	if _, exists := c.commands[name]; exists {
		return fmt.Errorf("console command %q is registered more than once", name)
	}

	c.commands[name] = command
	return nil
}

func (c *Console) writeHelp(output io.Writer) error {
	names := make([]string, 0, len(c.commands))
	for name := range c.commands {
		names = append(names, name)
	}
	sort.Strings(names)

	if _, err := fmt.Fprintln(output, "Usage: console <command> [arguments]"); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(output, "Commands:"); err != nil {
		return err
	}

	for _, name := range names {
		if _, err := fmt.Fprintf(
			output,
			"  %-16s %s\n",
			name,
			c.commands[name].Description(),
		); err != nil {
			return err
		}
	}

	return nil
}

func normalizeIO(streams IO) IO {
	if streams.In == nil {
		streams.In = os.Stdin
	}
	if streams.Out == nil {
		streams.Out = io.Discard
	}
	if streams.Err == nil {
		streams.Err = io.Discard
	}

	return streams
}
