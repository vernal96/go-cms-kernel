package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type Repository struct {
	connector *connectorpostgres.Connector
}

func (r *Repository) IsAdministrator(ctx context.Context, userID security.UserID) (bool, error) {
	var allowed bool
	err := r.connector.Pool().QueryRow(ctx, `
SELECT EXISTS (
 SELECT 1 FROM core.user_groups ug JOIN core.groups g ON g.id=ug.group_id
 WHERE ug.user_id=$1 AND g.code=$2
);`, userID, group.AdminCode).Scan(&allowed)
	return allowed, err
}

func NewRepository(
	connector *connectorpostgres.Connector,
) (*Repository, error) {
	if connector == nil {
		return nil, errors.New("access postgres connector is nil")
	}
	if connector.Pool() == nil {
		return nil, errors.New("access postgres pool is nil")
	}
	return &Repository{connector: connector}, nil
}

func (r *Repository) Subject(
	ctx context.Context,
	userID security.UserID,
) (access.Subject, error) {
	if ctx == nil {
		return access.Subject{}, errors.New("access subject context is nil")
	}

	var subject access.Subject
	err := r.connector.Pool().QueryRow(ctx, `
SELECT
    blocked_at IS NULL,
    EXISTS (
        SELECT 1
        FROM core.user_groups
        WHERE user_id = core.users.id
    ),
    EXISTS (
        SELECT 1
        FROM core.user_groups ug
        JOIN core.groups g ON g.id = ug.group_id
        WHERE ug.user_id = core.users.id
          AND g.is_super
    )
FROM core.users
WHERE id = $1;
`, userID).Scan(
		&subject.Active,
		&subject.HasGroups,
		&subject.IsSuper,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return access.Subject{}, nil
	}
	if err != nil {
		return access.Subject{}, fmt.Errorf(
			"query authorization subject %d: %w",
			userID,
			err,
		)
	}
	subject.Exists = true
	return subject, nil
}

func (r *Repository) Authorization(ctx context.Context, userID *security.UserID, codes []permission.Code) (access.Authorization, error) {
	if ctx == nil {
		return access.Authorization{}, errors.New("authorization context is nil")
	}
	requested := make([]string, len(codes))
	for i, code := range codes {
		requested[i] = string(code)
	}
	var facts access.Authorization
	var groupCodes, guestCodes []string
	err := r.connector.Pool().QueryRow(ctx, `
WITH subject AS (
 SELECT id, blocked_at IS NULL AS active FROM core.users WHERE id=$1
), memberships AS (
 SELECT ug.group_id, g.is_super FROM core.user_groups ug
 JOIN core.groups g ON g.id=ug.group_id WHERE ug.user_id=$1
)
SELECT
 EXISTS (SELECT 1 FROM subject),
 COALESCE((SELECT active FROM subject), false),
 EXISTS (SELECT 1 FROM memberships),
 EXISTS (SELECT 1 FROM memberships WHERE is_super),
 ARRAY(SELECT DISTINCT gp.permission_code FROM core.group_permissions gp
       JOIN memberships m ON m.group_id=gp.group_id
       WHERE gp.permission_code=ANY($2::text[])),
 ARRAY(SELECT permission_code FROM core.guest_permissions
       WHERE permission_code=ANY($2::text[]));`, userID, requested).Scan(
		&facts.Subject.Exists, &facts.Subject.Active, &facts.Subject.HasGroups, &facts.Subject.IsSuper,
		&groupCodes, &guestCodes)
	if err != nil {
		return access.Authorization{}, fmt.Errorf("query authorization: %w", err)
	}
	for _, code := range groupCodes {
		facts.GroupPermissions = append(facts.GroupPermissions, permission.Code(code))
	}
	for _, code := range guestCodes {
		facts.GuestPermissions = append(facts.GuestPermissions, permission.Code(code))
	}
	return facts, nil
}

func (r *Repository) GuestPermissions(
	ctx context.Context,
) ([]access.Grant, error) {
	if ctx == nil {
		return nil, errors.New("list guest permissions context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT
    permission_code,
    created_at,
    updated_at,
    created_by,
    updated_by
FROM core.guest_permissions
ORDER BY permission_code;
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]access.Grant, 0)
	for rows.Next() {
		var item access.Grant
		if err := rows.Scan(
			&item.Permission,
			&item.CreatedAt,
			&item.UpdatedAt,
			&item.CreatedBy,
			&item.UpdatedBy,
		); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *Repository) GrantGuest(
	ctx context.Context,
	actorID *security.UserID,
	code permission.Code,
) (access.Grant, error) {
	if ctx == nil {
		return access.Grant{}, errors.New("grant guest context is nil")
	}
	var item access.Grant
	err := r.connector.Pool().QueryRow(ctx, `
INSERT INTO core.guest_permissions
(
    permission_code,
    created_by,
    updated_by
)
VALUES ($1, $2, $2)
ON CONFLICT (permission_code) DO UPDATE
SET
    updated_at = now(),
    updated_by = EXCLUDED.updated_by
RETURNING
    permission_code,
    created_at,
    updated_at,
    created_by,
    updated_by;
`, code, actorID).Scan(
		&item.Permission,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.CreatedBy,
		&item.UpdatedBy,
	)
	if err != nil {
		return access.Grant{}, err
	}
	return item, nil
}

func (r *Repository) RevokeGuest(
	ctx context.Context,
	code permission.Code,
) error {
	if ctx == nil {
		return errors.New("revoke guest context is nil")
	}
	_, err := r.connector.Pool().Exec(ctx, `
DELETE FROM core.guest_permissions
WHERE permission_code = $1;
`, code)
	return err
}

var _ access.Repository = (*Repository)(nil)
