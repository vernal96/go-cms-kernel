package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
)

const managedAvatarParam = "profile_avatar_managed"

type ProfileAvatarDTO struct {
	MediaID   media.ID `json:"media_id"`
	FileID    file.ID  `json:"file_id"`
	Name      string   `json:"name"`
	MIMEType  string   `json:"mime_type"`
	Size      int64    `json:"size"`
	UpdatedAt string   `json:"updated_at"`
}

type ProfileDTO struct {
	ID          user.ID           `json:"id"`
	Login       string            `json:"login"`
	Email       string            `json:"email"`
	Name        string            `json:"name"`
	LastName    *string           `json:"last_name"`
	MiddleName  *string           `json:"middle_name"`
	Phone       *string           `json:"phone"`
	ColorScheme user.ColorScheme  `json:"color_scheme"`
	AccentColor user.AccentColor  `json:"accent_color"`
	Avatar      *ProfileAvatarDTO `json:"avatar"`
}

type ProfileResponse struct {
	User ProfileDTO `json:"user"`
}

func (m *Management) Profile(
	ctx context.Context,
	actor security.Actor,
) (ProfileResponse, error) {
	current, err := m.users.Current(ctx, actor)
	if err != nil {
		return ProfileResponse{}, err
	}
	return m.profileResponse(ctx, current)
}

func (m *Management) UpdateProfile(
	ctx context.Context,
	actor security.Actor,
	input user.UpdateCurrentInput,
) (ProfileResponse, error) {
	updated, err := m.users.UpdateCurrent(ctx, actor, input)
	if err != nil {
		return ProfileResponse{}, profileValidationError(err)
	}
	return m.profileResponse(ctx, updated)
}

func (m *Management) UpdateProfilePreferences(
	ctx context.Context,
	actor security.Actor,
	preferences user.Preferences,
) (ProfileResponse, error) {
	updated, err := m.users.UpdateCurrentPreferences(ctx, actor, preferences)
	if err != nil {
		return ProfileResponse{}, profileValidationError(err)
	}
	return m.profileResponse(ctx, updated)
}

func (m *Management) ChangeProfilePassword(
	ctx context.Context,
	actor security.Actor,
	currentPassword string,
	newPassword string,
) error {
	_, err := m.users.ChangeCurrentPassword(
		ctx,
		actor,
		currentPassword,
		newPassword,
	)
	return profileValidationError(err)
}

func (m *Management) SelectProfileAvatar(
	ctx context.Context,
	actor security.Actor,
	fileID file.ID,
) (ProfileResponse, error) {
	linkedFile, err := m.files.GetFile(ctx, actor, fileID)
	if err != nil {
		return ProfileResponse{}, fileValidationError(err)
	}
	if !profileAvatarMIMEAllowed(linkedFile.MIMEType) {
		return ProfileResponse{}, fmt.Errorf("%w: avatar file type is invalid", ErrValidation)
	}
	return m.attachProfileAvatar(ctx, actor, linkedFile, false)
}

func (m *Management) UploadProfileAvatar(
	ctx context.Context,
	actor security.Actor,
	name string,
	content io.Reader,
) (ProfileResponse, error) {
	current, err := m.users.Current(ctx, actor)
	if err != nil {
		return ProfileResponse{}, err
	}
	folderID, err := m.ensureAvatarFolder(ctx, current.ID)
	if err != nil {
		return ProfileResponse{}, fmt.Errorf("prepare avatar folder: %w", err)
	}
	uploaded, err := m.files.UploadAvailable(ctx, security.System(), file.UploadInput{
		Storage:  m.avatarStorage,
		FolderID: &folderID,
		Name:     name,
		Content:  content,
	})
	if err != nil {
		return ProfileResponse{}, fileValidationError(err)
	}
	if uploaded.Size > m.avatarMaxSize || !profileAvatarMIMEAllowed(uploaded.MIMEType) {
		_ = m.files.DeleteFile(context.WithoutCancel(ctx), security.System(), uploaded.ID)
		return ProfileResponse{}, fmt.Errorf("%w: avatar file type is invalid", ErrValidation)
	}
	return m.attachProfileAvatar(ctx, actor, uploaded, true)
}

func (m *Management) RemoveProfileAvatar(
	ctx context.Context,
	actor security.Actor,
) (ProfileResponse, error) {
	current, err := m.users.Current(ctx, actor)
	if err != nil {
		return ProfileResponse{}, err
	}
	oldAvatar, oldManaged, err := m.resolvedProfileAvatar(ctx, current)
	if err != nil {
		return ProfileResponse{}, err
	}
	updated, err := m.users.UpdateCurrentAvatar(ctx, actor, nil)
	if err != nil {
		return ProfileResponse{}, profileValidationError(err)
	}
	m.cleanupProfileAvatar(context.WithoutCancel(ctx), oldAvatar, oldManaged)
	return m.profileResponse(ctx, updated)
}

func (m *Management) OpenProfileAvatar(
	ctx context.Context,
	actor security.Actor,
) (file.OpenedFile, error) {
	current, err := m.users.Current(ctx, actor)
	if err != nil {
		return file.OpenedFile{}, err
	}
	resolved, _, err := m.resolvedProfileAvatar(ctx, current)
	if err != nil {
		return file.OpenedFile{}, err
	}
	if resolved == nil {
		return file.OpenedFile{}, file.ErrNotFound
	}
	return m.files.Open(ctx, security.System(), resolved.File.ID)
}

