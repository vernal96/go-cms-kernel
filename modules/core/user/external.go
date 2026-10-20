package user

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"

	"github.com/vernal96/go-cms-kernel/security"
)

var ErrRegistrationDisabled = errors.New("public registration is disabled")

// ExternalInput is trusted provider output, never a public request DTO.
// Linking requires a previously authenticated user and its credential version.
type ExternalInput struct {
	Provider           string
	Namespace          string
	Subject            string
	Email              string
	Name               string
	AllowRegistration  bool
	LinkUserID         *ID
	LinkSessionVersion int64
}

type ExternalIdentityRepository interface {
	AuthenticateExternal(context.Context, ExternalInput, Record) (Record, error)
}

func (s *ApplicationService) AuthenticateExternal(
	ctx context.Context,
	input ExternalInput,
) (User, error) {
	if ctx == nil {
		return User{}, errors.New("external authentication context is nil")
	}
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	if input.Provider == "" || len(input.Provider) > 64 ||
		input.Namespace == "" || len(input.Namespace) > 512 ||
		input.Subject == "" || len(input.Subject) > 512 ||
		strings.TrimSpace(input.Subject) != input.Subject {
		return User{}, ErrInvalidCredentials
	}
	if input.LinkUserID != nil && (*input.LinkUserID <= 0 || input.LinkSessionVersion <= 0) {
		return User{}, security.ErrUnauthenticated
	}
	repository, ok := s.repository.(ExternalIdentityRepository)
	if !ok {
		return User{}, errors.New("external identity repository is unavailable")
	}
	// No manufactured email or password: a provider may omit both credentials.
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "Пользователь"
	}
	candidate, err := normalize(Record{User: User{
		Login: "external-" + strings.ToLower(rand.Text()),
		Email: input.Email,
		Name:  name,
	}})
	if err != nil {
		return User{}, err
	}
	actor := security.Guest()
	if input.LinkUserID != nil {
		actor = security.User(*input.LinkUserID)
	}
	ctx, finish, err := s.beginMutation(ctx, actor, OperationExternal, "")
	if err != nil {
		return User{}, err
	}
	defer finish()
	record, err := repository.AuthenticateExternal(ctx, input, candidate)
	if err != nil {
		return User{}, err
	}
	return Clone(record.User), nil
}
