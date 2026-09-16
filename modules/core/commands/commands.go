package commands

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/vernal96/go-cms-kernel/console"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type Application interface {
	Users() user.Service
	Groups() group.Service
	Authorization() access.Service
}

type Provider struct {
	application Application
}

func New(application Application) *Provider {
	return &Provider{application: application}
}

func (p *Provider) Commands() []console.Command {
	if p == nil || p.application == nil {
		return nil
	}
	return []console.Command{
		&usersCommand{application: p.application},
		&groupsCommand{application: p.application},
		&permissionsCommand{application: p.application},
	}
}

type usersCommand struct {
	application Application
}

func (*usersCommand) Name() string        { return "users" }
func (*usersCommand) Description() string { return "manage users" }
func (*usersCommand) RequiresBoot() bool  { return true }

func (c *usersCommand) Run(
	ctx context.Context,
	args []string,
	streams console.IO,
) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := fmt.Fprintln(
			streams.Out,
			"Usage: console users <create|get|list|update|password|block|unblock> [flags]",
		)
		return err
	}

	switch args[0] {
	case "create":
		return c.create(ctx, args[1:], streams)

	case "get", "block", "unblock", "password":
		flags := newFlagSet("users "+args[0], streams)
		rawID := flags.Int64("id", 0, "user id")
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		id, err := userID(*rawID)
		if err != nil {
			return err
		}
		switch args[0] {
		case "get":
			item, err := c.application.Users().Get(ctx, security.System(), id)
			return writeResult(streams.Out, item, err)
		case "block":
			item, err := c.application.Users().Block(ctx, security.System(), id)
			return writeResult(streams.Out, item, err)
		case "unblock":
			item, err := c.application.Users().Unblock(ctx, security.System(), id)
			return writeResult(streams.Out, item, err)
		default:
			password, err := readPassword(streams.In)
			if err != nil {
				return err
			}
			item, err := c.application.Users().ChangePassword(
				ctx,
				security.System(),
				id,
				password,
			)
			return writeResult(streams.Out, item, err)
		}

	case "list":
		if len(args) != 1 {
			return errors.New("users list does not accept arguments")
		}
		items, err := c.application.Users().List(ctx, security.System())
		return writeResult(streams.Out, items, err)

	case "update":
		return c.update(ctx, args[1:], streams)
	default:
		return fmt.Errorf("unknown users subcommand %q", args[0])
	}
}

type createUserResult struct {
	User              user.User     `json:"user"`
	Groups            []group.Group `json:"groups"`
	GeneratedPassword string        `json:"generated_password,omitempty"`
}

func (c *usersCommand) create(
	ctx context.Context,
	args []string,
	streams console.IO,
) error {
	flags := newFlagSet("users create", streams)
	login := flags.String("login", "", "unique login")
	email := flags.String("email", "", "unique email")
	name := flags.String("name", "", "name")
	lastName := flags.String("last-name", "", "last name")
	middleName := flags.String("middle-name", "", "middle name")
	phone := flags.String("phone", "", "phone")
	avatar := flags.String("avatar-media-id", "", "avatar media id")
	generate := flags.Bool(
		"generate-password",
		false,
		"generate and return a password",
	)
	var groupCodes stringValues
	flags.Var(
		&groupCodes,
		"group",
		"group code; repeat for multiple groups",
	)
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "login", value: *login},
		{name: "email", value: *email},
		{name: "name", value: *name},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("%s is required", required.name)
		}
	}
	if len(groupCodes) == 0 {
		return errors.New("at least one group is required")
	}

	avatarID, err := optionalMediaID(*avatar)
	if err != nil {
		return err
	}
	available, err := c.application.Groups().List(ctx, security.System())
	if err != nil {
		return fmt.Errorf("list groups: %w", err)
	}
	groupIDs, selected, err := selectGroups(groupCodes, available)
	if err != nil {
		return err
	}

	var password string
	var generatedPassword string
	if *generate {
		password, err = generatePassword(cryptorand.Reader)
		generatedPassword = password
	} else {
		password, err = readPassword(streams.In)
	}
	if err != nil {
		return err
	}

	created, err := c.application.Users().Create(
		ctx,
		security.System(),
		user.CreateInput{
			Login:         *login,
			Email:         *email,
			Password:      password,
			Name:          *name,
			LastName:      optionalString(*lastName),
			MiddleName:    optionalString(*middleName),
			Phone:         optionalString(*phone),
			AvatarMediaID: avatarID,
			GroupIDs:      groupIDs,
		},
	)
	return writeResult(streams.Out, createUserResult{
		User:              created,
		Groups:            selected,
		GeneratedPassword: generatedPassword,
	}, err)
}

