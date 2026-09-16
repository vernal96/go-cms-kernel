package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/medialock"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

type Repository struct {
	connector *connectorpostgres.Connector
}

func NewRepository(
	connector *connectorpostgres.Connector,
) (*Repository, error) {
	if connector == nil {
		return nil, errors.New("postgres connector is nil")
	}
	if connector.Pool() == nil {
		return nil, errors.New("postgres pool is nil")
	}
	return &Repository{connector: connector}, nil
}

func (r *Repository) Create(
	ctx context.Context,
	actorID *security.UserID,
	item media.Media,
) (media.Media, error) {
	if ctx == nil {
		return media.Media{}, errors.New("create media context is nil")
	}

	rawParams, err := json.Marshal(item.Params)
	if err != nil {
		return media.Media{}, fmt.Errorf("encode media params: %w", err)
	}

	result, err := scanMedia(r.connector.Pool().QueryRow(ctx, `
INSERT INTO core.media
(
    file_id,
    title,
    params,
    created_by,
    updated_by
)
VALUES ($1, $2, $3::jsonb, $4, $4)
RETURNING
    id, file_id, title, params,
    created_at, updated_at, created_by, updated_by;
`, item.FileID, item.Title, string(rawParams), actorID))
	if err != nil {
		return media.Media{}, translateError(err)
	}
	return result, nil
}

