package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/forms"
)

func (r *Repository) ClaimExecution(ctx context.Context, siteID site.ID, id forms.ActionExecutionID, maxAttempts int) (_ forms.ExecutionWork, claimed bool, resultErr error) {
	if maxAttempts < 1 {
		return forms.ExecutionWork{}, false, forms.ErrInvalid
	}
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return forms.ExecutionWork{}, false, err
	}
	defer rollback(ctx, tx, &resultErr)

	var execution forms.ActionExecution
	var trigger []byte
	err = tx.QueryRow(ctx, `UPDATE forms.action_executions
SET status='running',attempt_count=attempt_count+1,safe_error='',started_at=clock_timestamp(),finished_at=NULL,updated_at=clock_timestamp()
WHERE site_id=$1 AND id=$2
  AND (status IN ('pending','retryable') OR (status='running' AND updated_at <= clock_timestamp()-interval '10 minutes'))
  AND attempt_count < $3
RETURNING `+executionColumns+`;`, siteID, id, maxAttempts).Scan(
		&execution.ID, &execution.SiteID, &execution.ResultID, &execution.ActionID,
		&execution.ActionCode, &execution.ActionName, &execution.ActionType, &trigger,
		&execution.Config, &execution.Status, &execution.AttemptCount, &execution.SafeError,
		&execution.ExternalReference, &execution.StartedAt, &execution.FinishedAt,
		&execution.CreatedAt, &execution.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		command, terminalErr := tx.Exec(ctx, `UPDATE forms.action_executions
SET status='failed',safe_error='execution lease expired',finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE site_id=$1 AND id=$2
  AND (status IN ('pending','retryable') OR (status='running' AND updated_at <= clock_timestamp()-interval '10 minutes'))
  AND attempt_count >= $3;`, siteID, id, maxAttempts)
		if terminalErr != nil {
			return forms.ExecutionWork{}, false, terminalErr
		}
		var status forms.ExecutionStatus
		queryErr := tx.QueryRow(ctx, `SELECT status FROM forms.action_executions WHERE site_id=$1 AND id=$2;`, siteID, id).Scan(&status)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return forms.ExecutionWork{}, false, forms.ErrNotFound
		}
		if queryErr != nil {
			return forms.ExecutionWork{}, false, queryErr
		}
		if err := tx.Commit(ctx); err != nil {
			return forms.ExecutionWork{}, false, err
		}
		if command.RowsAffected() == 0 && status == forms.ExecutionRunning {
			return forms.ExecutionWork{}, false, forms.ErrExecutionBusy
		}
		return forms.ExecutionWork{}, false, nil
	}
	if err != nil {
		return forms.ExecutionWork{}, false, err
	}
	if err := json.Unmarshal(trigger, &execution.Trigger); err != nil {
		return forms.ExecutionWork{}, false, err
	}
	result, err := scanResult(tx.QueryRow(ctx, `SELECT `+resultColumns+` FROM forms.results r JOIN forms.statuses s ON s.id=r.status_id WHERE r.site_id=$1 AND r.id=$2;`, siteID, execution.ResultID))
	if err != nil {
		return forms.ExecutionWork{}, false, err
	}
	values, err := listResultValues(ctx, tx, execution.ResultID)
	if err != nil {
		return forms.ExecutionWork{}, false, err
	}
	uploads, err := listResultUploads(ctx, tx, execution.ResultID)
	if err != nil {
		return forms.ExecutionWork{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return forms.ExecutionWork{}, false, err
	}
	return forms.ExecutionWork{Execution: execution, Result: result, Values: values, Uploads: uploads}, true, nil
}

