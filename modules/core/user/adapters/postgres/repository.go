package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
)

type Repository struct {
	connector *connectorpostgres.Connector
}

func NewRepository(
	connector *connectorpostgres.Connector,
) (*Repository, error) {
	if connector == nil {
		return nil, errors.New("user postgres connector is nil")
	}
	if connector.Pool() == nil {
		return nil, errors.New("user postgres pool is nil")
	}
	return &Repository{connector: connector}, nil
}

func (r *Repository) Create(
	ctx context.Context,
	actorID *security.UserID,
	record user.Record,
	groupIDs []group.ID,
	validate user.ValidateAvatarMedia,
) (_ user.Record, resultErr error) {
	if ctx == nil {
		return user.Record{}, errors.New("create user context is nil")
	}
	transaction, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return user.Record{}, fmt.Errorf("begin user create: %w", err)
	}
	defer rollbackOnError(transaction, &resultErr)()

	record, groupIDs, err = user.PrepareMutation(ctx, nil, record, groupIDs)
	if err != nil {
		return user.Record{}, err
	}
	if record.AvatarMediaID != nil {
		if validate == nil {
			return user.Record{}, errors.New("avatar validator is nil")
		}
		if err := medialock.Lock(
			ctx,
			transaction,
			*record.AvatarMediaID,
		); err != nil {
			return user.Record{}, err
		}
		if err := ensureMediaAvailable(
			ctx,
			transaction,
			*record.AvatarMediaID,
			0,
		); err != nil {
			return user.Record{}, err
		}
		if err := validate(ctx, *record.AvatarMediaID); err != nil {
			return user.Record{}, err
		}
	}

	created, err := scanRecord(transaction.QueryRow(ctx, `
INSERT INTO core.users
(
    login,
    email,
    password_hash,
    name,
    last_name,
    middle_name,
    phone,
    avatar_media_id,
    color_scheme,
    accent_color,
    created_by,
    updated_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
RETURNING
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by;
`,
		record.Login,
		record.Email,
		record.PasswordHash,
		record.Name,
		record.LastName,
		record.MiddleName,
		record.Phone,
		record.AvatarMediaID,
		record.ColorScheme,
		record.AccentColor,
		actorID,
	))
	if err != nil {
		return user.Record{}, translateError(err)
	}

	if err := createGroupMemberships(
		ctx,
		transaction,
		actorID,
		created.ID,
		groupIDs,
	); err != nil {
		return user.Record{}, err
	}

	if err := r.appendMutation(ctx, transaction, nil, created.ID); err != nil {
		return user.Record{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return user.Record{}, translateError(err)
	}
	return created, nil
}

func createGroupMemberships(
	ctx context.Context,
	transaction pgx.Tx,
	actorID *security.UserID,
	userID security.UserID,
	groupIDs []group.ID,
) error {
	if len(groupIDs) == 0 {
		return nil
	}

	rawGroupIDs := make([]int64, len(groupIDs))
	seen := make(map[group.ID]struct{}, len(groupIDs))
	for index, groupID := range groupIDs {
		if groupID <= 0 {
			return group.ErrNotFound
		}
		if _, exists := seen[groupID]; exists {
			return fmt.Errorf("duplicate group id %d", groupID)
		}
		seen[groupID] = struct{}{}
		rawGroupIDs[index] = int64(groupID)
	}

	var created int64
	err := transaction.QueryRow(ctx, `
WITH requested(group_id) AS (
    SELECT DISTINCT unnest($3::bigint[])
),
created AS (
    INSERT INTO core.user_groups
    (
        user_id,
        group_id,
        created_by,
        updated_by
    )
    SELECT
        $1,
        groups.id,
        $2,
        $2
    FROM requested
    JOIN core.groups groups ON groups.id = requested.group_id
    RETURNING group_id
)
SELECT count(*) FROM created;
`, userID, actorID, rawGroupIDs).Scan(&created)
	if err != nil {
		return translateError(err)
	}
	if created != int64(len(groupIDs)) {
		return group.ErrNotFound
	}
	return nil
}

