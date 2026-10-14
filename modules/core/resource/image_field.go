package resource

import (
	"context"
	"fmt"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

// ImageField resolves the trusted resource image declaration from the site's core runtime.
func ImageField(runtime *site.Runtime) (field.Definition, bool) {
	if runtime == nil {
		return field.Definition{}, false
	}
	options := runtime.Profile().Profile().Settings.FileSystem.ResourceImage
	if options == nil {
		return field.Definition{}, false
	}
	return field.Definition{Key: "image_media_id", Label: "Изображение", Type: field.TypeMedia, Options: *options}, true
}

func (s *Service) validateImageField(ctx context.Context, actor security.Actor, runtime *site.Runtime, id media.ID) error {
	definition, ok := ImageField(runtime)
	if !ok {
		return fmt.Errorf("%w: resource image field is unavailable", ErrInvalidReference)
	}
	options, err := field.DecodeOptions[field.MediaOptions](definition.Options)
	if err != nil {
		return err
	}
	return s.validateFileReferences(ctx, actor, []field.FileReference{{Key: definition.Key, ID: int64(id), Options: options}}, nil)
}