func (r *Repository) ByID(
	ctx context.Context,
	id media.ID,
) (media.Media, error) {
	if ctx == nil {
		return media.Media{}, errors.New("get media context is nil")
	}

	result, err := scanMedia(r.connector.Pool().QueryRow(ctx, `
SELECT
    id, file_id, title, params,
    created_at, updated_at, created_by, updated_by
FROM core.media
WHERE id = $1;
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return media.Media{}, media.ErrNotFound
	}
	if err != nil {
		return media.Media{}, fmt.Errorf("query media %d: %w", id, err)
	}
	return result, nil
}

func (r *Repository) Update(
	ctx context.Context,
	actorID *security.UserID,
	item media.Media,
	validate media.ValidateUsages,
) (_ media.Media, resultErr error) {
	return r.update(ctx, actorID, item, validate, nil)
}
func (r *Repository) UpdateImage(ctx context.Context, actorID *security.UserID, item media.Media, expected time.Time, validate media.ValidateUsages) (media.Media, error) {
	return r.update(ctx, actorID, item, validate, &expected)
}
func (r *Repository) update(ctx context.Context, actorID *security.UserID, item media.Media, validate media.ValidateUsages, expected *time.Time) (_ media.Media, resultErr error) {
	if ctx == nil {
		return media.Media{}, errors.New("update media context is nil")
	}
	if validate == nil {
		return media.Media{}, errors.New("media usage validator is nil")
	}

	transaction, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return media.Media{}, fmt.Errorf("begin media update: %w", err)
	}
	defer func() {
		if resultErr == nil {
			return
		}
		rollbackErr := transaction.Rollback(context.Background())
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()

	if err := medialock.Lock(ctx, transaction, item.ID); err != nil {
		return media.Media{}, err
	}

	var lockedID media.ID
	var updatedAt time.Time
	if err := transaction.QueryRow(ctx, `
SELECT id, updated_at
FROM core.media
WHERE id = $1
FOR UPDATE;
`, item.ID).Scan(&lockedID, &updatedAt); errors.Is(err, pgx.ErrNoRows) {
		return media.Media{}, media.ErrNotFound
	} else if err != nil {
		return media.Media{}, fmt.Errorf("lock media %d: %w", item.ID, err)
	}

	if expected != nil && !updatedAt.Equal(*expected) {
		return media.Media{}, media.ErrImageConflict
	}
	usages, err := mediaUsages(ctx, transaction, item.ID)
	if err != nil {
		return media.Media{}, err
	}
	if err := validate(ctx, usages); err != nil {
		return media.Media{}, err
	}

	rawParams, err := json.Marshal(item.Params)
	if err != nil {
		return media.Media{}, fmt.Errorf("encode media params: %w", err)
	}

	updated, err := scanMedia(transaction.QueryRow(ctx, `
UPDATE core.media
SET
    file_id = $2,
    title = $3,
    params = $4::jsonb,
    updated_at = clock_timestamp(),
    updated_by = $5
WHERE id = $1
RETURNING
    id, file_id, title, params,
    created_at, updated_at, created_by, updated_by;
`, item.ID, item.FileID, item.Title, string(rawParams), actorID))
	if err != nil {
		return media.Media{}, translateError(err)
	}

	if err := transaction.Commit(ctx); err != nil {
		return media.Media{}, translateError(err)
	}
	return updated, nil
}

func (r *Repository) Delete(
	ctx context.Context,
	id media.ID,
) (_ error) {
	if ctx == nil {
		return errors.New("delete media context is nil")
	}

	transaction, err := r.connector.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin media delete: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = transaction.Rollback(context.Background())
	}()

	if err := medialock.Lock(ctx, transaction, id); err != nil {
		return err
	}
	commandTag, err := transaction.Exec(ctx, `
DELETE FROM core.media
WHERE id = $1;
`, id)
	if err != nil {
		return translateError(err)
	}
	if commandTag.RowsAffected() == 0 {
		return media.ErrNotFound
	}
	if err := transaction.Commit(ctx); err != nil {
		return translateError(err)
	}
	committed = true
	return nil
}

func mediaUsages(
	ctx context.Context,
	transaction pgx.Tx,
	id media.ID,
) ([]media.Usage, error) {
	rows, err := transaction.Query(ctx, `
SELECT kind, owner_id
FROM
(
    SELECT 'resource.image'::text AS kind, resource_id AS owner_id
    FROM core.resource_media_references WHERE media_id = $1
    UNION ALL
    SELECT
        'resource.image'::text AS kind,
        id AS owner_id
    FROM core.resources
    WHERE image_media_id = $1

    UNION ALL

    SELECT
		'resource.image'::text AS kind,
		id AS owner_id
	FROM core.library_items
	WHERE image_media_id = $1

    UNION ALL

    SELECT
        'user.avatar'::text AS kind,
        id AS owner_id
    FROM core.users
    WHERE avatar_media_id = $1
) AS usages
ORDER BY kind, owner_id;
`, id)
	if err != nil {
		return nil, fmt.Errorf("query media %d usages: %w", id, err)
	}
	defer rows.Close()

	result := make([]media.Usage, 0)
	for rows.Next() {
		var (
			kind    media.UsageKind
			ownerID int64
		)
		if err := rows.Scan(&kind, &ownerID); err != nil {
			return nil, fmt.Errorf("scan media usage: %w", err)
		}
		result = append(result, media.Usage{
			Kind:    kind,
			OwnerID: ownerID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate media usages: %w", err)
	}
	return result, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanMedia(scanner rowScanner) (media.Media, error) {
	var (
		item      media.Media
		rawParams []byte
	)
	if err := scanner.Scan(
		&item.ID,
		&item.FileID,
		&item.Title,
		&rawParams,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.CreatedBy,
		&item.UpdatedBy,
	); err != nil {
		return media.Media{}, err
	}

	item.Params = make(map[string]any)
	if len(rawParams) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(rawParams))
		decoder.UseNumber()
		if err := decoder.Decode(&item.Params); err != nil {
			return media.Media{}, fmt.Errorf(
				"decode params for media %d: %w",
				item.ID,
				err,
			)
		}
	}
	return item, nil
}

func translateError(err error) error {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return err
	}

	switch postgresError.Code {
	case pgerrcode.ForeignKeyViolation:
		return fmt.Errorf("%w: %s", media.ErrInvalidReference, err)
	default:
		return err
	}
}

var _ media.Repository = (*Repository)(nil)
