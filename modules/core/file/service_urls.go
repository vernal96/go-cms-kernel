package file

import (
	"context"
	"errors"
	"time"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/security"
)

func (s *service) URL(
	ctx context.Context,
	actor security.Actor,
	id ID,
) (string, error) {
	if err := validateContext(ctx, "create file URL"); err != nil {
		return "", err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return "", err
	}
	item, err := s.file(ctx, id)
	if err != nil {
		return "", err
	}
	disk, err := s.disk(item.Storage)
	if err != nil {
		return "", err
	}
	if disk.Visibility() != filesystem.VisibilityPublic {
		return "", filesystem.ErrInvalidVisibility
	}
	return disk.URL(ctx, reference(item))
}

func (s *service) TemporaryURL(
	ctx context.Context,
	actor security.Actor,
	id ID,
	expiresAt time.Time,
) (string, error) {
	if err := validateContext(ctx, "create temporary file URL"); err != nil {
		return "", err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return "", err
	}
	if !expiresAt.After(time.Now()) {
		return "", errors.New("temporary URL expiration must be in the future")
	}
	item, err := s.file(ctx, id)
	if err != nil {
		return "", err
	}
	disk, err := s.disk(item.Storage)
	if err != nil {
		return "", err
	}
	if disk.Visibility() != filesystem.VisibilityPrivate {
		return "", filesystem.ErrInvalidVisibility
	}
	return disk.TemporaryURL(ctx, reference(item), expiresAt)
}