func (r *Repository) ByID(
	ctx context.Context,
	id user.ID,
) (user.Record, error) {
	if ctx == nil {
		return user.Record{}, errors.New("get user context is nil")
	}
	record, err := scanRecord(r.connector.Pool().QueryRow(ctx, `
SELECT
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by
FROM core.users
WHERE id = $1;
`, id))
	return translateRecordResult(record, err)
}

func (r *Repository) ByIdentifier(
	ctx context.Context,
	identifier string,
) (user.Record, error) {
	if ctx == nil {
		return user.Record{}, errors.New("get auth user context is nil")
	}
	record, err := scanRecord(r.connector.Pool().QueryRow(ctx, `
SELECT
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by
FROM core.users
WHERE login = $1 OR email = $1
LIMIT 1;
`, identifier))
	return translateRecordResult(record, err)
}

func (r *Repository) List(
	ctx context.Context,
) ([]user.Record, error) {
	if ctx == nil {
		return nil, errors.New("list users context is nil")
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by
FROM core.users
ORDER BY id;
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]user.Record, 0)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (r *Repository) ListPage(
	ctx context.Context,
	query user.ListQuery,
) (user.Page, error) {
	if ctx == nil {
		return user.Page{}, errors.New("list user page context is nil")
	}
	offset := (query.Page - 1) * query.PerPage
	predicate := `
($1 = '' OR login ILIKE '%' || $1 || '%' OR email ILIKE '%' || $1 || '%'
 OR concat_ws(' ', last_name, name, middle_name) ILIKE '%' || $1 || '%')
AND ($2 = 'all' OR ($2 = 'active' AND blocked_at IS NULL)
 OR ($2 = 'blocked' AND blocked_at IS NOT NULL))`
	var total int
	if err := r.connector.Pool().QueryRow(ctx, `
SELECT count(*) FROM core.users WHERE `+predicate+`;
`, query.Search, query.Status).Scan(&total); err != nil {
		return user.Page{}, err
	}
	rows, err := r.connector.Pool().Query(ctx, `
SELECT
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by
FROM core.users
WHERE `+predicate+`
ORDER BY id DESC
LIMIT $3 OFFSET $4;
`, query.Search, query.Status, query.PerPage, offset)
	if err != nil {
		return user.Page{}, err
	}
	defer rows.Close()
	items := make([]user.User, 0, query.PerPage)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return user.Page{}, err
		}
		items = append(items, user.Clone(record.User))
	}
	return user.Page{Items: items, Total: total}, rows.Err()
}

func (r *Repository) Statistics(ctx context.Context) (user.Statistics, error) {
	if ctx == nil {
		return user.Statistics{}, errors.New("user statistics context is nil")
	}
	var result user.Statistics
	err := r.connector.Pool().QueryRow(ctx, `
SELECT
    count(*),
    count(*) FILTER (WHERE blocked_at IS NULL),
    count(*) FILTER (WHERE blocked_at IS NOT NULL)
FROM core.users;
`).Scan(&result.Total, &result.Active, &result.Blocked)
	if err != nil {
		return user.Statistics{}, fmt.Errorf("count core user statistics: %w", err)
	}
	return result, nil
}

