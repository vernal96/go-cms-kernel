package file

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

var (
	readPermission = permission.MustCode(
		"core",
		"file",
		permission.Read,
	)
	createPermission = permission.MustCode(
		"core",
		"file",
		permission.Create,
	)
	updatePermission = permission.MustCode(
		"core",
		"file",
		permission.Update,
	)
	deletePermission = permission.MustCode(
		"core",
		"file",
		permission.Delete,
	)
)

type DiskResolver interface {
	Disk(filesystem.Code) (filesystem.Disk, bool)
}

type service struct {
	repository Repository
	disks      DiskResolver
	authorizer security.Authorizer
}

func NewService(
	repository Repository,
	disks DiskResolver,
	authorizer security.Authorizer,
) (ManagementService, error) {
	if repository == nil {
		return nil, errors.New("file repository is nil")
	}
	if disks == nil {
		return nil, errors.New("filesystem disk resolver is nil")
	}
	if authorizer == nil {
		return nil, errors.New("file authorizer is nil")
	}
	return &service{
		repository: repository,
		disks:      disks,
		authorizer: authorizer,
	}, nil
}

func (s *service) Disks(
	ctx context.Context,
	actor security.Actor,
) ([]filesystem.DiskInfo, error) {
	if err := validateContext(ctx, "list filesystem disks"); err != nil {
		return nil, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return nil, err
	}
	catalog, ok := s.disks.(interface{ Disks() []filesystem.DiskInfo })
	if !ok {
		return nil, errors.New("filesystem disk catalog is unavailable")
	}
	return append([]filesystem.DiskInfo(nil), catalog.Disks()...), nil
}

func (s *service) Browse(
	ctx context.Context,
	actor security.Actor,
	storage filesystem.Code,
	folderID *FolderID,
) (BrowserListing, error) {
	if err := validateContext(ctx, "browse file folder"); err != nil {
		return BrowserListing{}, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return BrowserListing{}, err
	}
	disk, err := s.disk(storage)
	if err != nil {
		return BrowserListing{}, err
	}
	repository, err := s.managementRepository()
	if err != nil {
		return BrowserListing{}, err
	}

	var current *Folder
	var breadcrumbs []Folder
	if folderID != nil {
		item, err := repository.FolderByID(ctx, *folderID)
		if err != nil {
			return BrowserListing{}, fmt.Errorf("get browsed file folder: %w", err)
		}
		if item.Storage != storage {
			return BrowserListing{}, ErrStorageMismatch
		}
		item = CloneFolder(item)
		current = &item
		breadcrumbs, err = repository.FolderAncestors(ctx, *folderID)
		if err != nil {
			return BrowserListing{}, fmt.Errorf("list folder breadcrumbs: %w", err)
		}
	}
	folders, err := repository.ListFolderEntries(ctx, storage, folderID)
	if err != nil {
		return BrowserListing{}, fmt.Errorf("list file folders: %w", err)
	}
	files, err := repository.ListFiles(ctx, storage, folderID)
	if err != nil {
		return BrowserListing{}, fmt.Errorf("list files: %w", err)
	}
	return BrowserListing{
		Storage:     storage,
		Visibility:  disk.Visibility(),
		Folder:      current,
		Breadcrumbs: breadcrumbs,
		Folders:     folders,
		Files:       files,
	}, nil
}

func (s *service) ResolveFolder(ctx context.Context, actor security.Actor, storage filesystem.Code, folderPath string) (Folder, error) {
	if err := validateContext(ctx, "resolve file folder path"); err != nil {
		return Folder{}, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return Folder{}, err
	}
	if _, err := s.disk(storage); err != nil {
		return Folder{}, err
	}
	normalized, err := normalizeFolderPath(folderPath)
	if err != nil {
		return Folder{}, err
	}
	var parentID *FolderID
	var current Folder
	for _, name := range strings.Split(normalized, "/") {
		folders, err := s.repository.ListFolders(ctx, storage, parentID)
		if err != nil {
			return Folder{}, fmt.Errorf("list file folders while resolving path: %w", err)
		}
		found := false
		for _, folder := range folders {
			if folder.Name == name {
				current = folder
				value := folder.ID
				parentID = &value
				found = true
				break
			}
		}
		if !found {
			return Folder{}, ErrNotFound
		}
	}
	return CloneFolder(current), nil
}

func (s *service) EnsureFolderPath(ctx context.Context, actor security.Actor, storage filesystem.Code, folderPath string) (Folder, error) {
	if err := validateContext(ctx, "ensure file folder path"); err != nil {
		return Folder{}, err
	}
	if err := s.authorizer.Check(ctx, actor, createPermission); err != nil {
		return Folder{}, err
	}
	if _, err := s.disk(storage); err != nil {
		return Folder{}, err
	}
	normalized, err := normalizeFolderPath(folderPath)
	if err != nil {
		return Folder{}, err
	}
	var parentID *FolderID
	var current Folder
	for _, name := range strings.Split(normalized, "/") {
		folders, err := s.repository.ListFolders(ctx, storage, parentID)
		if err != nil {
			return Folder{}, fmt.Errorf("list file folders while ensuring path: %w", err)
		}
		if existing, exists := namedFolder(folders, name); exists {
			current = existing
			value := existing.ID
			parentID = &value
			continue
		}
		created, err := s.repository.CreateFolder(ctx, Folder{
			ParentID: cloneFolderID(parentID), Storage: storage, Name: name,
			CreatedBy: actor.AuditUserID(), UpdatedBy: actor.AuditUserID(),
		})
		if errors.Is(err, ErrConflict) {
			folders, listErr := s.repository.ListFolders(ctx, storage, parentID)
			if listErr != nil {
				return Folder{}, fmt.Errorf("resolve concurrently ensured file folder: %w", listErr)
			}
			var exists bool
			created, exists = namedFolder(folders, name)
			if !exists {
				return Folder{}, ErrConflict
			}
		} else if err != nil {
			return Folder{}, fmt.Errorf("ensure file folder path: %w", err)
		}
		current = created
		value := created.ID
		parentID = &value
	}
	return CloneFolder(current), nil
}

