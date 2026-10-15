package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/console"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
)

type usersApplication struct {
	Application
	groups      []group.Group
	groupsErr   error
	createInput user.CreateInput
	created     user.User
	createErr   error
	changedID   security.UserID
	password    string
	changed     user.User
}

func (a *usersApplication) listGroups(
	context.Context,
	security.Actor,
) ([]group.Group, error) {
	return append([]group.Group(nil), a.groups...), a.groupsErr
}

func (a *usersApplication) createUser(
	_ context.Context,
	_ security.Actor,
	input user.CreateInput,
) (user.User, error) {
	input.GroupIDs = append([]group.ID(nil), input.GroupIDs...)
	a.createInput = input
	return a.created, a.createErr
}

type usersService struct {
	user.Service
	application *usersApplication
}

func (s usersService) Create(
	ctx context.Context,
	actor security.Actor,
	input user.CreateInput,
) (user.User, error) {
	return s.application.createUser(ctx, actor, input)
}

func (s usersService) ChangePassword(
	_ context.Context,
	_ security.Actor,
	id security.UserID,
	password string,
) (user.User, error) {
	s.application.changedID = id
	s.application.password = password
	return s.application.changed, nil
}

type groupsService struct {
	group.Service
	application *usersApplication
}

func (s groupsService) List(
	ctx context.Context,
	actor security.Actor,
) ([]group.Group, error) {
	return s.application.listGroups(ctx, actor)
}

func (a *usersApplication) Users() user.Service {
	return usersService{application: a}
}

func (a *usersApplication) Groups() group.Service {
	return groupsService{application: a}
}

func TestCreateUserRequiresExactlyOnePasswordSource(t *testing.T) {
	t.Parallel()

	baseArgs := []string{
		"create",
		"-login=user",
		"-email=user@example.test",
		"-name=User",
		"-group=manager",
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: baseArgs, want: "either --password or --generate-password is required"},
		{name: "both", args: append(append([]string(nil), baseArgs...), "-password=secret", "-generate-password"), want: "use either --password or --generate-password, not both"},
		{name: "empty manual", args: append(append([]string(nil), baseArgs...), "-password="), want: "password is empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := (&usersCommand{application: &usersApplication{}}).Run(
				t.Context(),
				test.args,
				console.IO{In: failingReader{err: errors.New("stdin must not be read")}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}},
			)
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestUsersPasswordRequiresAndPassesDirectPasswordFlag(t *testing.T) {
	application := &usersApplication{changed: user.User{ID: 9}}
	command := &usersCommand{application: application}
	for _, test := range []struct {
		name     string
		args     []string
		wantErr  string
		password string
	}{
		{name: "missing", args: []string{"password", "-id=9"}, wantErr: "password is required"},
		{name: "empty", args: []string{"password", "-id=9", "-password="}, wantErr: "password is required"},
		{name: "provided", args: []string{"password", "-id=9", "-password=  direct password  "}, password: "  direct password  "},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := bytes.Buffer{}
			err := command.Run(t.Context(), test.args, console.IO{
				In: failingReader{err: errors.New("stdin must not be read")}, Out: &output, Err: &bytes.Buffer{},
			})
			if test.wantErr != "" {
				if err == nil || err.Error() != test.wantErr {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if application.changedID != 9 || application.password != test.password {
				t.Fatalf("changed id = %d, password = %q", application.changedID, application.password)
			}
		})
	}
}

func TestOtherUserCommandsRejectPasswordFlag(t *testing.T) {
	t.Parallel()

	for _, subcommand := range []string{"get", "block", "unblock"} {
		t.Run(subcommand, func(t *testing.T) {
			t.Parallel()
			var flagErrors bytes.Buffer
			err := (&usersCommand{}).Run(
				t.Context(),
				[]string{subcommand, "-id=1", "-password=secret"},
				console.IO{
					In:  failingReader{err: errors.New("stdin must not be read")},
					Out: &bytes.Buffer{},
					Err: &flagErrors,
				},
			)
			if err == nil || !strings.Contains(
				flagErrors.String(),
				"flag provided but not defined: -password",
			) {
				t.Fatalf("error = %v, stderr = %q", err, flagErrors.String())
			}
		})
	}
}