func (r *Repository) Update(
	ctx context.Context,
	actorID *security.UserID,
	expected user.Record,
	next user.Record,
	validate user.ValidateAvatarMedia,
) (_ user.Record, resultErr error) {
	if ctx == nil {
		return user.Record{}, errors.New("update user context is nil")
	}
	transaction, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return user.Record{}, fmt.Errorf("begin user update: %w", err)
	}
	defer rollbackOnError(transaction, &resultErr)()

	locked, err := scanRecord(transaction.QueryRow(ctx, `
SELECT
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by
FROM core.users
WHERE id = $1
FOR UPDATE;
`, next.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return user.Record{}, user.ErrNotFound
	}
	if err != nil {
		return user.Record{}, err
	}

	if !locked.UpdatedAt.Equal(expected.UpdatedAt) {
		return user.Record{}, user.ErrConflict
	}
	_, hookBefore, err := ReadHookState(ctx, transaction, next.ID)
	if err != nil {
		return user.Record{}, err
	}
	next, _, err = user.PrepareMutation(ctx, &hookBefore, next, hookBefore.GroupIDs)
	if err != nil {
		return user.Record{}, err
	}
	avatarChanged := !sameMediaID(
		locked.AvatarMediaID,
		next.AvatarMediaID,
	)
	if avatarChanged {
		if validate == nil && next.AvatarMediaID != nil {
			return user.Record{}, errors.New("avatar validator is nil")
		}
		ids := make([]media.ID, 0, 2)
		if locked.AvatarMediaID != nil {
			ids = append(ids, *locked.AvatarMediaID)
		}
		if next.AvatarMediaID != nil {
			ids = append(ids, *next.AvatarMediaID)
		}
		if err := medialock.Lock(ctx, transaction, ids...); err != nil {
			return user.Record{}, err
		}
		if next.AvatarMediaID != nil {
			if err := ensureMediaAvailable(
				ctx,
				transaction,
				*next.AvatarMediaID,
				next.ID,
			); err != nil {
				return user.Record{}, err
			}
			if err := validate(ctx, *next.AvatarMediaID); err != nil {
				return user.Record{}, err
			}
		}
	}

	updated, err := scanRecord(transaction.QueryRow(ctx, `
UPDATE core.users
SET
    login = $2,
    email = $3,
    name = $4,
    last_name = $5,
    middle_name = $6,
    phone = $7,
    avatar_media_id = $8,
    color_scheme = $9,
    accent_color = $10,
    updated_at = now(),
    updated_by = $11
WHERE id = $1
RETURNING
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by;
`,
		next.ID,
		next.Login,
		next.Email,
		next.Name,
		next.LastName,
		next.MiddleName,
		next.Phone,
		next.AvatarMediaID,
		next.ColorScheme,
		next.AccentColor,
		actorID,
	))
	if err != nil {
		return user.Record{}, translateError(err)
	}

	if avatarChanged && locked.AvatarMediaID != nil {
		if _, err := transaction.Exec(ctx, `
DELETE FROM core.media
WHERE id = $1;
`, *locked.AvatarMediaID); err != nil {
			return user.Record{}, translateError(err)
		}
	}

	if err := r.appendMutation(ctx, transaction, &hookBefore, updated.ID); err != nil {
		return user.Record{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return user.Record{}, translateError(err)
	}
	return updated, nil
}

func (r *Repository) ChangePassword(
	ctx context.Context,
	actorID *security.UserID,
	id user.ID,
	passwordHash string,
) (user.Record, error) {
	if ctx == nil {
		return user.Record{}, errors.New("change password context is nil")
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return user.Record{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	current, hookBefore, err := ReadHookState(ctx, tx, id)
	if err != nil {
		return user.Record{}, err
	}
	current.PasswordHash = passwordHash
	next, _, err := user.PrepareMutation(ctx, &hookBefore, current, hookBefore.GroupIDs)
	if err != nil {
		return user.Record{}, err
	}
	passwordHash = next.PasswordHash

	record, err := scanRecord(tx.QueryRow(ctx, `
UPDATE core.users
SET
    password_hash = $2,
    session_version = session_version + 1,
    updated_at = now(),
    updated_by = $3
WHERE id = $1
RETURNING
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by;
`, id, passwordHash, actorID))
	if err != nil {
		return user.Record{}, translateError(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM core.auth_sessions WHERE user_id=$1", id); err != nil {
		return user.Record{}, err
	}
	if err := r.appendMutation(ctx, tx, &hookBefore, record.ID); err != nil {
		return user.Record{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return user.Record{}, translateError(err)
	}
	return record, nil
}

func (r *Repository) RecordLogin(
	ctx context.Context,
	id user.ID,
	expectedHash string,
	passwordHash *string,
) (user.Record, error) {
	if ctx == nil {
		return user.Record{}, errors.New("record login context is nil")
	}
	record, err := scanRecord(r.connector.Pool().QueryRow(ctx, `
UPDATE core.users
SET
    password_hash = COALESCE($2, password_hash),
    last_login_at = now(),
    updated_at = now(),
    updated_by = id
WHERE id = $1
  AND blocked_at IS NULL AND password_hash = $3
RETURNING
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by;
`, id, passwordHash, expectedHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return user.Record{}, user.ErrInvalidCredentials
	}
	return record, translateError(err)
}

func (r *Repository) Block(
	ctx context.Context,
	actorID *security.UserID,
	id user.ID,
) (_ user.Record, resultErr error) {
	if ctx == nil {
		return user.Record{}, errors.New("block user context is nil")
	}
	transaction, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return user.Record{}, fmt.Errorf("begin user block: %w", err)
	}
	defer rollbackOnError(transaction, &resultErr)()

	if _, err := transaction.Exec(ctx, "LOCK TABLE core.users IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return user.Record{}, fmt.Errorf("lock user administration: %w", err)
	}
	if _, err := transaction.Exec(ctx, "LOCK TABLE core.user_groups IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return user.Record{}, fmt.Errorf("lock user administration: %w", err)
	}

	current, err := scanRecord(transaction.QueryRow(ctx, `
SELECT
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by
FROM core.users
WHERE id = $1
FOR UPDATE;
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return user.Record{}, user.ErrNotFound
	}
	if err != nil {
		return user.Record{}, err
	}
	_, hookBefore, err := ReadHookState(ctx, transaction, id)
	if err != nil {
		return user.Record{}, err
	}
	if _, _, err := user.PrepareMutation(ctx, &hookBefore, current, hookBefore.GroupIDs); err != nil {
		return user.Record{}, err
	}
	if current.BlockedAt == nil {
		var (
			isAdministrator bool
			activeAdmins    int
		)
		if err := transaction.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM core.user_groups ug
    JOIN core.groups g ON g.id = ug.group_id
    WHERE ug.user_id = $1 AND g.code = 'admin'
);
`, id).Scan(&isAdministrator); err != nil {
			return user.Record{}, err
		}
		if isAdministrator {
			if err := transaction.QueryRow(ctx, `
SELECT count(*)
FROM core.user_groups ug
JOIN core.groups g ON g.id = ug.group_id
JOIN core.users u ON u.id = ug.user_id
WHERE g.code = 'admin' AND u.blocked_at IS NULL;
`).Scan(&activeAdmins); err != nil {
				return user.Record{}, err
			}
			if activeAdmins <= 1 {
				return user.Record{}, user.ErrLastAdministrator
			}
		}
	}

	record, err := scanRecord(transaction.QueryRow(ctx, `
UPDATE core.users
SET
    blocked_at = COALESCE(blocked_at, now()),
    blocked_by = COALESCE(blocked_by, $2),
    updated_at = now(),
    updated_by = $2
WHERE id = $1
RETURNING
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by;
`, id, actorID))
	if err != nil {
		return user.Record{}, translateError(err)
	}
	if err := r.appendMutation(ctx, transaction, &hookBefore, record.ID); err != nil {
		return user.Record{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return user.Record{}, translateError(err)
	}
	return record, nil
}

func (r *Repository) Unblock(
	ctx context.Context,
	actorID *security.UserID,
	id user.ID,
) (user.Record, error) {
	if ctx == nil {
		return user.Record{}, errors.New("unblock user context is nil")
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return user.Record{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	current, hookBefore, err := ReadHookState(ctx, tx, id)
	if err != nil {
		return user.Record{}, err
	}
	next, _, err := user.PrepareMutation(ctx, &hookBefore, current, hookBefore.GroupIDs)
	if err != nil {
		return user.Record{}, err
	}
	_ = next

	record, err := scanRecord(tx.QueryRow(ctx, `
UPDATE core.users
SET
    blocked_at = NULL,
    blocked_by = NULL,
    updated_at = now(),
    updated_by = $2
WHERE id = $1
RETURNING
    id, login, email, password_hash, session_version, name,
    last_name, middle_name, phone, avatar_media_id, color_scheme, accent_color,
    last_login_at, created_at, updated_at, blocked_at,
    created_by, updated_by, blocked_by;
`, id, actorID))
	if err != nil {
		return user.Record{}, translateError(err)
	}
	if err := r.appendMutation(ctx, tx, &hookBefore, record.ID); err != nil {
		return user.Record{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return user.Record{}, translateError(err)
	}
	return record, nil
}

func ensureMediaAvailable(
	ctx context.Context,
	transaction pgx.Tx,
	id media.ID,
	excludeUserID user.ID,
) error {
	var (
		exists       bool
		resourceUsed bool
		userUsed     bool
	)
	err := transaction.QueryRow(ctx, `
SELECT
    EXISTS (
        SELECT 1 FROM core.media WHERE id = $1
    ),
    EXISTS (
        SELECT 1
        FROM core.resources
        WHERE image_media_id = $1
        UNION ALL SELECT 1 FROM core.library_items WHERE image_media_id = $1
        UNION ALL SELECT 1 FROM core.resource_media_references WHERE media_id = $1
    ),
    EXISTS (
        SELECT 1
        FROM core.users
        WHERE avatar_media_id = $1
          AND id <> $2
    );
`, id, excludeUserID).Scan(
		&exists,
		&resourceUsed,
		&userUsed,
	)
	if err != nil {
		return fmt.Errorf("query avatar media availability: %w", err)
	}
	if !exists {
		return user.ErrInvalidReference
	}
	if resourceUsed || userUsed {
		return media.ErrAlreadyAttached
	}
	return nil
}

func sameMediaID(left, right *media.ID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type rowScanner interface {
	Scan(...any) error
}

func scanRecord(scanner rowScanner) (user.Record, error) {
	var record user.Record
	err := scanner.Scan(
		&record.ID,
		&record.Login,
		&record.Email,
		&record.PasswordHash,
		&record.SessionVersion,
		&record.Name,
		&record.LastName,
		&record.MiddleName,
		&record.Phone,
		&record.AvatarMediaID,
		&record.ColorScheme,
		&record.AccentColor,
		&record.LastLoginAt,
		&record.CreatedAt,
		&record.UpdatedAt,
		&record.BlockedAt,
		&record.CreatedBy,
		&record.UpdatedBy,
		&record.BlockedBy,
	)
	return record, err
}

func translateRecordResult(
	record user.Record,
	err error,
) (user.Record, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return user.Record{}, user.ErrNotFound
	}
	if err != nil {
		return user.Record{}, err
	}
	return record, nil
}

func rollbackOnError(
	transaction pgx.Tx,
	resultErr *error,
) func() {
	return func() {
		if *resultErr == nil {
			return
		}
		err := transaction.Rollback(context.Background())
		if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			*resultErr = errors.Join(*resultErr, err)
		}
	}
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
		switch postgresError.ConstraintName {
		case "uq_users_login":
			return fmt.Errorf("%w: %s", user.ErrLoginExists, err)
		case "uq_users_email":
			return fmt.Errorf("%w: %s", user.ErrEmailExists, err)
		}
		return fmt.Errorf("%w: %s", user.ErrConflict, err)
	case pgerrcode.ForeignKeyViolation, pgerrcode.CheckViolation:
		return fmt.Errorf("%w: %s", user.ErrInvalidReference, err)
	default:
		return err
	}
}

var _ user.Repository = (*Repository)(nil)
var _ user.ManagementRepository = (*Repository)(nil)
var _ user.StatisticsRepository = (*Repository)(nil)
