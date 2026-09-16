package resource

import (
	"context"
	"fmt"
	"strconv"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

func (s *Service) validateMediaFields(ctx context.Context, actor security.Actor, values []field.StoredValue) error {
	for _, value := range values {
		references, err := value.MediaReferences()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidReference, err)
		}
		for _, reference := range references {
			id := reference.ID
			path := []string{value.Key}
			if value.Multiple {
				path = append(path, strconv.Itoa(value.Position))
			}
			key := field.ReferenceKey(append(path, reference.Path...))
			resolved, err := s.media.Resolve(ctx, actor, media.ID(id))
			if err != nil {
				return fmt.Errorf("resolve Media field %q: %w", key, err)
			}
			if err := ValidateImageMediaFile(ctx, resolved.File, media.Usage{Kind: ImageMediaUsage}); err != nil {
				return err
			}
		}
	}
	return nil
}