func TestCreateUserRequiresIdentityAndGroupFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "login",
			args: []string{
				"create",
				"-email=user@example.test",
				"-name=User",
				"-group=manager",
			},
			want: "login is required",
		},
		{
			name: "email",
			args: []string{
				"create",
				"-login=user",
				"-name=User",
				"-group=manager",
			},
			want: "email is required",
		},
		{
			name: "name",
			args: []string{
				"create",
				"-login=user",
				"-email=user@example.test",
				"-group=manager",
			},
			want: "name is required",
		},
		{
			name: "group",
			args: []string{
				"create",
				"-login=user",
				"-email=user@example.test",
				"-name=User",
			},
			want: "at least one group is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := (&usersCommand{
				application: &usersApplication{},
			}).Run(
				t.Context(),
				test.args,
				console.IO{
					In:  strings.NewReader("a-valid-password\n"),
					Out: &bytes.Buffer{},
					Err: &bytes.Buffer{},
				},
			)
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCreateUserSelectsGroupsAndUsesPasswordFlag(t *testing.T) {
	t.Parallel()

	application := &usersApplication{
		groups: []group.Group{
			{ID: 7, Code: "editor", Name: "Editor"},
			{ID: 2, Code: "manager", Name: "Manager"},
		},
		created: user.User{
			ID:    11,
			Login: "new-user",
			Email: "new-user@example.test",
			Name:  "New User",
		},
	}
	var output bytes.Buffer
	err := (&usersCommand{application: application}).Run(
		t.Context(),
		[]string{
			"create",
			"-login=NEW-USER",
			"-email=NEW-USER@EXAMPLE.TEST",
			"-name=New User",
			"-password=  a-valid-password  ",
			"-group=MANAGER",
			"-group=editor",
		},
		console.IO{
			In:  failingReader{err: errors.New("stdin must not be read")},
			Out: &output,
			Err: &bytes.Buffer{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if application.createInput.Password != "  a-valid-password  " {
		t.Fatalf("password = %q", application.createInput.Password)
	}
	if !slices.Equal(
		application.createInput.GroupIDs,
		[]group.ID{2, 7},
	) {
		t.Fatalf("group ids = %#v", application.createInput.GroupIDs)
	}
	if strings.Contains(output.String(), "generated_password") {
		t.Fatalf("generated password leaked in output: %s", output.String())
	}

	var result createUserResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.User.ID != 11 ||
		len(result.Groups) != 2 ||
		result.Groups[0].Code != "manager" ||
		result.Groups[1].Code != "editor" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCreateUserGeneratesPasswordWithoutReadingStdin(t *testing.T) {
	t.Parallel()

	application := &usersApplication{
		groups: []group.Group{
			{ID: 2, Code: "manager", Name: "Manager"},
		},
		created: user.User{ID: 12, Login: "generated-user"},
	}
	var output bytes.Buffer
	err := (&usersCommand{application: application}).Run(
		t.Context(),
		[]string{
			"create",
			"-login=generated-user",
			"-email=generated@example.test",
			"-name=Generated User",
			"-group=manager",
			"-generate-password",
		},
		console.IO{
			In:  failingReader{err: errors.New("stdin must not be read")},
			Out: &output,
			Err: &bytes.Buffer{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	var result createUserResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.GeneratedPassword) != 32 ||
		application.createInput.Password != result.GeneratedPassword {
		t.Fatalf(
			"generated password = %q, input = %q",
			result.GeneratedPassword,
			application.createInput.Password,
		)
	}
}

func TestCreateUserRejectsUnknownAndDuplicateGroups(t *testing.T) {
	t.Parallel()

	application := &usersApplication{
		groups: []group.Group{
			{ID: 2, Code: "manager", Name: "Manager"},
			{ID: 1, Code: "admin", Name: "Administrator"},
		},
	}
	for _, test := range []struct {
		name   string
		groups []string
		want   string
	}{
		{
			name:   "unknown",
			groups: []string{"missing"},
			want: "unknown group \"missing\"; available groups: " +
				"admin (Administrator), manager (Manager)",
		},
		{
			name:   "duplicate",
			groups: []string{"manager", "MANAGER"},
			want:   "duplicate group code \"manager\"",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			args := []string{
				"create",
				"-login=user",
				"-email=user@example.test",
				"-name=User",
				"-password=valid-password",
			}
			for _, code := range test.groups {
				args = append(args, "-group="+code)
			}
			err := (&usersCommand{application: application}).Run(
				t.Context(),
				args,
				console.IO{
					In:  failingReader{err: errors.New("stdin must not be read")},
					Out: &bytes.Buffer{},
					Err: &bytes.Buffer{},
				},
			)
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGeneratePasswordUsesTwentyFourBytesOfEntropy(t *testing.T) {
	t.Parallel()

	password, err := generatePassword(bytes.NewReader(make([]byte, 24)))
	if err != nil {
		t.Fatal(err)
	}
	if password != strings.Repeat("A", 32) {
		t.Fatalf("password = %q", password)
	}

	expected := errors.New("entropy unavailable")
	if _, err := generatePassword(failingReader{err: expected}); !errors.Is(
		err,
		expected,
	) {
		t.Fatalf("entropy error = %v", err)
	}
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

var _ io.Reader = failingReader{}
