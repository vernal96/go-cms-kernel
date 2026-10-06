package file

import (
	"context"
	"fmt"

	"github.com/vernal96/go-cms-kernel/security"
)

func (s *service) MoveFile(
	ctx context.Context,
	actor security.Actor,
	input MoveFileInput,
) (File, error) {
	if err := validateContext(ctx, "move file"); err != nil {
		return File{}, err
	}
	if err := s.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return File{}, err
	}
	item, err := s.file(ctx, input.ID)
	if err != nil {
		return File{}, err
	}
	if input.FolderID != nil {
		folder, err := s.repository.FolderByID(ctx, *input.FolderID)
		if err != nil {
			return File{}, fmt.Errorf("get target file folder: %w", err)
		}
		if folder.Storage != item.Storage {
			return File{}, ErrStorageMismatch
		}
	}
	result, err := s.repository.MoveFile(
		ctx,
		actor.AuditUserID(),
		input.ID,
		input.FolderID,
	)
	if err != nil {
		return File{}, fmt.Errorf("move file %d: %w", input.ID, err)
	}
	return Clone(result), nil
}

func (s *service) MoveFolder(
	ctx context.Context,
	actor security.Actor,
	input MoveFolderInput,
) (Folder, error) {
	if err := validateContext(ctx, "move file folder"); err != nil {
		return Folder{}, err
	}
	if err := s.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return Folder{}, err
	}
	if input.ID <= 0 {
		return Folder{}, fmt.Errorf("%w: file folder id is invalid", ErrInvalidInput)
	}
	item, err := s.repository.FolderByID(ctx, input.ID)
	if err != nil {
		return Folder{}, fmt.Errorf("get moved file folder: %w", err)
	}
	if input.ParentID != nil {
		if *input.ParentID == input.ID {
			return Folder{}, ErrInvalidTree
		}
		parent, err := s.repository.FolderByID(ctx, *input.ParentID)
		if err != nil {
			return Folder{}, fmt.Errorf("get target file folder: %w", err)
		}
		if parent.Storage != item.Storage {
			return Folder{}, ErrStorageMismatch
		}
	}
	result, err := s.repository.MoveFolder(
		ctx,
		actor.AuditUserID(),
		input.ID,
		input.ParentID,
	)
	if err != nil {
		return Folder{}, fmt.Errorf("move file folder %d: %w", input.ID, err)
	}
	return CloneFolder(result), nil
}

func (s *service) RenameFile(
	ctx context.Context,
	actor security.Actor,
	input RenameFileInput,
) (File, error) {
	if err := validateContext(ctx, "rename file"); err != nil {
		return File{}, err
	}
	if err := s.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return File{}, err
	}
	if input.ID <= 0 {
		return File{}, fmt.Errorf("%w: file id is invalid", ErrInvalidInput)
	}
	name, err := normalizeName(input.Name)
	if err != nil {
		return File{}, err
	}
	repository, err := s.managementRepository()
	if err != nil {
		return File{}, err
	}
	result, err := repository.RenameFile(ctx, actor.AuditUserID(), input.ID, name)
	if err != nil {
		return File{}, fmt.Errorf("rename file %d: %w", input.ID, err)
	}
	return Clone(result), nil
}

func (s *service) RenameFolder(
	ctx context.Context,
	actor security.Actor,
	input RenameFolderInput,
) (Folder, error) {
	if err := validateContext(ctx, "rename file folder"); err != nil {
		return Folder{}, err
	}
	if err := s.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return Folder{}, err
	}
	if input.ID <= 0 {
		return Folder{}, fmt.Errorf("%w: file folder id is invalid", ErrInvalidInput)
	}
	name, err := normalizeName(input.Name)
	if err != nil {
		return Folder{}, err
	}
	repository, err := s.managementRepository()
	if err != nil {
		return Folder{}, err
	}
	result, err := repository.RenameFolder(ctx, actor.AuditUserID(), input.ID, name)
	if err != nil {
		return Folder{}, fmt.Errorf("rename file folder %d: %w", input.ID, err)
	}
	return CloneFolder(result), nil
}

func (s *service) MoveItems(
	ctx context.Context,
	actor security.Actor,
	input MoveItemsInput,
) ([]Folder, []File, error) {
	if err := validateContext(ctx, "move filesystem items"); err != nil {
		return nil, nil, err
	}
	if err := s.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return nil, nil, err
	}
	if len(input.Items) == 0 {
		return nil, nil, fmt.Errorf("%w: filesystem move items are empty", ErrInvalidInput)
	}
	if err := validateItemReferences(input.Items); err != nil {
		return nil, nil, err
	}
	if _, err := s.disk(input.Storage); err != nil {
		return nil, nil, err
	}
	if input.FolderID != nil {
		folder, err := s.repository.FolderByID(ctx, *input.FolderID)
		if err != nil {
			return nil, nil, fmt.Errorf("get target file folder: %w", err)
		}
		if folder.Storage != input.Storage {
			return nil, nil, ErrStorageMismatch
		}
	}
	repository, err := s.managementRepository()
	if err != nil {
		return nil, nil, err
	}
	folders, files, err := repository.MoveItems(ctx, actor.AuditUserID(), input)
	if err != nil {
		return nil, nil, fmt.Errorf("move filesystem items: %w", err)
	}
	return folders, files, nil
}

func (s *service) DeleteItems(
	ctx context.Context,
	actor security.Actor,
	input DeleteItemsInput,
) error {
	if err := validateContext(ctx, "delete filesystem items"); err != nil {
		return err
	}
	if err := s.authorizer.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	if len(input.Items) == 0 {
		return fmt.Errorf("%w: filesystem delete items are empty", ErrInvalidInput)
	}
	if err := validateItemReferences(input.Items); err != nil {
		return err
	}
	repository, err := s.managementRepository()
	if err != nil {
		return err
	}
	if err := repository.DeleteItems(ctx, input.Items, s.deletePhysical); err != nil {
		return fmt.Errorf("delete filesystem items: %w", err)
	}
	return nil
}

func (s *service) DeleteFile(
	ctx context.Context,
	actor security.Actor,
	id ID,
) error {
	if err := validateContext(ctx, "delete file"); err != nil {
		return err
	}
	if err := s.authorizer.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("%w: file id is invalid", ErrInvalidInput)
	}
	if err := s.repository.DeleteFile(ctx, id, s.deletePhysical); err != nil {
		return fmt.Errorf("delete file %d: %w", id, err)
	}
	return nil
}

func (s *service) DeleteFolder(
	ctx context.Context,
	actor security.Actor,
	id FolderID,
) error {
	if err := validateContext(ctx, "delete file folder"); err != nil {
		return err
	}
	if err := s.authorizer.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("%w: file folder id is invalid", ErrInvalidInput)
	}
	if err := s.repository.DeleteFolder(ctx, id, s.deletePhysical); err != nil {
		return fmt.Errorf("delete file folder %d: %w", id, err)
	}
	return nil
}
