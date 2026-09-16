package forms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

const ExecuteActionJobName = "forms.execute_action"

type Worker struct {
	siteID      site.ID
	repository  Repository
	registry    *actionRegistry
	spool       *UploadSpool
	lifecycle   *runtimeLifecycle
	maxAttempts int
	logger      *slog.Logger
}

func newWorker(siteID site.ID, repository Repository, registry *actionRegistry, spool *UploadSpool, lifecycle *runtimeLifecycle, maxAttempts int, logger *slog.Logger) (*Worker, error) {
	if siteID <= 0 || repository == nil || registry == nil || lifecycle == nil || maxAttempts < 1 {
		return nil, errors.New("Forms worker dependencies are invalid")
	}
	return &Worker{siteID: siteID, repository: repository, registry: registry, spool: spool, lifecycle: lifecycle, maxAttempts: maxAttempts, logger: logger}, nil
}

func (w *Worker) Handle(ctx context.Context, item job.Envelope) error {
	if item.Name != ExecuteActionJobName || item.SchemaVersion != 1 || item.ScopeID != fmt.Sprint(w.siteID) {
		return errors.New("Forms action job envelope is invalid")
	}
	var payload struct {
		ExecutionID ActionExecutionID `json:"action_execution_id"`
	}
	if json.Unmarshal(item.Payload, &payload) != nil || payload.ExecutionID <= 0 {
		return errors.New("Forms action job payload is invalid")
	}
	var work ExecutionWork
	var claimed bool
	err := w.lifecycle.withActive(func() error {
		var claimErr error
		work, claimed, claimErr = w.repository.ClaimExecution(ctx, w.siteID, payload.ExecutionID, w.maxAttempts)
		return claimErr
	})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil || !claimed {
		return err
	}
	actionType, exists := w.registry.Type(work.Execution.ActionType)
	if !exists {
		return w.finishTerminal(ctx, work, "action_unavailable", fmt.Errorf("%w: %s", ErrActionUnavailable, work.Execution.ActionType))
	}
	accessor := &executionUploads{spool: w.spool, items: work.Uploads}
	result, executeErr := actionType.Execute(ctx, ActionExecutionContext{Execution: work.Execution, Result: work.Result, Values: work.Values, Uploads: accessor}, work.Execution.Config)
	failure := classifyActionError(executeErr)
	if failure == nil {
		if err := w.repository.FinishExecution(ctx, work.Execution.ID, ExecutionSucceeded, "", result.ExternalReference); err != nil {
			return err
		}
		w.log(ctx, work.Execution, "forms.action.succeeded", "", false)
		w.cleanupResultUploads(ctx, work.Result.ID)
		return nil
	}
	terminal := !failure.Retryable || work.Execution.AttemptCount >= w.maxAttempts
	status := ExecutionRetryable
	if terminal {
		status = ExecutionFailed
	}
	safeError := truncateUTF8(failure.Error(), 4096)
	if err := w.repository.FinishExecution(ctx, work.Execution.ID, status, safeError, ""); err != nil {
		return errors.Join(failure, err)
	}
	w.log(ctx, work.Execution, "forms.action.failed", failure.Code, failure.Retryable && !terminal)
	if terminal {
		w.cleanupResultUploads(ctx, work.Result.ID)
		return nil
	}
	return failure
}

func classifyActionError(err error) *ActionError {
	if err == nil {
		return nil
	}
	var failure *ActionError
	if errors.As(err, &failure) && failure != nil {
		return failure
	}
	return retryableActionError("temporary_failure", err)
}

func (w *Worker) finishTerminal(ctx context.Context, work ExecutionWork, code string, err error) error {
	if finishErr := w.repository.FinishExecution(ctx, work.Execution.ID, ExecutionFailed, truncateUTF8(err.Error(), 4096), ""); finishErr != nil {
		return errors.Join(err, finishErr)
	}
	w.log(ctx, work.Execution, "forms.action.failed", code, false)
	w.cleanupResultUploads(ctx, work.Result.ID)
	return nil
}

func (w *Worker) cleanupResultUploads(ctx context.Context, resultID ResultID) {
	if w.spool == nil {
		return
	}
	active, err := w.repository.ResultHasActiveSubmittedExecutions(context.WithoutCancel(ctx), w.siteID, resultID)
	if err != nil || active {
		return
	}
	detail, err := w.repository.ResultDetail(context.WithoutCancel(ctx), w.siteID, resultID)
	if err != nil {
		return
	}
	keys := []string{}
	for _, item := range detail.Uploads {
		if item.SpoolDeletedAt == nil && item.SpoolReference != "" {
			if deleteErr := w.spool.Delete(context.WithoutCancel(ctx), item.SpoolReference); deleteErr == nil {
				keys = append(keys, item.SpoolReference)
			}
		}
	}
	if len(keys) > 0 {
		_ = w.repository.MarkUploadSpoolDeleted(context.WithoutCancel(ctx), w.siteID, resultID, keys)
	}
}

func (w *Worker) log(ctx context.Context, execution ActionExecution, event, code string, retryable bool) {
	if w.logger == nil {
		return
	}
	w.logger.InfoContext(ctx, "Forms action execution completed", slog.String("event", event), slog.Int64("site_id", int64(w.siteID)), slog.Int64("result_id", int64(execution.ResultID)), slog.Int64("action_execution_id", int64(execution.ID)), slog.String("action_code", execution.ActionCode), slog.Int("attempt", execution.AttemptCount), slog.String("error_code", code), slog.Bool("retryable", retryable))
}

type executionUploads struct {
	spool *UploadSpool
	items []ResultUpload
}

func (a *executionUploads) Metadata(code string) []ResultUpload {
	result := []ResultUpload{}
	for _, item := range a.items {
		if item.FieldCode == code {
			clone := item
			clone.SpoolReference = ""
			result = append(result, clone)
		}
	}
	return result
}
func (a *executionUploads) Open(ctx context.Context, code string, position int) (io.ReadCloser, error) {
	for _, item := range a.items {
		if item.FieldCode == code && item.Position == position {
			if item.SpoolDeletedAt != nil {
				return nil, errors.New("Forms upload bytes were already deleted")
			}
			return a.spool.Open(ctx, item.SpoolReference)
		}
	}
	return nil, ErrNotFound
}
