package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

func TestProfileAvatarMIMEAllowlist(t *testing.T) {
	t.Parallel()

	for _, mimeType := range []string{"image/jpeg", "image/png", "image/webp", "image/gif"} {
		if !profileAvatarMIMEAllowed(mimeType) {
			t.Fatalf("allowed MIME type %q was rejected", mimeType)
		}
	}
	for _, mimeType := range []string{"image/svg+xml", "text/html", "application/octet-stream"} {
		if profileAvatarMIMEAllowed(mimeType) {
			t.Fatalf("disallowed MIME type %q was accepted", mimeType)
		}
	}
}

type profileUserService struct {
	user.Service
	current         user.User
	profileInput    user.UpdateCurrentInput
	passwordCurrent string
	passwordNext    string
	preferencesErr  error
}

func (s *profileUserService) Current(context.Context, security.Actor) (user.User, error) {
	return user.Clone(s.current), nil
}

func (s *profileUserService) UpdateCurrent(
	_ context.Context,
	_ security.Actor,
	input user.UpdateCurrentInput,
) (user.User, error) {
	s.profileInput = input
	s.current.Name = input.Name
	s.current.LastName = input.LastName
	s.current.MiddleName = input.MiddleName
	s.current.Phone = input.Phone
	return user.Clone(s.current), nil
}

func TestProfilePreferencesInvalidAccentReturnsValidationError(t *testing.T) {
	users := &profileUserService{
		current: user.User{
			ID: 3, Login: "member", Email: "member@example.test", Name: "Member",
			ColorScheme: user.ColorSchemeSystem, AccentColor: user.AccentColorBlue,
		},
		preferencesErr: errors.New("invalid user accent color"),
	}
	router := chi.NewRouter()
	registerProfileRoutes(router, &managementHTTP{management: &Management{users: users}})
	request := httptest.NewRequest(
		http.MethodPut,
		"/profile/preferences",
		strings.NewReader(`{"color_scheme":"light","accent_color":"turquoise"}`),
	)
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(3)))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func (s *profileUserService) UpdateCurrentPreferences(
	_ context.Context,
	_ security.Actor,
	preferences user.Preferences,
) (user.User, error) {
	if s.preferencesErr != nil {
		return user.User{}, s.preferencesErr
	}
	s.current.ColorScheme = preferences.ColorScheme
	s.current.AccentColor = preferences.AccentColor
	return user.Clone(s.current), nil
}

func (s *profileUserService) ChangeCurrentPassword(
	_ context.Context,
	_ security.Actor,
	current string,
	next string,
) (user.User, error) {
	s.passwordCurrent, s.passwordNext = current, next
	return user.Clone(s.current), nil
}

func (s *profileUserService) UpdateCurrentAvatar(
	_ context.Context,
	_ security.Actor,
	avatarID *media.ID,
) (user.User, error) {
	s.current.AvatarMediaID = avatarID
	return user.Clone(s.current), nil
}

type profileFileService struct {
	file.ManagementService
	item    file.File
	actor   security.Actor
	deleted []file.ID
}

func (s *profileFileService) DeleteFile(
	_ context.Context,
	_ security.Actor,
	id file.ID,
) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *profileFileService) GetFile(
	_ context.Context,
	actor security.Actor,
	_ file.ID,
) (file.File, error) {
	s.actor = actor
	return s.item, nil
}

type profileMediaService struct {
	media.Service
	item media.Media
	file file.File
}

func (s *profileMediaService) Create(
	_ context.Context,
	_ security.Actor,
	input media.CreateInput,
) (media.Media, error) {
	s.item = media.Media{ID: 9, FileID: input.FileID, Title: input.Title, Params: input.Params}
	return media.Clone(s.item), nil
}

func (s *profileMediaService) Resolve(
	_ context.Context,
	_ security.Actor,
	_ media.ID,
) (media.ResolvedMedia, error) {
	return media.ResolvedMedia{Media: media.Clone(s.item), File: file.Clone(s.file)}, nil
}