func normalizeFolderPath(folderPath string) (string, error) {
	raw := strings.TrimSpace(folderPath)
	if raw == "" || path.IsAbs(raw) || strings.Contains(raw, "\\") {
		return "", fmt.Errorf("%w: file folder path is invalid", ErrInvalidInput)
	}
	normalized := strings.Trim(raw, "/")
	if normalized == "" || path.Clean(normalized) != normalized || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("%w: file folder path is invalid", ErrInvalidInput)
	}
	return normalized, nil
}

func namedFolder(folders []Folder, name string) (Folder, bool) {
	for _, folder := range folders {
		if folder.Name == name {
			return CloneFolder(folder), true
		}
	}
	return Folder{}, false
}

func (s *service) CreateFolder(
	ctx context.Context,
	actor security.Actor,
	input CreateFolderInput,
) (Folder, error) {
	if err := validateContext(ctx, "create file folder"); err != nil {
		return Folder{}, err
	}
	if err := s.authorizer.Check(ctx, actor, createPermission); err != nil {
		return Folder{}, err
	}
	name, err := normalizeName(input.Name)
	if err != nil {
		return Folder{}, err
	}
	if _, err := s.disk(input.Storage); err != nil {
		return Folder{}, err
	}
	if input.ParentID != nil {
		parent, err := s.repository.FolderByID(ctx, *input.ParentID)
		if err != nil {
			return Folder{}, fmt.Errorf("get parent file folder: %w", err)
		}
		if parent.Storage != input.Storage {
			return Folder{}, ErrStorageMismatch
		}
	}

	result, err := s.repository.CreateFolder(ctx, Folder{
		ParentID:  cloneFolderID(input.ParentID),
		Storage:   input.Storage,
		Name:      name,
		CreatedBy: actor.AuditUserID(),
		UpdatedBy: actor.AuditUserID(),
	})
	if err != nil {
		return Folder{}, fmt.Errorf("create file folder: %w", err)
	}
	return CloneFolder(result), nil
}

func (s *service) CreateAvailableFolder(
	ctx context.Context,
	actor security.Actor,
	input CreateFolderInput,
) (Folder, error) {
	if err := validateContext(ctx, "create available file folder"); err != nil {
		return Folder{}, err
	}
	if err := s.authorizer.Check(ctx, actor, createPermission); err != nil {
		return Folder{}, err
	}
	name, err := normalizeName(input.Name)
	if err != nil {
		return Folder{}, err
	}
	if _, err := s.disk(input.Storage); err != nil {
		return Folder{}, err
	}
	if input.ParentID != nil {
		parent, err := s.repository.FolderByID(ctx, *input.ParentID)
		if err != nil {
			return Folder{}, fmt.Errorf("get parent file folder: %w", err)
		}
		if parent.Storage != input.Storage {
			return Folder{}, ErrStorageMismatch
		}
	}
	repository, err := s.managementRepository()
	if err != nil {
		return Folder{}, err
	}
	result, err := repository.CreateAvailableFolder(ctx, Folder{
		ParentID:  cloneFolderID(input.ParentID),
		Storage:   input.Storage,
		Name:      name,
		CreatedBy: actor.AuditUserID(),
		UpdatedBy: actor.AuditUserID(),
	})
	if err != nil {
		return Folder{}, fmt.Errorf("create available file folder: %w", err)
	}
	return CloneFolder(result), nil
}

func (s *service) GetFolder(
	ctx context.Context,
	actor security.Actor,
	id FolderID,
) (Folder, error) {
	if err := validateContext(ctx, "get file folder"); err != nil {
		return Folder{}, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return Folder{}, err
	}
	if id <= 0 {
		return Folder{}, fmt.Errorf("%w: file folder id is invalid", ErrInvalidInput)
	}
	result, err := s.repository.FolderByID(ctx, id)
	if err != nil {
		return Folder{}, fmt.Errorf("get file folder %d: %w", id, err)
	}
	return CloneFolder(result), nil
}

func (s *service) ListFolder(
	ctx context.Context,
	actor security.Actor,
	storage filesystem.Code,
	folderID *FolderID,
) (Listing, error) {
	if err := validateContext(ctx, "list file folder"); err != nil {
		return Listing{}, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return Listing{}, err
	}
	if _, err := s.disk(storage); err != nil {
		return Listing{}, err
	}

	var current *Folder
	if folderID != nil {
		item, err := s.repository.FolderByID(ctx, *folderID)
		if err != nil {
			return Listing{}, fmt.Errorf("get listed file folder: %w", err)
		}
		if item.Storage != storage {
			return Listing{}, ErrStorageMismatch
		}
		item = CloneFolder(item)
		current = &item
	}

	folders, err := s.repository.ListFolders(ctx, storage, folderID)
	if err != nil {
		return Listing{}, fmt.Errorf("list file folders: %w", err)
	}
	files, err := s.repository.ListFiles(ctx, storage, folderID)
	if err != nil {
		return Listing{}, fmt.Errorf("list files: %w", err)
	}
	return Listing{Folder: current, Folders: folders, Files: files}, nil
}