func (c *usersCommand) update(
	ctx context.Context,
	args []string,
	streams console.IO,
) error {
	flags := newFlagSet("users update", streams)
	rawID := flags.Int64("id", 0, "user id")
	login := flags.String("login", "", "unique login")
	email := flags.String("email", "", "unique email")
	name := flags.String("name", "", "name")
	lastName := flags.String("last-name", "", "last name; empty clears")
	middleName := flags.String(
		"middle-name",
		"",
		"middle name; empty clears",
	)
	phone := flags.String("phone", "", "phone; empty clears")
	avatar := flags.String(
		"avatar-media-id",
		"",
		"avatar media id; empty clears",
	)
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	id, err := userID(*rawID)
	if err != nil {
		return err
	}
	current, err := c.application.Users().Get(ctx, security.System(), id)
	if err != nil {
		return err
	}
	input := user.UpdateInput{
		ID:            id,
		Login:         current.Login,
		Email:         current.Email,
		Name:          current.Name,
		LastName:      current.LastName,
		MiddleName:    current.MiddleName,
		Phone:         current.Phone,
		AvatarMediaID: current.AvatarMediaID,
	}
	if wasSet(flags, "login") {
		input.Login = *login
	}
	if wasSet(flags, "email") {
		input.Email = *email
	}
	if wasSet(flags, "name") {
		input.Name = *name
	}
	if wasSet(flags, "last-name") {
		input.LastName = optionalString(*lastName)
	}
	if wasSet(flags, "middle-name") {
		input.MiddleName = optionalString(*middleName)
	}
	if wasSet(flags, "phone") {
		input.Phone = optionalString(*phone)
	}
	if wasSet(flags, "avatar-media-id") {
		input.AvatarMediaID, err = optionalMediaID(*avatar)
		if err != nil {
			return err
		}
		input.UpdateAvatar = true
	}
	updated, err := c.application.Users().Update(
		ctx,
		security.System(),
		input,
	)
	return writeResult(streams.Out, updated, err)
}

type groupsCommand struct {
	application Application
}

func (*groupsCommand) Name() string        { return "groups" }
func (*groupsCommand) Description() string { return "manage user groups" }
func (*groupsCommand) RequiresBoot() bool  { return true }

func (c *groupsCommand) Run(
	ctx context.Context,
	args []string,
	streams console.IO,
) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := fmt.Fprintln(
			streams.Out,
			"Usage: console groups <create|get|list|update|delete|add-user|remove-user|members|grant|revoke|permissions> [flags]",
		)
		return err
	}
	switch args[0] {
	case "create":
		flags := newFlagSet("groups create", streams)
		code := flags.String("code", "", "immutable group code")
		name := flags.String("name", "", "group name")
		isSuper := flags.Bool("super", false, "grant super privileges")
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		item, err := c.application.Groups().Create(
			ctx,
			security.System(),
			group.CreateInput{
				Code:    *code,
				Name:    *name,
				IsSuper: *isSuper,
			},
		)
		return writeResult(streams.Out, item, err)
	case "list":
		if len(args) != 1 {
			return errors.New("groups list does not accept arguments")
		}
		items, err := c.application.Groups().List(ctx, security.System())
		return writeResult(streams.Out, items, err)
	case "get", "delete", "members", "permissions":
		flags := newFlagSet("groups "+args[0], streams)
		rawGroup := flags.Int64("id", 0, "group id")
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		groupID, err := parseGroupID(*rawGroup)
		if err != nil {
			return err
		}
		switch args[0] {
		case "get":
			item, err := c.application.Groups().Get(
				ctx,
				security.System(),
				groupID,
			)
			return writeResult(streams.Out, item, err)
		case "delete":
			if err := c.application.Groups().Delete(
				ctx,
				security.System(),
				groupID,
			); err != nil {
				return err
			}
			return writeResult(streams.Out, map[string]any{
				"deleted":  true,
				"group_id": groupID,
			}, nil)
		case "members":
			items, err := c.application.Groups().Members(
				ctx,
				security.System(),
				groupID,
			)
			return writeResult(streams.Out, items, err)
		default:
			items, err := c.application.Groups().Permissions(
				ctx,
				security.System(),
				groupID,
			)
			return writeResult(streams.Out, items, err)
		}
	case "update":
		return c.update(ctx, args[1:], streams)
	case "add-user", "remove-user":
		return c.membership(ctx, args[0], args[1:], streams)
	case "grant", "revoke":
		return c.permission(ctx, args[0], args[1:], streams)
	default:
		return fmt.Errorf("unknown groups subcommand %q", args[0])
	}
}

