package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

var ErrImageConflict = errors.New("media image changed; reload editor")

type ImageRepository interface {
	UpdateImage(context.Context, *security.UserID, Media, time.Time, ValidateUsages) (Media, error)
}
type ImageMetadata struct {
	Version   int                    `json:"version"`
	Transform image.TransformOptions `json:"transform"`
}
type ImageState struct {
	Media      Media
	Current    file.File
	Original   file.File
	Transform  *image.TransformOptions
	CanRestore bool
	Editable   bool
}
type ImageService struct {
	media     *service
	files     file.ManagementService
	processor image.Processor
	limits    image.Limits
	logger    *slog.Logger
}

func NewImageService(repository Repository, files file.ManagementService, policies FilePolicies, authorizer security.Authorizer, processor image.Processor, limits image.Limits, logger *slog.Logger) (*ImageService, error) {
	base, err := NewService(repository, files, policies, authorizer)
	if err != nil {
		return nil, err
	}
	if processor == nil || logger == nil {
		return nil, errors.New("image dependencies are nil")
	}
	if err = limits.Validate(); err != nil {
		return nil, err
	}
	return &ImageService{base.(*service), files, processor, limits, logger}, nil
}
func (s *ImageService) Limits() image.Limits { return s.limits }
func (s *ImageService) State(ctx context.Context, actor security.Actor, id ID) (ImageState, error) {
	m, err := s.media.Get(ctx, actor, id)
	if err != nil {
		return ImageState{}, err
	}
	current, err := s.files.GetFile(ctx, actor, m.FileID)
	if err != nil {
		return ImageState{}, err
	}
	root := current
	seen := map[file.ID]bool{}
	for root.ParentID != nil {
		if seen[root.ID] {
			return ImageState{}, file.ErrInvalidTree
		}
		seen[root.ID] = true
		root, err = s.files.GetFile(ctx, actor, *root.ParentID)
		if err != nil {
			return ImageState{}, err
		}
	}
	state := ImageState{Media: m, Current: current, Original: root, CanRestore: root.ID != current.ID, Editable: image.EditableMIME(root.MIMEType)}
	if raw, ok := m.Params["image"]; ok {
		b, e := json.Marshal(raw)
		if e != nil {
			return state, e
		}
		var metadata ImageMetadata
		if e = json.Unmarshal(b, &metadata); e != nil {
			return state, e
		}
		if metadata.Version != 1 {
			return state, image.ErrInvalidTransform
		}
		state.Transform = &metadata.Transform
	}
	return state, nil
}
func (s *ImageService) Edit(ctx context.Context, actor security.Actor, id ID, expected time.Time, options image.TransformOptions) (ImageState, error) {
	if err := s.media.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return ImageState{}, err
	}
	if err := s.media.authorizer.Check(ctx, actor, permission.Code("core.file.create")); err != nil {
		return ImageState{}, err
	}
	state, err := s.State(ctx, actor, id)
	if err != nil {
		return state, err
	}
	if expected.IsZero() || !state.Media.UpdatedAt.Equal(expected) {
		return state, ErrImageConflict
	}
	if !state.Editable {
		return state, image.ErrUnsupportedFormat
	}
	options, err = image.Normalize(options, s.limits)
	if err != nil {
		return state, err
	}
	opened, err := s.files.Open(ctx, actor, state.Original.ID)
	if err != nil {
		return state, err
	}
	result, err := s.processor.Transform(ctx, opened.Body, options)
	closeErr := opened.Body.Close()
	if err != nil {
		return state, err
	}
	if closeErr != nil {
		return state, closeErr
	}
	ext := ".png"
	if result.MIMEType == "image/jpeg" {
		ext = ".jpg"
	}
	child, err := s.files.UploadAvailable(ctx, actor, file.UploadInput{Storage: state.Original.Storage, FolderID: state.Original.FolderID, ParentID: &state.Original.ID, Name: fmt.Sprintf("edited-%d%s", state.Original.ID, ext), Content: bytes.NewReader(result.Bytes)})
	if err != nil {
		return state, err
	}
	next := Clone(state.Media)
	next.FileID = child.ID
	next.Params["image"] = ImageMetadata{Version: 1, Transform: options}
	updated, err := s.switchFile(ctx, actor, next, expected, child)
	if err != nil {
		s.cleanup(ctx, child)
		return state, err
	}
	s.cleanupPrevious(ctx, state)
	state.Media = updated
	state.Current = child
	state.Transform = &options
	state.CanRestore = true
	return state, nil
}
func (s *ImageService) Restore(ctx context.Context, actor security.Actor, id ID, expected time.Time) (ImageState, error) {
	if err := s.media.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return ImageState{}, err
	}
	state, err := s.State(ctx, actor, id)
	if err != nil {
		return state, err
	}
	if !state.CanRestore {
		return state, nil
	}
	if expected.IsZero() || !state.Media.UpdatedAt.Equal(expected) {
		return state, ErrImageConflict
	}
	next := Clone(state.Media)
	next.FileID = state.Original.ID
	delete(next.Params, "image")
	updated, err := s.switchFile(ctx, actor, next, expected, state.Original)
	if err != nil {
		return state, err
	}
	s.cleanupPrevious(ctx, state)
	state.Media = updated
	state.Current = state.Original
	state.Transform = nil
	state.CanRestore = false
	return state, nil
}
func (s *ImageService) switchFile(ctx context.Context, actor security.Actor, next Media, expected time.Time, target file.File) (Media, error) {
	repo, ok := s.media.repository.(ImageRepository)
	if !ok {
		return Media{}, errors.New("media image repository unavailable")
	}
	return repo.UpdateImage(ctx, actor.AuditUserID(), next, expected, func(ctx context.Context, usages []Usage) error { return s.media.validateUsages(ctx, target, usages) })
}
func (s *ImageService) cleanupPrevious(ctx context.Context, state ImageState) {
	if state.Current.ID != state.Original.ID {
		s.cleanup(ctx, state.Current)
	}
}
func (s *ImageService) cleanup(ctx context.Context, item file.File) {
	if item.ParentID == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	// Already-authorized image command; safe deletion still protects every live reference.
	if err := s.files.DeleteFile(cleanupCtx, security.System(), item.ID); err != nil && !errors.Is(err, file.ErrInUse) && !errors.Is(err, file.ErrNotFound) {
		s.logger.ErrorContext(cleanupCtx, "image derivative cleanup failed", "file_id", item.ID, "error", err)
	}
}

func (s *ImageService) Create(ctx context.Context, actor security.Actor, id int64) (Media, error) {
	f, err := s.files.GetFile(ctx, actor, file.ID(id))
	if err != nil {
		return Media{}, err
	}
	if !image.EditableMIME(f.MIMEType) {
		return Media{}, image.ErrUnsupportedFormat
	}
	return s.media.Create(ctx, actor, CreateInput{FileID: f.ID})
}
