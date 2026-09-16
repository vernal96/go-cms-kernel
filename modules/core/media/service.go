package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

var (
	readPermission = permission.MustCode(
		"core",
		"media",
		permission.Read,
	)
	createPermission = permission.MustCode(
		"core",
		"media",
		permission.Create,
	)
	updatePermission = permission.MustCode(
		"core",
		"media",
		permission.Update,
	)
	deletePermission = permission.MustCode(
		"core",
		"media",
		permission.Delete,
	)
)

type service struct {
	repository Repository
	files      file.Service
	policies   FilePolicies
	authorizer security.Authorizer
}

func NewService(
	repository Repository,
	files file.Service,
	policies FilePolicies,
	authorizer security.Authorizer,
) (Service, error) {
	if repository == nil {
		return nil, errors.New("media repository is nil")
	}
	if files == nil {
		return nil, errors.New("media file service is nil")
	}
	if authorizer == nil {
		return nil, errors.New("media authorizer is nil")
	}

	clonedPolicies := make(FilePolicies, len(policies))
	for kind, policy := range policies {
		if kind == "" {
			return nil, errors.New("media usage kind is empty")
		}
		if policy == nil {
			return nil, fmt.Errorf(
				"media file policy %q is nil",
				kind,
			)
		}
		clonedPolicies[kind] = policy
	}

	return &service{
		repository: repository,
		files:      files,
		policies:   clonedPolicies,
		authorizer: authorizer,
	}, nil
}

func (s *service) Create(
	ctx context.Context,
	actor security.Actor,
	input CreateInput,
) (Media, error) {
	if err := validateContext(ctx, "create media"); err != nil {
		return Media{}, err
	}
	if err := s.authorizer.Check(ctx, actor, createPermission); err != nil {
		return Media{}, err
	}
	if input.FileID <= 0 {
		return Media{}, errors.New("media file id is invalid")
	}

	if _, err := s.files.GetFile(
		ctx,
		security.System(),
		input.FileID,
	); err != nil {
		return Media{}, fmt.Errorf(
			"%w: get media file %d: %v",
			ErrInvalidReference,
			input.FileID,
			err,
		)
	}

	title := normalizeTitle(input.Title)
	params, err := normalizeParams(input.Params)
	if err != nil {
		return Media{}, err
	}

	created, err := s.repository.Create(
		ctx,
		actor.AuditUserID(),
		Media{
			FileID:    input.FileID,
			Title:     title,
			Params:    params,
			CreatedBy: actor.AuditUserID(),
			UpdatedBy: actor.AuditUserID(),
		},
	)
	if err != nil {
		return Media{}, fmt.Errorf("create media: %w", err)
	}
	return Clone(created), nil
}

func (s *service) Get(
	ctx context.Context,
	actor security.Actor,
	id ID,
) (Media, error) {
	if err := validateContext(ctx, "get media"); err != nil {
		return Media{}, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return Media{}, err
	}
	if id <= 0 {
		return Media{}, errors.New("media id is invalid")
	}

	item, err := s.repository.ByID(ctx, id)
	if err != nil {
		return Media{}, fmt.Errorf("get media %d: %w", id, err)
	}
	return Clone(item), nil
}

func (s *service) Resolve(
	ctx context.Context,
	actor security.Actor,
	id ID,
) (ResolvedMedia, error) {
	if err := validateContext(ctx, "resolve media"); err != nil {
		return ResolvedMedia{}, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return ResolvedMedia{}, err
	}

	item, err := s.repository.ByID(ctx, id)
	if err != nil {
		return ResolvedMedia{}, fmt.Errorf("get media %d: %w", id, err)
	}
	linkedFile, err := s.files.GetFile(
		ctx,
		security.System(),
		item.FileID,
	)
	if err != nil {
		return ResolvedMedia{}, fmt.Errorf(
			"%w: get file %d for media %d: %v",
			ErrInvalidReference,
			item.FileID,
			id,
			err,
		)
	}

	return ResolvedMedia{
		Media: Clone(item),
		File:  file.Clone(linkedFile),
	}, nil
}

func (s *service) Update(
	ctx context.Context,
	actor security.Actor,
	input UpdateInput,
) (Media, error) {
	if err := validateContext(ctx, "update media"); err != nil {
		return Media{}, err
	}
	if err := s.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return Media{}, err
	}
	if input.ID <= 0 {
		return Media{}, errors.New("media id is invalid")
	}
	if input.FileID <= 0 {
		return Media{}, errors.New("media file id is invalid")
	}

	linkedFile, err := s.files.GetFile(
		ctx,
		security.System(),
		input.FileID,
	)
	if err != nil {
		return Media{}, fmt.Errorf(
			"%w: get media file %d: %v",
			ErrInvalidReference,
			input.FileID,
			err,
		)
	}

	title := normalizeTitle(input.Title)
	params, err := normalizeParams(input.Params)
	if err != nil {
		return Media{}, err
	}

	updated, err := s.repository.Update(
		ctx,
		actor.AuditUserID(),
		Media{
			ID:        input.ID,
			FileID:    input.FileID,
			Title:     title,
			Params:    params,
			UpdatedBy: actor.AuditUserID(),
		},
		func(ctx context.Context, usages []Usage) error {
			return s.validateUsages(ctx, linkedFile, usages)
		},
	)
	if err != nil {
		return Media{}, fmt.Errorf("update media %d: %w", input.ID, err)
	}
	return Clone(updated), nil
}

func (s *service) Delete(
	ctx context.Context,
	actor security.Actor,
	id ID,
) error {
	if err := validateContext(ctx, "delete media"); err != nil {
		return err
	}
	if err := s.authorizer.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("media id is invalid")
	}

	if err := s.repository.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete media %d: %w", id, err)
	}
	return nil
}

func (s *service) validateUsages(
	ctx context.Context,
	linkedFile file.File,
	usages []Usage,
) error {
	for _, usage := range usages {
		policy, exists := s.policies[usage.Kind]
		if !exists {
			return fmt.Errorf(
				"%w: %q",
				ErrUnknownUsage,
				usage.Kind,
			)
		}
		if err := policy(ctx, linkedFile, usage); err != nil {
			return fmt.Errorf(
				"validate media usage %q for owner %d: %w",
				usage.Kind,
				usage.OwnerID,
				err,
			)
		}
	}
	return nil
}

func normalizeTitle(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func normalizeParams(source map[string]any) (map[string]any, error) {
	if source == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return nil, fmt.Errorf("encode media params: %w", err)
	}

	result := make(map[string]any)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode media params: %w", err)
	}
	return result, nil
}

func validateContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s context is nil", operation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

var _ Service = (*service)(nil)
