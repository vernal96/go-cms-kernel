package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	userpostgres "github.com/vernal96/go-cms-kernel/modules/core/user/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type Repository struct {
	connector *connectorpostgres.Connector
}

func NewRepository(
	connector *connectorpostgres.Connector,
) (*Repository, error) {
	if connector == nil {
		return nil, errors.New("group postgres connector is nil")
	}
	if connector.Pool() == nil {
		return nil, errors.New("group postgres pool is nil")
	}
	return &Repository{connector: connector}, nil
}

func (r *Repository) Create(
	ctx context.Context,
	actorID *security.UserID,
	item group.Group,
	permissions []permission.Code,
	siteAccesses []group.SiteAccess,
) (_ group.Group, resultErr error) {
	if ctx == nil {
		return group.Group{}, errors.New("create group context is nil")
	}
	transaction, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return group.Group{}, fmt.Errorf("begin group create: %w", err)
	}
	defer rollbackOnError(transaction, &resultErr)()
	result, err := scanGroup(transaction.QueryRow(ctx, `
INSERT INTO core.groups
(
    code,
    name,
    is_super,
    created_by,
    updated_by
)
VALUES ($1, $2, $3, $4, $4)
RETURNING
    id, code, name, is_super,
    created_at, updated_at, created_by, updated_by;
`, item.Code, item.Name, item.IsSuper, actorID))
	if err != nil {
		return group.Group{}, translateError(err)
	}
	if err := replacePermissions(ctx, transaction, actorID, result.ID, permissions); err != nil {
		return group.Group{}, err
	}
	if err := replaceSiteAccesses(ctx, transaction, actorID, result.ID, siteAccesses); err != nil {
		return group.Group{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return group.Group{}, translateError(err)
	}
	return result, nil
}

func (r *Repository) ByID(
	ctx context.Context,
	id group.ID,
) (group.Group, error) {
	return r.queryOne(ctx, "id = $1", id)
}

func (r *Repository) ByCode(
	ctx context.Context,
	code string,
) (group.Group, error) {
	return r.queryOne(ctx, "code = $1", code)
}

