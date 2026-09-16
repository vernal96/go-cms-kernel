package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vernal96/go-cms-kernel/domainevent"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/messageid"
)

// Source uses module-owned tables in the same database as the entity mutation.
type Source struct {
	pool                *pgxpool.Pool
	name, calls, outbox string
}

func NewSource(pool *pgxpool.Pool, name, schema string) *Source {
	return &Source{pool: pool, name: name, calls: pgx.Identifier{schema, "entity_hook_calls"}.Sanitize(), outbox: pgx.Identifier{schema, "outbox_messages"}.Sanitize()}
}
func (s *Source) Name() string { return s.name }

// Append receives the adapter's actual transaction, never opens another one.
func (s *Source) Append(ctx context.Context, tx pgx.Tx, event domainevent.Envelope, targets []entityhooks.Target, key []byte) error {
	message, err := domainevent.Message(event, key)
	if err != nil {
		return err
	}
	if message.Key == nil {
		message.Key = []byte{}
	}
	if len(targets) > 0 {
		message.Headers[entityhooks.SourceHeader] = []byte(s.name)
	}
	headers, err := json.Marshal(message.Headers)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if _, err := tx.Exec(ctx, `INSERT INTO `+s.calls+` (event_id,event_name,event_body,module,handler,scope,scope_id) VALUES ($1,$2,$3::jsonb,$4,$5,$6,$7)`, event.ID, event.Name, string(message.Body), target.Module, target.Handler, target.Scope, target.ScopeID); err != nil {
			return fmt.Errorf("append entity hook call: %w", err)
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO `+s.outbox+` (message_id,topic,message_key,body,headers) VALUES ($1,$2,$3,$4,$5::jsonb)`, event.ID, message.Topic, message.Key, message.Body, string(headers))
	return err
}

func (s *Source) Calls(ctx context.Context, id messageid.ID) ([]entityhooks.Call, error) {
	rows, err := s.pool.Query(ctx, `SELECT event_body,module,handler,scope,scope_id,completed_at IS NOT NULL FROM `+s.calls+` WHERE event_id=$1 ORDER BY module,handler,scope,scope_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var calls []entityhooks.Call
	for rows.Next() {
		var call entityhooks.Call
		var raw []byte
		if err := rows.Scan(&raw, &call.Target.Module, &call.Target.Handler, &call.Target.Scope, &call.Target.ScopeID, &call.Completed); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &call.Event); err != nil {
			return nil, err
		}
		call.Source = s.name
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

func args(d entityhooks.Delivery) []any {
	return []any{d.Event.ID, d.Target.Module, d.Target.Handler, d.Target.Scope, d.Target.ScopeID}
}

const identity = `event_id=$1 AND module=$2 AND handler=$3 AND scope=$4 AND scope_id=$5`

func (s *Source) Claim(ctx context.Context, d entityhooks.Delivery, owner string, lease time.Duration) (bool, error) {
	if owner == "" || lease <= 0 {
		return false, errors.New("invalid entity hook lease")
	}
	a := append(args(d), owner, lease.Microseconds())
	result, err := s.pool.Exec(ctx, `UPDATE `+s.calls+` SET lease_owner=$6,lease_until=clock_timestamp()+($7::bigint*interval '1 microsecond'),attempt_count=attempt_count+1 WHERE `+identity+` AND completed_at IS NULL AND (lease_until IS NULL OR lease_until<=clock_timestamp())`, a...)
	return result.RowsAffected() == 1, err
}
func (s *Source) Complete(ctx context.Context, d entityhooks.Delivery, owner string) error {
	result, err := s.pool.Exec(ctx, `UPDATE `+s.calls+` SET completed_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,last_error=NULL WHERE `+identity+` AND lease_owner=$6 AND lease_until>clock_timestamp() AND completed_at IS NULL`, append(args(d), owner)...)
	if err == nil && result.RowsAffected() != 1 {
		return errors.New("entity hook lease was lost")
	}
	return err
}
func (s *Source) Fail(ctx context.Context, d entityhooks.Delivery, owner, lastError string) error {
	result, err := s.pool.Exec(ctx, `UPDATE `+s.calls+` SET lease_owner=NULL,lease_until=NULL,last_error=$7 WHERE `+identity+` AND lease_owner=$6 AND completed_at IS NULL`, append(args(d), owner, lastError)...)
	if err == nil && result.RowsAffected() != 1 {
		return errors.New("entity hook lease was lost")
	}
	return err
}
func (s *Source) Pending(ctx context.Context) ([]entityhooks.PendingTarget, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT event_name,module,handler,scope,scope_id FROM `+s.calls+` WHERE completed_at IS NULL ORDER BY event_name,module,handler,scope,scope_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []entityhooks.PendingTarget
	for rows.Next() {
		var item entityhooks.PendingTarget
		if err := rows.Scan(&item.Name, &item.Target.Module, &item.Target.Handler, &item.Target.Scope, &item.Target.ScopeID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *Source) Cleanup(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	if retention <= 0 || limit < 1 {
		return 0, errors.New("invalid entity hook cleanup")
	}
	// Retain the whole event while any recipient remains pending. Otherwise a
	// redelivery could repeat completed recipients removed by early cleanup.
	result, err := s.pool.Exec(ctx, `WITH candidates AS (SELECT c.ctid FROM `+s.calls+` c WHERE c.completed_at<clock_timestamp()-($1::bigint*interval '1 microsecond') AND NOT EXISTS (SELECT 1 FROM `+s.calls+` pending WHERE pending.event_id=c.event_id AND pending.completed_at IS NULL) ORDER BY c.completed_at LIMIT $2 FOR UPDATE OF c SKIP LOCKED) DELETE FROM `+s.calls+` c USING candidates WHERE c.ctid=candidates.ctid`, retention.Microseconds(), limit)
	return result.RowsAffected(), err
}
