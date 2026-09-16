package file

import (
	"context"
	"errors"
	"fmt"

	"github.com/vernal96/go-cms-kernel/security"
)

type DeletePolicy string

const DeleteSafe DeletePolicy = "safe"
const DeleteConfirmedMediaCascade DeletePolicy = "confirmed_media_cascade"

type DeleteImpact struct {
	ResourceSites       []int64 `json:"-"`
	SelectedCount       int     `json:"selected_count"`
	TotalFiles          int     `json:"total_files"`
	DerivedFiles        int     `json:"derived_files"`
	MediaReferences     int     `json:"media_references"`
	FileFieldReferences int     `json:"file_field_references"`
	Token               string  `json:"token"`
}
type CascadeRepository interface {
	DeleteImpact(context.Context, []ItemReference) (DeleteImpact, error)
	DeleteConfirmed(context.Context, *security.UserID, []ItemReference, string, DeletePhysical) error
}
type CascadeService interface {
	DeleteImpact(context.Context, security.Actor, []ItemReference) (DeleteImpact, error)
	DeleteConfirmed(context.Context, security.Actor, []ItemReference, string) error
}

func (s *service) DeleteImpact(ctx context.Context, actor security.Actor, items []ItemReference) (DeleteImpact, error) {
	if err := validateContext(ctx, "delete impact"); err != nil {
		return DeleteImpact{}, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return DeleteImpact{}, err
	}
	if err := s.authorizer.Check(ctx, actor, deletePermission); err != nil {
		return DeleteImpact{}, err
	}
	if len(items) == 0 || len(items) > 1000 {
		return DeleteImpact{}, ErrInvalidInput
	}
	if err := validateItemReferences(items); err != nil {
		return DeleteImpact{}, err
	}
	repo, ok := s.repository.(CascadeRepository)
	if !ok {
		return DeleteImpact{}, errors.New("delete impact repository unavailable")
	}
	return repo.DeleteImpact(ctx, items)
}
func (s *service) DeleteConfirmed(ctx context.Context, actor security.Actor, items []ItemReference, token string) error {
	if err := validateContext(ctx, "confirmed delete"); err != nil {
		return err
	}
	if err := s.authorizer.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	if token == "" || len(items) == 0 || len(items) > 1000 {
		return ErrInvalidInput
	}
	if err := validateItemReferences(items); err != nil {
		return err
	}
	repo, ok := s.repository.(CascadeRepository)
	if !ok {
		return errors.New("cascade repository unavailable")
	}
	if err := repo.DeleteConfirmed(ctx, actor.AuditUserID(), items, token, s.deletePhysical); err != nil {
		return fmt.Errorf("confirmed filesystem delete: %w", err)
	}
	return nil
}