func (r *Repository) queryOne(
	ctx context.Context,
	predicate string,
	value any,
) (group.Group, error) {
	if ctx == nil {
		return group.Group{}, errors.New("get group context is nil")
	}
	result, err := scanGroup(r.connector.Pool().QueryRow(
		ctx,
		`
SELECT
    id, code, name, is_super,
    created_at, updated_at, created_by, updated_by
FROM core.groups
WHERE `+predicate+`;
`,
		value,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return group.Group{}, group.ErrNotFound
	}
	if err != nil {
		return group.Group{}, err
	}
	return result, nil
}

func (r *Repository) List(
	ctx context.Context,
) ([]group.Group, error) {
	if ctx == nil {
		return nil, errors.New("list groups context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT
    id, code, name, is_super,
    created_at, updated_at, created_by, updated_by
FROM core.groups
ORDER BY code;
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]group.Group, 0)
	for rows.Next() {
		item, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *Repository) ListPage(
	ctx context.Context,
	query group.ListQuery,
) (group.Page, error) {
	if ctx == nil {
		return group.Page{}, errors.New("list group page context is nil")
	}
	offset := (query.Page - 1) * query.PerPage
	var total int
	if err := r.connector.Pool().QueryRow(ctx, `
SELECT count(*)
FROM core.groups
WHERE $1 = '' OR code ILIKE '%' || $1 || '%' OR name ILIKE '%' || $1 || '%';
`, query.Search).Scan(&total); err != nil {
		return group.Page{}, err
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT id, code, name, is_super, created_at, updated_at, created_by, updated_by
FROM core.groups
WHERE $1 = '' OR code ILIKE '%' || $1 || '%' OR name ILIKE '%' || $1 || '%'
ORDER BY code
LIMIT $2 OFFSET $3;
`, query.Search, query.PerPage, offset)
	if err != nil {
		return group.Page{}, err
	}
	defer rows.Close()
	items := make([]group.Group, 0, query.PerPage)
	for rows.Next() {
		item, err := scanGroup(rows)
		if err != nil {
			return group.Page{}, err
		}
		items = append(items, item)
	}
	return group.Page{Items: items, Total: total}, rows.Err()
}

func (r *Repository) Count(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, errors.New("group count context is nil")
	}
	var result int
	if err := r.connector.Pool().QueryRow(ctx, `SELECT count(*) FROM core.groups;`).Scan(&result); err != nil {
		return 0, fmt.Errorf("count core groups: %w", err)
	}
	return result, nil
}

func (r *Repository) Update(
	ctx context.Context,
	actorID *security.UserID,
	item group.Group,
	permissions *[]permission.Code,
	siteAccesses *[]group.SiteAccess,
) (_ group.Group, resultErr error) {
	if ctx == nil {
		return group.Group{}, errors.New("update group context is nil")
	}
	transaction, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return group.Group{}, fmt.Errorf("begin group update: %w", err)
	}
	defer rollbackOnError(transaction, &resultErr)()
	result, err := scanGroup(transaction.QueryRow(ctx, `
UPDATE core.groups
SET
    name = $2,
    is_super = $3,
    updated_at = now(),
    updated_by = $4
WHERE id = $1
RETURNING
    id, code, name, is_super,
    created_at, updated_at, created_by, updated_by;
`, item.ID, item.Name, item.IsSuper, actorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return group.Group{}, group.ErrNotFound
	}
	if err != nil {
		return group.Group{}, translateError(err)
	}
	if permissions != nil {
		if err := replacePermissions(ctx, transaction, actorID, result.ID, *permissions); err != nil {
			return group.Group{}, err
		}
	}
	if siteAccesses != nil {
		if err := replaceSiteAccesses(ctx, transaction, actorID, result.ID, *siteAccesses); err != nil {
			return group.Group{}, err
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return group.Group{}, translateError(err)
	}
	return result, nil
}

func (r *Repository) Delete(ctx context.Context, id group.ID) error {
	return r.membershipTransaction(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT user_id FROM core.user_groups WHERE group_id=$1 ORDER BY user_id`, id)
		if err != nil {
			return err
		}
		var users []security.UserID
		for rows.Next() {
			var userID security.UserID
			if err := rows.Scan(&userID); err != nil {
				rows.Close()
				return err
			}
			users = append(users, userID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var actorID *security.UserID
		if invocation := entityhooks.InvocationFrom(ctx); invocation != nil {
			actorID = invocation.Actor.AuditUserID()
		}
		for _, userID := range users {
			_, before, err := userpostgres.ReadHookState(ctx, tx, userID)
			if err != nil {
				return err
			}
			var next []group.ID
			for _, groupID := range before.GroupIDs {
				if groupID != id {
					next = append(next, groupID)
				}
			}
			if err := r.prepareMembership(ctx, tx, actorID, userID, next); err != nil {
				return err
			}
			var remains bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core.user_groups WHERE user_id=$1 AND group_id=$2)`, userID, id).Scan(&remains); err != nil {
				return err
			}
			if remains {
				return fmt.Errorf("user hook retained the deleted group")
			}
		}
		tag, err := tx.Exec(ctx, `DELETE FROM core.groups WHERE id=$1`, id)
		if err != nil {
			return translateError(err)
		}
		if tag.RowsAffected() == 0 {
			return group.ErrNotFound
		}
		return nil
	})
}

func (r *Repository) AddUser(ctx context.Context, actorID *security.UserID, groupID group.ID, userID security.UserID) (group.Membership, error) {
	var result group.Membership
	err := r.membershipTransaction(ctx, func(tx pgx.Tx) error {
		current, before, err := userpostgres.ReadHookState(ctx, tx, userID)
		if err != nil {
			return err
		}
		if current.BlockedAt != nil {
			return group.ErrInvalidReference
		}
		ids := append([]group.ID(nil), before.GroupIDs...)
		found := false
		for _, id := range ids {
			if id == groupID {
				found = true
			}
		}
		if !found {
			ids = append(ids, groupID)
		}
		if err := r.prepareMembership(ctx, tx, actorID, userID, ids); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT user_id,group_id,created_at,updated_at,created_by,updated_by FROM core.user_groups WHERE user_id=$1 AND group_id=$2`, userID, groupID).Scan(&result.UserID, &result.GroupID, &result.CreatedAt, &result.UpdatedAt, &result.CreatedBy, &result.UpdatedBy)
	})
	return result, err
}

func (r *Repository) RemoveUser(ctx context.Context, groupID group.ID, userID security.UserID) error {
	var actorID *security.UserID
	if invocation := entityhooks.InvocationFrom(ctx); invocation != nil {
		actorID = invocation.Actor.AuditUserID()
	}
	return r.changeUserGroups(ctx, actorID, userID, func(current []group.ID) ([]group.ID, error) {
		var next []group.ID
		found := false
		for _, id := range current {
			if id == groupID {
				found = true
			} else {
				next = append(next, id)
			}
		}
		if !found {
			return nil, group.ErrNotFound
		}
		return next, nil
	})
}

func (r *Repository) Members(
	ctx context.Context,
	groupID group.ID,
) ([]group.Membership, error) {
	if ctx == nil {
		return nil, errors.New("list group members context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT
    user_id, group_id,
    created_at, updated_at, created_by, updated_by
FROM core.user_groups
WHERE group_id = $1
ORDER BY user_id;
`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]group.Membership, 0)
	for rows.Next() {
		var item group.Membership
		if err := rows.Scan(
			&item.UserID,
			&item.GroupID,
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

func (r *Repository) GroupsForUser(
	ctx context.Context,
	userID security.UserID,
) ([]group.Group, error) {
	if ctx == nil {
		return nil, errors.New("list user groups context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT
    g.id, g.code, g.name, g.is_super,
    g.created_at, g.updated_at, g.created_by, g.updated_by
FROM core.user_groups ug
JOIN core.groups g ON g.id = ug.group_id
WHERE ug.user_id = $1
ORDER BY g.code;
`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]group.Group, 0)
	for rows.Next() {
		item, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *Repository) ReplaceUserGroups(ctx context.Context, actorID *security.UserID, userID security.UserID, groupIDs []group.ID) error {
	return r.changeUserGroups(ctx, actorID, userID, func(_ []group.ID) ([]group.ID, error) { return groupIDs, nil })
}

func (r *Repository) GrantPermission(
	ctx context.Context,
	actorID *security.UserID,
	groupID group.ID,
	code permission.Code,
) (group.PermissionGrant, error) {
	if ctx == nil {
		return group.PermissionGrant{}, errors.New(
			"grant group permission context is nil",
		)
	}
	var item group.PermissionGrant
	err := r.connector.Pool().QueryRow(ctx, `
INSERT INTO core.group_permissions
(
    group_id,
    permission_code,
    created_by,
    updated_by
)
VALUES ($1, $2, $3, $3)
ON CONFLICT (group_id, permission_code) DO UPDATE
SET
    updated_at = now(),
    updated_by = EXCLUDED.updated_by
RETURNING
    group_id, permission_code,
    created_at, updated_at, created_by, updated_by;
`, groupID, code, actorID).Scan(
		&item.GroupID,
		&item.Permission,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.CreatedBy,
		&item.UpdatedBy,
	)
	if err != nil {
		return group.PermissionGrant{}, translateError(err)
	}
	return item, nil
}

func (r *Repository) RevokePermission(
	ctx context.Context,
	groupID group.ID,
	code permission.Code,
) error {
	if ctx == nil {
		return errors.New("revoke group permission context is nil")
	}
	_, err := r.connector.Pool().Exec(ctx, `
DELETE FROM core.group_permissions
WHERE group_id = $1
  AND permission_code = $2;
`, groupID, code)
	return translateError(err)
}

func (r *Repository) Permissions(
	ctx context.Context,
	groupID group.ID,
) ([]group.PermissionGrant, error) {
	if ctx == nil {
		return nil, errors.New("list group permissions context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT
    group_id, permission_code,
    created_at, updated_at, created_by, updated_by
FROM core.group_permissions
WHERE group_id = $1
ORDER BY permission_code;
`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]group.PermissionGrant, 0)
	for rows.Next() {
		var item group.PermissionGrant
		if err := rows.Scan(
			&item.GroupID,
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

func (r *Repository) SiteAccesses(
	ctx context.Context,
	groupID group.ID,
) ([]group.SiteAccess, error) {
	if ctx == nil {
		return nil, errors.New("list group site access context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT
    group_id, site_id, can_view, can_edit, can_delete,
    created_at, updated_at, created_by, updated_by
FROM core.group_site_access
WHERE group_id = $1
ORDER BY site_id;
`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]group.SiteAccess, 0)
	for rows.Next() {
		var item group.SiteAccess
		if err := rows.Scan(
			&item.GroupID,
			&item.SiteID,
			&item.CanView,
			&item.CanEdit,
			&item.CanDelete,
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

func (r *Repository) EffectiveSiteIDs(
	ctx context.Context,
	userID security.UserID,
	action group.SiteAccessAction,
) ([]site.ID, error) {
	if ctx == nil {
		return nil, errors.New("effective site access context is nil")
	}
	column, err := siteAccessColumn(action)
	if err != nil {
		return nil, err
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT DISTINCT gsa.site_id
FROM core.user_groups ug
JOIN core.group_site_access gsa ON gsa.group_id = ug.group_id
WHERE ug.user_id = $1 AND `+column+`
ORDER BY gsa.site_id;
`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]site.ID, 0)
	for rows.Next() {
		var id site.ID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (r *Repository) UserHasSiteAccess(
	ctx context.Context,
	userID security.UserID,
	siteID site.ID,
	action group.SiteAccessAction,
) (bool, error) {
	if ctx == nil {
		return false, errors.New("check site access context is nil")
	}
	column, err := siteAccessColumn(action)
	if err != nil {
		return false, err
	}
	var allowed bool
	err = r.connector.Pool().QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM core.user_groups ug
    JOIN core.group_site_access gsa ON gsa.group_id = ug.group_id
    WHERE ug.user_id = $1 AND gsa.site_id = $2 AND `+column+`
);
`, userID, siteID).Scan(&allowed)
	return allowed, err
}

func replacePermissions(
	ctx context.Context,
	transaction pgx.Tx,
	actorID *security.UserID,
	groupID group.ID,
	codes []permission.Code,
) error {
	if _, err := transaction.Exec(ctx, `
DELETE FROM core.group_permissions WHERE group_id = $1;
`, groupID); err != nil {
		return translateError(err)
	}
	if len(codes) == 0 {
		return nil
	}
	raw := make([]string, len(codes))
	for index, code := range codes {
		raw[index] = string(code)
	}
	_, err := transaction.Exec(ctx, `
INSERT INTO core.group_permissions
    (group_id, permission_code, created_by, updated_by)
SELECT $1, code, $3, $3
FROM unnest($2::text[]) AS code;
`, groupID, raw, actorID)
	return translateError(err)
}

func replaceSiteAccesses(
	ctx context.Context,
	transaction pgx.Tx,
	actorID *security.UserID,
	groupID group.ID,
	items []group.SiteAccess,
) error {
	if _, err := transaction.Exec(ctx, `
DELETE FROM core.group_site_access WHERE group_id = $1;
`, groupID); err != nil {
		return translateError(err)
	}
	if len(items) == 0 {
		return nil
	}
	siteIDs := make([]int64, len(items))
	canView := make([]bool, len(items))
	canEdit := make([]bool, len(items))
	canDelete := make([]bool, len(items))
	for index, item := range items {
		siteIDs[index] = int64(item.SiteID)
		canView[index] = item.CanView
		canEdit[index] = item.CanEdit
		canDelete[index] = item.CanDelete
	}
	_, err := transaction.Exec(ctx, `
INSERT INTO core.group_site_access
    (group_id, site_id, can_view, can_edit, can_delete, created_by, updated_by)
SELECT $1, values.site_id, values.can_view, values.can_edit, values.can_delete, $6, $6
FROM unnest($2::bigint[], $3::boolean[], $4::boolean[], $5::boolean[])
    AS values(site_id, can_view, can_edit, can_delete);
`, groupID, siteIDs, canView, canEdit, canDelete, actorID)
	return translateError(err)
}

func siteAccessColumn(action group.SiteAccessAction) (string, error) {
	switch action {
	case group.SiteAccessView:
		return "gsa.can_view", nil
	case group.SiteAccessEdit:
		return "gsa.can_edit", nil
	case group.SiteAccessDelete:
		return "gsa.can_delete", nil
	default:
		return "", errors.New("invalid site access action")
	}
}

type rowScanner interface {
	Scan(...any) error
}

func scanGroup(scanner rowScanner) (group.Group, error) {
	var item group.Group
	err := scanner.Scan(
		&item.ID,
		&item.Code,
		&item.Name,
		&item.IsSuper,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.CreatedBy,
		&item.UpdatedBy,
	)
	return item, err
}

func translateError(err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return err
	}
	switch postgresError.Code {
	case pgerrcode.UniqueViolation:
		return fmt.Errorf("%w: %s", group.ErrConflict, err)
	case pgerrcode.ForeignKeyViolation, pgerrcode.CheckViolation:
		return fmt.Errorf("%w: %s", group.ErrInvalidReference, err)
	default:
		return err
	}
}

func rollbackOnError(transaction pgx.Tx, resultErr *error) func() {
	return func() {
		if resultErr == nil || *resultErr == nil {
			return
		}
		_ = transaction.Rollback(context.Background())
	}
}

var _ group.Repository = (*Repository)(nil)
var _ group.ManagementRepository = (*Repository)(nil)
var _ group.StatisticsRepository = (*Repository)(nil)

func (r *Repository) replaceUserGroupsTx(ctx context.Context, transaction pgx.Tx, actorID *security.UserID, userID security.UserID, groupIDs []group.ID) error {
	var blockedAt *time.Time
	if err := transaction.QueryRow(ctx, `
SELECT blocked_at FROM core.users WHERE id = $1 FOR UPDATE;
`, userID).Scan(&blockedAt); errors.Is(err, pgx.ErrNoRows) {
		return group.ErrInvalidReference
	} else if err != nil {
		return err
	}

	rawIDs := make([]int64, len(groupIDs))
	for index, id := range groupIDs {
		rawIDs[index] = int64(id)
	}
	var currentAdmin, requestedAdmin bool
	if err := transaction.QueryRow(ctx, `
SELECT
    EXISTS (
        SELECT 1 FROM core.user_groups ug
        JOIN core.groups g ON g.id = ug.group_id
        WHERE ug.user_id = $1 AND g.code = 'admin'
    ),
    EXISTS (
        SELECT 1 FROM core.groups g
        WHERE g.id = ANY($2::bigint[]) AND g.code = 'admin'
    );
`, userID, rawIDs).Scan(&currentAdmin, &requestedAdmin); err != nil {
		return err
	}
	if currentAdmin && !requestedAdmin && blockedAt == nil {
		var activeAdmins int
		if err := transaction.QueryRow(ctx, `
SELECT count(*)
FROM core.user_groups ug
JOIN core.groups g ON g.id = ug.group_id
JOIN core.users u ON u.id = ug.user_id
WHERE g.code = 'admin' AND u.blocked_at IS NULL;
`).Scan(&activeAdmins); err != nil {
			return err
		}
		if activeAdmins <= 1 {
			return group.ErrLastAdministrator
		}
	}

	if _, err := transaction.Exec(ctx, `
DELETE FROM core.user_groups
WHERE user_id = $1 AND NOT (group_id = ANY($2::bigint[]));
`, userID, rawIDs); err != nil {
		return translateError(err)
	}
	var assigned int
	if err := transaction.QueryRow(ctx, `
WITH requested(group_id) AS (SELECT DISTINCT unnest($2::bigint[])),
assigned AS (
    INSERT INTO core.user_groups (user_id, group_id, created_by, updated_by)
    SELECT $1, g.id, $3, $3
    FROM requested JOIN core.groups g ON g.id = requested.group_id
    ON CONFLICT (user_id, group_id) DO UPDATE
    SET updated_at = now(), updated_by = EXCLUDED.updated_by
    RETURNING group_id
)
SELECT count(*) FROM assigned;
`, userID, rawIDs, actorID).Scan(&assigned); err != nil {
		return translateError(err)
	}
	if assigned != len(groupIDs) {
		return group.ErrInvalidReference
	}
	return nil
}