func (c *groupsCommand) update(
	ctx context.Context,
	args []string,
	streams console.IO,
) error {
	flags := newFlagSet("groups update", streams)
	rawID := flags.Int64("id", 0, "group id")
	name := flags.String("name", "", "group name")
	isSuper := flags.Bool("super", false, "super privileges")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	id, err := parseGroupID(*rawID)
	if err != nil {
		return err
	}
	current, err := c.application.Groups().Get(ctx, security.System(), id)
	if err != nil {
		return err
	}
	input := group.UpdateInput{
		ID:      id,
		Name:    current.Name,
		IsSuper: current.IsSuper,
	}
	if wasSet(flags, "name") {
		input.Name = *name
	}
	if wasSet(flags, "super") {
		input.IsSuper = *isSuper
	}
	updated, err := c.application.Groups().Update(
		ctx,
		security.System(),
		input,
	)
	return writeResult(streams.Out, updated, err)
}

func (c *groupsCommand) membership(
	ctx context.Context,
	action string,
	args []string,
	streams console.IO,
) error {
	flags := newFlagSet("groups "+action, streams)
	rawGroup := flags.Int64("group", 0, "group id")
	rawUser := flags.Int64("user", 0, "user id")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	groupID, err := parseGroupID(*rawGroup)
	if err != nil {
		return err
	}
	userID, err := userID(*rawUser)
	if err != nil {
		return err
	}
	if action == "add-user" {
		item, err := c.application.Groups().AddUser(
			ctx,
			security.System(),
			groupID,
			userID,
		)
		return writeResult(streams.Out, item, err)
	}
	if err := c.application.Groups().RemoveUser(
		ctx,
		security.System(),
		groupID,
		userID,
	); err != nil {
		return err
	}
	return writeResult(streams.Out, map[string]any{
		"removed":  true,
		"group_id": groupID,
		"user_id":  userID,
	}, nil)
}

func (c *groupsCommand) permission(
	ctx context.Context,
	action string,
	args []string,
	streams console.IO,
) error {
	flags := newFlagSet("groups "+action, streams)
	rawGroup := flags.Int64("group", 0, "group id")
	rawCode := flags.String("permission", "", "permission code")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	groupID, err := parseGroupID(*rawGroup)
	if err != nil {
		return err
	}
	code := permission.Code(strings.TrimSpace(*rawCode))
	if code == "" {
		return errors.New("permission code is empty")
	}
	if action == "grant" {
		item, err := c.application.Groups().GrantPermission(
			ctx,
			security.System(),
			groupID,
			code,
		)
		return writeResult(streams.Out, item, err)
	}
	if err := c.application.Groups().RevokePermission(
		ctx,
		security.System(),
		groupID,
		code,
	); err != nil {
		return err
	}
	return writeResult(streams.Out, map[string]any{
		"revoked":    true,
		"group_id":   groupID,
		"permission": code,
	}, nil)
}

type permissionsCommand struct {
	application Application
}

func (*permissionsCommand) Name() string        { return "permissions" }
func (*permissionsCommand) Description() string { return "manage permission catalog and guest grants" }
func (*permissionsCommand) RequiresBoot() bool  { return true }

