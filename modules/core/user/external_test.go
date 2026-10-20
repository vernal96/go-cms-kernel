package user

import (
	"context"
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/security"
)

func TestSocialOnlyAccountCannotAuthenticateOrChangePasswordAsLocalCredential(t *testing.T) {
	service, repository, hasher := newService(t)
	repository.records[1] = Record{User: User{ID: 1, Login: "external-user", Name: "Person"}}
	_, err := service.Authenticate(context.Background(), AuthenticateInput{
		Identifier: "external-user", Password: "dummy-password",
	})
	if !errors.Is(err, ErrInvalidCredentials) || hasher.verifyCalls != 1 {
		t.Fatal("account without password did not perform dummy verification and reject", err)
	}
	_, err = service.ChangeCurrentPassword(context.Background(), security.User(1),
		"dummy-password", "new-valid-password")
	if !errors.Is(err, ErrInvalidCurrentPassword) || hasher.hashCalls != 0 {
		t.Fatal("missing current password established a local credential", err)
	}
}
