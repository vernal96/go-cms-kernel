package forms

import (
	"context"
	"fmt"

	"github.com/vernal96/go-cms-kernel/security"
)

func (s *Service) ListResults(ctx context.Context, actor security.Actor, query ResultQuery) (ResultSummaryPage, []FormField, error) {
	if err := s.authorizer.Check(ctx, actor, ResultReadPermission); err != nil {
		return ResultSummaryPage{}, nil, err
	}
	page, err := normalizePage(query.PageQuery)
	if err != nil {
		return ResultSummaryPage{}, nil, err
	}
	query.PageQuery = page
	columns := []FormField{}
	codes := []string{}
	if query.FormID > 0 {
		detail, detailErr := s.repository.FormDetail(ctx, s.siteID, query.FormID)
		if detailErr != nil {
			return ResultSummaryPage{}, nil, detailErr
		}
		for _, item := range fieldsByResultPosition(detail.Fields) {
			if item.ShowInResults && item.Type != FieldTypeCaptcha && item.Type != FieldTypeUpload {
				columns = append(columns, item)
				codes = append(codes, item.Code)
			}
		}
	}
	result, err := s.repository.ListResults(ctx, s.siteID, query, codes)
	return result, columns, err
}

func (s *Service) ResultDetail(ctx context.Context, actor security.Actor, id ResultID) (ResultDetail, error) {
	if err := s.authorizer.Check(ctx, actor, ResultReadPermission); err != nil {
		return ResultDetail{}, err
	}
	result, err := s.repository.ResultDetail(ctx, s.siteID, id)
	if err != nil {
		return ResultDetail{}, err
	}
	form, err := s.repository.FormDetail(ctx, s.siteID, result.Result.FormID)
	if err != nil {
		return ResultDetail{}, err
	}
	result.AvailableStatuses = append([]Status(nil), form.Statuses...)
	return result, nil
}

func (s *Service) ChangeResultStatus(ctx context.Context, actor security.Actor, resultID ResultID, targetID StatusID) (ResultDetail, error) {
	if err := s.authorizer.Check(ctx, actor, ResultUpdatePermission); err != nil {
		return ResultDetail{}, err
	}
	current, err := s.repository.ResultDetail(ctx, s.siteID, resultID)
	if err != nil {
		return ResultDetail{}, err
	}
	detail, err := s.repository.FormDetail(ctx, s.siteID, current.Result.FormID)
	if err != nil {
		return ResultDetail{}, err
	}
	target, exists := statusByID(detail.Statuses, targetID)
	if !exists {
		return ResultDetail{}, fmt.Errorf("%w: target status does not belong to result form", ErrInvalid)
	}
	if current.Result.StatusID == targetID {
		current.AvailableStatuses = append([]Status(nil), detail.Statuses...)
		return current, nil
	}
	trigger := Trigger{Type: TriggerStatusChanged, From: current.Result.StatusCode, To: target.Code}
	actions := matchingActions(detail.Actions, trigger)
	var updated ResultDetail
	err = s.lifecycle.withActive(func() error {
		var changeErr error
		updated, changeErr = s.repository.ChangeResultStatus(ctx, ResultStatusChange{
			SiteID: s.siteID, ResultID: resultID, FromStatusID: current.Result.StatusID,
			ToStatusID: targetID, Actions: actions,
		})
		return changeErr
	})
	if err == nil {
		updated.AvailableStatuses = append([]Status(nil), detail.Statuses...)
		s.logQueuedActions(ctx, updated.Executions)
	}
	return updated, err
}

func (s *Service) DeleteResult(ctx context.Context, actor security.Actor, id ResultID) error {
	if err := s.authorizer.Check(ctx, actor, ResultDeletePermission); err != nil {
		return err
	}
	keys, err := s.repository.DeleteResult(ctx, s.siteID, id)
	if err != nil {
		return err
	}
	return s.deleteSpoolReferences(context.WithoutCancel(ctx), keys)
}