func (c *permissionsCommand) Run(
	ctx context.Context,
	args []string,
	streams console.IO,
) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := fmt.Fprintln(
			streams.Out,
			"Usage: console permissions <list|guest-grant|guest-revoke|guest-list> [flags]",
		)
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("permissions list does not accept arguments")
		}
		codes := c.application.Authorization().Codes()
		return writeResult(
			streams.Out,
			codes,
			nil,
		)
	case "guest-list":
		if len(args) != 1 {
			return errors.New(
				"permissions guest-list does not accept arguments",
			)
		}
		items, err := c.application.Authorization().GuestPermissions(
			ctx,
			security.System(),
		)
		return writeResult(streams.Out, items, err)
	case "guest-grant", "guest-revoke":
		flags := newFlagSet("permissions "+args[0], streams)
		rawCode := flags.String("permission", "", "permission code")
		if err := parseFlags(flags, args[1:]); err != nil {
			return err
		}
		code := permission.Code(strings.TrimSpace(*rawCode))
		if code == "" {
			return errors.New("permission code is empty")
		}
		if args[0] == "guest-grant" {
			item, err := c.application.Authorization().GrantGuest(
				ctx,
				security.System(),
				code,
			)
			return writeResult(streams.Out, item, err)
		}
		if err := c.application.Authorization().RevokeGuest(
			ctx,
			security.System(),
			code,
		); err != nil {
			return err
		}
		return writeResult(streams.Out, map[string]any{
			"revoked":    true,
			"permission": code,
		}, nil)
	default:
		return fmt.Errorf(
			"unknown permissions subcommand %q",
			args[0],
		)
	}
}

func newFlagSet(name string, streams console.IO) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(streams.Err)
	return flags
}

func parseFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	return nil
}

func wasSet(flags *flag.FlagSet, name string) bool {
	found := false
	flags.Visit(func(item *flag.Flag) {
		if item.Name == name {
			found = true
		}
	})
	return found
}

func userID(value int64) (security.UserID, error) {
	if value <= 0 {
		return 0, errors.New("user id must be positive")
	}
	return security.UserID(value), nil
}

func parseGroupID(value int64) (group.ID, error) {
	if value <= 0 {
		return 0, errors.New("group id must be positive")
	}
	return group.ID(value), nil
}

func optionalMediaID(value string) (*media.ID, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return nil, errors.New("avatar media id must be positive")
	}
	id := media.ID(parsed)
	return &id, nil
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

type stringValues []string

func (values *stringValues) String() string {
	if values == nil {
		return ""
	}
	return strings.Join(*values, ",")
}

func (values *stringValues) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func selectGroups(
	requested []string,
	available []group.Group,
) ([]group.ID, []group.Group, error) {
	byCode := make(map[string]group.Group, len(available))
	availableLabels := make([]string, 0, len(available))
	for _, item := range available {
		byCode[item.Code] = item
		availableLabels = append(
			availableLabels,
			fmt.Sprintf("%s (%s)", item.Code, item.Name),
		)
	}
	sort.Strings(availableLabels)

	ids := make([]group.ID, 0, len(requested))
	selected := make([]group.Group, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, rawCode := range requested {
		code := strings.ToLower(strings.TrimSpace(rawCode))
		if code == "" {
			return nil, nil, errors.New("group code is empty")
		}
		if _, exists := seen[code]; exists {
			return nil, nil, fmt.Errorf("duplicate group code %q", code)
		}
		seen[code] = struct{}{}

		item, exists := byCode[code]
		if !exists {
			if len(availableLabels) == 0 {
				return nil, nil, errors.New("no groups are available")
			}
			return nil, nil, fmt.Errorf(
				"unknown group %q; available groups: %s",
				code,
				strings.Join(availableLabels, ", "),
			)
		}
		ids = append(ids, item.ID)
		selected = append(selected, item)
	}
	return ids, selected, nil
}

func generatePassword(entropy io.Reader) (string, error) {
	if entropy == nil {
		return "", errors.New("password entropy source is nil")
	}
	const generatedPasswordBytes = 24
	random := make([]byte, generatedPasswordBytes)
	if _, err := io.ReadFull(entropy, random); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func readPassword(input io.Reader) (string, error) {
	if input == nil {
		return "", errors.New("password stdin is nil")
	}
	reader := bufio.NewReader(input)
	password, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	password = strings.TrimSuffix(password, "\n")
	password = strings.TrimSuffix(password, "\r")
	if password == "" {
		return "", errors.New("password from stdin is empty")
	}
	return password, nil
}

func writeResult(output io.Writer, value any, err error) error {
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

var _ console.Provider = (*Provider)(nil)
var _ console.RequiresBoot = (*usersCommand)(nil)
var _ console.RequiresBoot = (*groupsCommand)(nil)
var _ console.RequiresBoot = (*permissionsCommand)(nil)