func (r *Repository) FinishExecution(ctx context.Context, id forms.ActionExecutionID, status forms.ExecutionStatus, safeError, externalReference string) error {
	if status != forms.ExecutionSucceeded && status != forms.ExecutionRetryable && status != forms.ExecutionFailed {
		return forms.ErrInvalid
	}
	command, err := r.connector.Pool().Exec(ctx, `UPDATE forms.action_executions
SET status=$2,safe_error=$3,external_reference=$4,
    finished_at=CASE WHEN $2='retryable' THEN NULL ELSE clock_timestamp() END,
    updated_at=clock_timestamp()
WHERE id=$1 AND status='running';`, id, status, safeError, externalReference)
	if err != nil {
		return mapWriteError(err)
	}
	if command.RowsAffected() != 1 {
		return forms.ErrConflict
	}
	return nil
}

func (r *Repository) HasActiveExecutions(ctx context.Context, siteID site.ID) (bool, error) {
	var result bool
	err := r.connector.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM forms.action_executions WHERE site_id=$1 AND status IN ('pending','running','retryable'));`, siteID).Scan(&result)
	return result, err
}

func (r *Repository) ResultHasActiveSubmittedExecutions(ctx context.Context, siteID site.ID, resultID forms.ResultID) (bool, error) {
	var result bool
	err := r.connector.Pool().QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM forms.action_executions
WHERE site_id=$1 AND result_id=$2 AND trigger->>'type'=$3 AND status IN ('pending','running','retryable')
);`, siteID, resultID, forms.TriggerSubmitted).Scan(&result)
	return result, err
}

func (r *Repository) MarkUploadSpoolDeleted(ctx context.Context, siteID site.ID, resultID forms.ResultID, references []string) error {
	if len(references) == 0 {
		return nil
	}
	_, err := r.connector.Pool().Exec(ctx, `UPDATE forms.result_uploads u
SET spool_deleted_at=coalesce(spool_deleted_at,clock_timestamp())
FROM forms.results r
WHERE u.result_id=r.id AND r.site_id=$1 AND r.id=$2 AND u.spool_reference=ANY($3);`, siteID, resultID, references)
	return err
}

func (r *Repository) MarkUploadSpoolReferencesDeleted(ctx context.Context, siteID site.ID, references []string) error {
	if len(references) == 0 {
		return nil
	}
	_, err := r.connector.Pool().Exec(ctx, `UPDATE forms.result_uploads u
SET spool_deleted_at=coalesce(spool_deleted_at,clock_timestamp())
FROM forms.results r
WHERE u.result_id=r.id AND r.site_id=$1 AND u.spool_reference=ANY($2);`, siteID, references)
	return err
}

func (r *Repository) MarkAllUploadSpoolDeleted(ctx context.Context, siteID site.ID) error {
	_, err := r.connector.Pool().Exec(ctx, `UPDATE forms.result_uploads u
SET spool_deleted_at=coalesce(spool_deleted_at,clock_timestamp())
FROM forms.results r
WHERE u.result_id=r.id AND r.site_id=$1 AND u.spool_reference IS NOT NULL AND u.spool_deleted_at IS NULL;`, siteID)
	return err
}

func (r *Repository) ActiveSpoolReferences(ctx context.Context, siteID site.ID, references []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if len(references) == 0 {
		return result, nil
	}
	rows, err := r.connector.Pool().Query(ctx, `SELECT u.spool_reference
FROM forms.result_uploads u
JOIN forms.results r ON r.id=u.result_id
WHERE r.site_id=$1 AND u.spool_reference=ANY($2) AND u.spool_deleted_at IS NULL
  AND EXISTS(SELECT 1 FROM forms.action_executions e WHERE e.result_id=r.id AND e.trigger->>'type'=$3 AND e.status IN ('pending','running','retryable'));`, siteID, references, forms.TriggerSubmitted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var reference string
		if err := rows.Scan(&reference); err != nil {
			return nil, err
		}
		result[reference] = struct{}{}
	}
	return result, rows.Err()
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return forms.ErrNotFound
	}
	return err
}

func mapWriteError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return forms.ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case pgerrcode.UniqueViolation:
			return forms.ErrConflict
		case pgerrcode.ForeignKeyViolation, pgerrcode.CheckViolation, pgerrcode.NotNullViolation:
			return forms.ErrInvalid
		}
	}
	return err
}

var _ forms.Repository = (*Repository)(nil)
