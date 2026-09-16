package admin

import (
	"context"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

// A profile command resolves the authenticated owner first. Only that owner's
// current avatar is eligible for the internal image sub-operation.
func (m *Management) profileImageID(ctx context.Context, actor security.Actor) (media.ID, error) {
	current, err := m.users.Current(ctx, actor)
	if err != nil {
		return 0, err
	}
	if current.AvatarMediaID == nil {
		return 0, media.ErrNotFound
	}
	return *current.AvatarMediaID, nil
}

func (m *Management) ProfileImageState(ctx context.Context, actor security.Actor) (media.ImageState, error) {
	id, err := m.profileImageID(ctx, actor)
	if err != nil {
		return media.ImageState{}, err
	}
	return m.images.State(ctx, security.System(), id)
}
func (m *Management) EditProfileImage(ctx context.Context, actor security.Actor, expected time.Time, transform image.TransformOptions) (media.ImageState, error) {
	id, err := m.profileImageID(ctx, actor)
	if err != nil {
		return media.ImageState{}, err
	}
	return m.images.Edit(ctx, security.System(), id, expected, transform)
}
func (m *Management) RestoreProfileImage(ctx context.Context, actor security.Actor, expected time.Time) (media.ImageState, error) {
	id, err := m.profileImageID(ctx, actor)
	if err != nil {
		return media.ImageState{}, err
	}
	return m.images.Restore(ctx, security.System(), id, expected)
}
func (m *Management) OpenProfileImageSource(ctx context.Context, actor security.Actor) (file.OpenedFile, error) {
	state, err := m.ProfileImageState(ctx, actor)
	if err != nil {
		return file.OpenedFile{}, err
	}
	return m.files.Open(ctx, security.System(), state.Original.ID)
}
func (m *Management) ProfileThumbnail(ctx context.Context, actor security.Actor) (image.Result, error) {
	state, err := m.ProfileImageState(ctx, actor)
	if err != nil {
		return image.Result{}, err
	}
	return m.imageFiles.Thumbnail(ctx, security.System(), state.Current.ID, image.ThumbnailSpec{Width: 128, Height: 128})
}
