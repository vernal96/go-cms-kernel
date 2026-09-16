package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
)

func TestTranslateErrorDistinguishesIdentityConflicts(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		constraint string
		target     error
	}{
		{
			name:       "login",
			constraint: "uq_users_login",
			target:     user.ErrLoginExists,
		},
		{
			name:       "email",
			constraint: "uq_users_email",
			target:     user.ErrEmailExists,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := translateError(&pgconn.PgError{
				Code:           pgerrcode.UniqueViolation,
				ConstraintName: test.constraint,
			})
			if !errors.Is(err, test.target) ||
				!errors.Is(err, user.ErrConflict) {
				t.Fatalf("translated error = %v", err)
			}
		})
	}
}