func (m *Management) attachProfileAvatar(
	ctx context.Context,
	actor security.Actor,
	linkedFile file.File,
	managed bool,
) (ProfileResponse, error) {
	current, err := m.users.Current(ctx, actor)
	if err != nil {
		return ProfileResponse{}, err
	}
	oldAvatar, oldManaged, err := m.resolvedProfileAvatar(ctx, current)
	if err != nil {
		return ProfileResponse{}, err
	}
	if oldAvatar != nil && oldAvatar.File.ID == linkedFile.ID {
		return m.profileResponse(ctx, current)
	}
	created, err := m.media.Create(ctx, security.System(), media.CreateInput{
		FileID: linkedFile.ID,
		Title:  &linkedFile.Name,
		Params: map[string]any{managedAvatarParam: managed},
	})
	if err != nil {
		if managed {
			_ = m.files.DeleteFile(context.WithoutCancel(ctx), security.System(), linkedFile.ID)
		}
		return ProfileResponse{}, profileValidationError(err)
	}
	updated, err := m.users.UpdateCurrentAvatar(ctx, actor, &created.ID)
	if err != nil {
		_ = m.media.Delete(context.WithoutCancel(ctx), security.System(), created.ID)
		if managed {
			_ = m.files.DeleteFile(context.WithoutCancel(ctx), security.System(), linkedFile.ID)
		}
		return ProfileResponse{}, profileValidationError(err)
	}
	m.cleanupProfileAvatar(context.WithoutCancel(ctx), oldAvatar, oldManaged)
	return m.profileResponse(ctx, updated)
}

func (m *Management) cleanupProfileAvatar(
	ctx context.Context,
	resolved *media.ResolvedMedia,
	managed bool,
) {
	if resolved == nil || !managed {
		return
	}
	_ = m.files.DeleteFile(ctx, security.System(), resolved.File.ID)
}

func (m *Management) profileResponse(
	ctx context.Context,
	current user.User,
) (ProfileResponse, error) {
	result := ProfileDTO{
		ID: current.ID, Login: current.Login, Email: current.Email,
		Name: current.Name, LastName: current.LastName,
		MiddleName: current.MiddleName, Phone: current.Phone,
		ColorScheme: current.ColorScheme, AccentColor: current.AccentColor,
	}
	resolved, _, err := m.resolvedProfileAvatar(ctx, current)
	if err != nil {
		return ProfileResponse{}, err
	}
	if resolved != nil {
		result.Avatar = &ProfileAvatarDTO{
			MediaID: resolved.Media.ID,
			FileID:  resolved.File.ID, Name: resolved.File.Name,
			MIMEType: resolved.File.MIMEType, Size: resolved.File.Size,
			UpdatedAt: resolved.File.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
	}
	return ProfileResponse{User: result}, nil
}

func (m *Management) resolvedProfileAvatar(
	ctx context.Context,
	current user.User,
) (*media.ResolvedMedia, bool, error) {
	if current.AvatarMediaID == nil {
		return nil, false, nil
	}
	resolved, err := m.media.Resolve(ctx, security.System(), *current.AvatarMediaID)
	if err != nil {
		return nil, false, err
	}
	managed, _ := resolved.Media.Params[managedAvatarParam].(bool)
	return &resolved, managed, nil
}

func (m *Management) ensureAvatarFolder(
	ctx context.Context,
	userID user.ID,
) (file.FolderID, error) {
	root, err := m.ensureNamedFolder(ctx, nil, "avatars")
	if err != nil {
		return 0, err
	}
	return m.ensureNamedFolder(ctx, &root, strconv.FormatInt(int64(userID), 10))
}

func (m *Management) ensureNamedFolder(
	ctx context.Context,
	parentID *file.FolderID,
	name string,
) (file.FolderID, error) {
	listing, err := m.files.Browse(ctx, security.System(), m.avatarStorage, parentID)
	if err != nil {
		return 0, err
	}
	for _, entry := range listing.Folders {
		if entry.Folder.Name == name {
			return entry.Folder.ID, nil
		}
	}
	created, err := m.files.CreateFolder(ctx, security.System(), file.CreateFolderInput{
		Storage: m.avatarStorage, ParentID: parentID, Name: name,
	})
	if err == nil {
		return created.ID, nil
	}
	if !errors.Is(err, file.ErrConflict) {
		return 0, err
	}
	listing, browseErr := m.files.Browse(ctx, security.System(), m.avatarStorage, parentID)
	if browseErr != nil {
		return 0, browseErr
	}
	for _, entry := range listing.Folders {
		if entry.Folder.Name == name {
			return entry.Folder.ID, nil
		}
	}
	return 0, err
}

func profileAvatarMIMEAllowed(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func profileValidationError(err error) error {
	if err == nil || errors.Is(err, security.ErrUnauthenticated) ||
		errors.Is(err, security.ErrForbidden) || errors.Is(err, file.ErrNotFound) ||
		errors.Is(err, file.ErrStorageNotFound) {
		return err
	}
	return fmt.Errorf("%w: profile data is invalid", ErrValidation)
}