func TestProfileSelfServiceDoesNotRequireUserManagementPermissions(t *testing.T) {
	users := &profileUserService{current: user.User{
		ID: 3, Login: "member", Email: "member@example.test", Name: "Member",
		ColorScheme: user.ColorSchemeSystem,
	}}
	management := &Management{users: users}
	lastName := "User"
	result, err := management.UpdateProfile(
		context.Background(), security.User(3),
		user.UpdateCurrentInput{Name: "Updated", LastName: &lastName},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.User.Login != "member" || result.User.Email != "member@example.test" ||
		result.User.Name != "Updated" || users.profileInput.Name != "Updated" {
		t.Fatalf("profile = %#v", result)
	}
	if _, err := management.UpdateProfilePreferences(
		context.Background(), security.User(3), user.Preferences{
			ColorScheme: user.ColorSchemeDark,
			AccentColor: user.AccentColorViolet,
		},
	); err != nil || users.current.ColorScheme != user.ColorSchemeDark ||
		users.current.AccentColor != user.AccentColorViolet {
		t.Fatalf("preferences error = %v", err)
	}
	if err := management.ChangeProfilePassword(
		context.Background(), security.User(3), "current-password", "next-password-valid",
	); err != nil || users.passwordCurrent != "current-password" || users.passwordNext != "next-password-valid" {
		t.Fatalf("password update = %q, %q, %v", users.passwordCurrent, users.passwordNext, err)
	}
}

func TestProfileAvatarSelectionUsesActorFileReadAndMediaAttachment(t *testing.T) {
	now := time.Now().UTC()
	linkedFile := file.File{
		ID: 5, Storage: "private", Name: "avatar.png",
		MIMEType: "image/png", Size: 128, UpdatedAt: now,
	}
	users := &profileUserService{current: user.User{
		ID: 3, Login: "member", Email: "member@example.test", Name: "Member",
		ColorScheme: user.ColorSchemeSystem,
	}}
	files := &profileFileService{item: linkedFile}
	mediaService := &profileMediaService{file: linkedFile}
	management := &Management{users: users, files: files, media: mediaService}

	result, err := management.SelectProfileAvatar(
		context.Background(), security.User(3), linkedFile.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	actorID, exists := files.actor.UserID()
	if !exists || actorID != 3 {
		t.Fatalf("file actor = %#v", files.actor)
	}
	if result.User.Avatar == nil || result.User.Avatar.FileID != linkedFile.ID ||
		users.current.AvatarMediaID == nil || *users.current.AvatarMediaID != 9 {
		t.Fatalf("avatar result = %#v", result)
	}
}

func TestRemovingProfileAvatarOnlyDeletesOwnedFile(t *testing.T) {
	linkedFile := file.File{ID: 5, Name: "avatar.png", MIMEType: "image/png"}
	avatarID := media.ID(9)
	users := &profileUserService{current: user.User{
		ID: 3, Login: "member", Email: "member@example.test", Name: "Member",
		AvatarMediaID: &avatarID, ColorScheme: user.ColorSchemeSystem,
	}}
	files := &profileFileService{item: linkedFile}
	mediaService := &profileMediaService{
		item: media.Media{
			ID: avatarID, FileID: linkedFile.ID,
			Params: map[string]any{managedAvatarParam: true},
		},
		file: linkedFile,
	}
	management := &Management{users: users, files: files, media: mediaService}

	if _, err := management.RemoveProfileAvatar(
		context.Background(), security.User(3),
	); err != nil {
		t.Fatal(err)
	}
	if len(files.deleted) != 1 || files.deleted[0] != linkedFile.ID {
		t.Fatalf("deleted files = %#v", files.deleted)
	}

	users.current.AvatarMediaID = &avatarID
	mediaService.item.Params[managedAvatarParam] = false
	files.deleted = nil
	if _, err := management.RemoveProfileAvatar(
		context.Background(), security.User(3),
	); err != nil {
		t.Fatal(err)
	}
	if len(files.deleted) != 0 {
		t.Fatalf("selected source files were deleted: %#v", files.deleted)
	}
}
