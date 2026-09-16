package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/messageid"
	"github.com/vernal96/go-cms-kernel/outbox"
)

type outboxSource struct {
	connector *connectorpostgres.Connector
	name      string
}

func newOutboxSource(connector *connectorpostgres.Connector) *outboxSource {
	return &outboxSource{connector: connector, name: "core:" + string(connector.Code())}
}

func (s *outboxSource) Name() string { return s.name }

func (s *outboxSource) Claim(ctx context.Context, claim outbox.Claim) (_ []outbox.Record, resultErr error) {
	if ctx == nil || strings.TrimSpace(claim.Owner) == "" || claim.Limit < 1 || claim.LeaseDuration <= 0 {
		return nil, errors.New("PostgreSQL outbox claim is invalid")
	}
	leaseMicroseconds := intervalMicroseconds(claim.LeaseDuration)
	tx, err := s.connector.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(context.Background()); resultErr != nil && rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()
	rows, err := tx.Query(ctx, `
WITH source_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now
), candidates AS (
    SELECT candidate.message_id
    FROM core.outbox_messages AS candidate
    CROSS JOIN source_clock
    WHERE published_at IS NULL
      AND available_at <= source_clock.now
      AND (lease_until IS NULL OR lease_until <= source_clock.now)
    ORDER BY available_at, created_at, message_id
    FOR UPDATE OF candidate SKIP LOCKED
    LIMIT $1
)
UPDATE core.outbox_messages AS message
SET lease_owner=$2,
    lease_until=source_clock.now + ($3::bigint * interval '1 microsecond')
FROM candidates
CROSS JOIN source_clock
WHERE message.message_id=candidates.message_id
RETURNING message.message_id,message.topic,message.message_key,message.body,message.headers,
          message.created_at,message.available_at,message.attempt_count,coalesce(message.last_error,''),
          message.lease_owner,message.lease_until,message.published_at;`, claim.Limit, claim.Owner, leaseMicroseconds)
	if err != nil {
		return nil, fmt.Errorf("claim outbox messages: %w", err)
	}
	defer rows.Close()
	records := make([]outbox.Record, 0, claim.Limit)
	for rows.Next() {
		var record outbox.Record
		var rawHeaders []byte
		if err := rows.Scan(&record.MessageID, &record.Topic, &record.Key, &record.Body, &rawHeaders,
			&record.CreatedAt, &record.AvailableAt, &record.AttemptCount, &record.LastError,
			&record.LeaseOwner, &record.LeaseUntil, &record.PublishedAt); err != nil {
			return nil, fmt.Errorf("scan outbox message: %w", err)
		}
		if err := json.Unmarshal(rawHeaders, &record.Headers); err != nil {
			return nil, fmt.Errorf("decode outbox message %q headers: %w", record.MessageID, err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox messages: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit outbox claim: %w", err)
	}
	return records, nil
}

func (s *outboxSource) MarkPublished(ctx context.Context, id messageid.ID, owner string) error {
	command, err := s.connector.Pool().Exec(ctx, `
UPDATE core.outbox_messages
SET published_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,last_error=NULL
WHERE message_id=$1 AND lease_owner=$2 AND published_at IS NULL;`, id, owner)
	if err != nil {
		return fmt.Errorf("mark outbox message published: %w", err)
	}
	if command.RowsAffected() != 1 {
		return outbox.ErrLeaseLost
	}
	return nil
}

func (s *outboxSource) MarkFailed(ctx context.Context, id messageid.ID, owner, failure string, retryAfter time.Duration) error {
	if retryAfter <= 0 {
		return errors.New("outbox retry delay is invalid")
	}
	if len(failure) > 4096 {
		failure = failure[:4096]
	}
	command, err := s.connector.Pool().Exec(ctx, `
UPDATE core.outbox_messages
SET attempt_count=attempt_count+1,last_error=$3,
    available_at=clock_timestamp() + ($4::bigint * interval '1 microsecond'),
    lease_owner=NULL,lease_until=NULL
WHERE message_id=$1 AND lease_owner=$2 AND published_at IS NULL;`, id, owner, failure, intervalMicroseconds(retryAfter))
	if err != nil {
		return fmt.Errorf("mark outbox message failed: %w", err)
	}
	if command.RowsAffected() != 1 {
		return outbox.ErrLeaseLost
	}
	return nil
}

func (s *outboxSource) CleanupPublished(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	if retention < 0 || limit < 1 {
		return 0, errors.New("outbox cleanup request is invalid")
	}
	command, err := s.connector.Pool().Exec(ctx, `
WITH source_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now
), candidates AS (
    SELECT candidate.message_id
    FROM core.outbox_messages AS candidate
    CROSS JOIN source_clock
    WHERE published_at IS NOT NULL
      AND published_at <= source_clock.now - ($1::bigint * interval '1 microsecond')
    ORDER BY published_at,message_id
    LIMIT $2
)
DELETE FROM core.outbox_messages AS message
USING candidates
WHERE message.message_id=candidates.message_id;`, intervalMicroseconds(retention), limit)
	if err != nil {
		return 0, fmt.Errorf("cleanup published outbox messages: %w", err)
	}
	return command.RowsAffected(), nil
}

func intervalMicroseconds(value time.Duration) int64 {
	// PostgreSQL timestamps have microsecond precision. Round positive partial
	// microseconds up so a configured delay never becomes immediate.
	microseconds := value / time.Microsecond
	if value%time.Microsecond != 0 {
		microseconds++
	}
	return int64(microseconds)
}

var _ outbox.Source = (*outboxSource)(nil)
