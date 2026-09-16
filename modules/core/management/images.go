package management

import (
	"context"
	"errors"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

// ConfigureImages installs application-owned image commands and prebuilt,
// profile-bound thumbnail services. Files themselves have global identity.
func (m *Files) ConfigureImages(editor *media.ImageService, thumbnails map[string]*image.Thumbnails) {
	m.images = editor
	m.thumbnails = thumbnails
}
func (m *Files) thumbnailService(profile string) (*image.Thumbnails, error) {
	if profile == "" && len(m.thumbnails) == 1 {
		for _, service := range m.thumbnails {
			return service, nil
		}
	}
	service, ok := m.thumbnails[profile]
	if !ok {
		return nil, errors.Join(ErrValidation, errors.New("thumbnail profile must be selected"))
	}
	return service, nil
}

func (m *Files) Thumbnail(ctx context.Context, actor security.Actor, id file.ID, spec image.ThumbnailSpec) (image.Result, error) {
	service, err := m.thumbnailService("")
	if err != nil {
		return image.Result{}, err
	}
	return service.Get(ctx, actor, id, spec)
}
