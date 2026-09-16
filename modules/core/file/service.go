package file

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

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

func (s *service) Upload(
	ctx context.Context,
	actor security.Actor,
	input UploadInput,
) (File, error) {
	return s.upload(ctx, actor, input, false)
}

func (s *service) UploadAvailable(
	ctx context.Context,
	actor security.Actor,
	input UploadInput,
) (File, error) {
	return s.upload(ctx, actor, input, true)
}

func (s *service) upload(
	ctx context.Context,
	actor security.Actor,
	input UploadInput,
	autoRename bool,
) (File, error) {
	if err := validateContext(ctx, "upload file"); err != nil {
		return File{}, err
	}
	if err := s.authorizer.Check(ctx, actor, createPermission); err != nil {
		return File{}, err
	}
	if input.Content == nil {
		return File{}, errors.New("file upload content is nil")
	}
	name, err := normalizeName(input.Name)
	if err != nil {
		return File{}, err
	}
	disk, err := s.disk(input.Storage)
	if err != nil {
		return File{}, err
	}
	if input.FolderID != nil {
		folder, err := s.repository.FolderByID(ctx, *input.FolderID)
		if err != nil {
			return File{}, fmt.Errorf("get upload file folder: %w", err)
		}
		if folder.Storage != input.Storage {
			return File{}, ErrStorageMismatch
		}
	}
	if input.ParentID != nil {
		_, err := s.repository.FileByID(ctx, *input.ParentID)
		if err != nil {
			return File{}, fmt.Errorf("get parent file: %w", err)
		}
	}
	if !autoRename {
		if err := s.repository.NameAvailable(
			ctx,
			input.Storage,
			input.FolderID,
			name,
		); err != nil {
			return File{}, err
		}
	}

	header := make([]byte, 512)
	count, readErr := io.ReadFull(input.Content, header)
	if readErr != nil &&
		!errors.Is(readErr, io.EOF) &&
		!errors.Is(readErr, io.ErrUnexpectedEOF) {
		return File{}, fmt.Errorf("read file header: %w", readErr)
	}
	header = header[:count]
	mimeType := http.DetectContentType(header)
	source := io.MultiReader(bytes.NewReader(header), input.Content)

	key, err := newObjectKey(time.Now().UTC())
	if err != nil {
		return File{}, err
	}
	hash := sha256.New()
	counter := &byteCounter{}
	measured := io.TeeReader(source, io.MultiWriter(hash, counter))
	if err := disk.PutNew(ctx, key, measured, mimeType); err != nil {
		return File{}, fmt.Errorf("store file on disk %q: %w", input.Storage, err)
	}

	item := File{
		FolderID:       cloneFolderID(input.FolderID),
		Storage:        input.Storage,
		Name:           name,
		MIMEType:       mimeType,
		Size:           counter.count,
		ChecksumSHA256: hex.EncodeToString(hash.Sum(nil)),
		Path:           key,
		ParentID:       cloneID(input.ParentID),
		CreatedBy:      actor.AuditUserID(),
		UpdatedBy:      actor.AuditUserID(),
	}
	var result File
	var createErr error
	if autoRename {
		repository, err := s.managementRepository()
		if err != nil {
			_ = disk.Delete(context.WithoutCancel(ctx), key)
			return File{}, err
		}
		result, createErr = repository.CreateAvailableFile(ctx, item)
	} else {
		result, createErr = s.repository.CreateFile(ctx, item)
	}
	if createErr != nil {
		cleanupErr := disk.Delete(context.WithoutCancel(ctx), key)
		return File{}, errors.Join(
			fmt.Errorf("register uploaded file: %w", createErr),
			wrapCleanupError(cleanupErr),
		)
	}
	return Clone(result), nil
}

func (s *service) GetFile(
	ctx context.Context,
	actor security.Actor,
	id ID,
) (File, error) {
	if err := validateContext(ctx, "get file"); err != nil {
		return File{}, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return File{}, err
	}
	return s.file(ctx, id)
}

func (s *service) Open(
	ctx context.Context,
	actor security.Actor,
	id ID,
) (OpenedFile, error) {
	if err := validateContext(ctx, "open file"); err != nil {
		return OpenedFile{}, err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return OpenedFile{}, err
	}
	item, err := s.file(ctx, id)
	if err != nil {
		return OpenedFile{}, err
	}
	disk, err := s.disk(item.Storage)
	if err != nil {
		return OpenedFile{}, err
	}
	body, err := disk.Open(ctx, item.Path)
	if err != nil {
		return OpenedFile{}, fmt.Errorf("open physical file %d: %w", id, err)
	}
	return OpenedFile{File: item, Body: body}, nil
}

func (s *service) OpenDelivery(
	ctx context.Context,
	id ID,
	authorization DeliveryAuthorization,
) (OpenedFile, error) {
	if err := validateContext(ctx, "open delivered file"); err != nil {
		return OpenedFile{}, err
	}
	item, err := s.file(ctx, id)
	if err != nil {
		return OpenedFile{}, err
	}
	disk, err := s.disk(item.Storage)
	if err != nil {
		return OpenedFile{}, err
	}
	if disk.Visibility() == filesystem.VisibilityPrivate {
		verifier, ok := disk.(filesystem.TemporaryURLVerifier)
		if !ok {
			return OpenedFile{}, ErrUnauthorized
		}
		err := verifier.VerifyTemporaryURL(
			reference(item),
			authorization.ExpiresAt,
			authorization.Signature,
		)
		if err != nil {
			return OpenedFile{}, ErrUnauthorized
		}
	}
	body, err := disk.Open(ctx, item.Path)
	if err != nil {
		return OpenedFile{}, fmt.Errorf("open delivered file %d: %w", id, err)
	}
	return OpenedFile{File: item, Body: body}, nil
}

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

func (s *service) URL(
	ctx context.Context,
	actor security.Actor,
	id ID,
) (string, error) {
	if err := validateContext(ctx, "create file URL"); err != nil {
		return "", err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return "", err
	}
	item, err := s.file(ctx, id)
	if err != nil {
		return "", err
	}
	disk, err := s.disk(item.Storage)
	if err != nil {
		return "", err
	}
	if disk.Visibility() != filesystem.VisibilityPublic {
		return "", filesystem.ErrInvalidVisibility
	}
	return disk.URL(ctx, reference(item))
}

func (s *service) TemporaryURL(
	ctx context.Context,
	actor security.Actor,
	id ID,
	expiresAt time.Time,
) (string, error) {
	if err := validateContext(ctx, "create temporary file URL"); err != nil {
		return "", err
	}
	if err := s.authorizer.Check(ctx, actor, readPermission); err != nil {
		return "", err
	}
	if !expiresAt.After(time.Now()) {
		return "", errors.New("temporary URL expiration must be in the future")
	}
	item, err := s.file(ctx, id)
	if err != nil {
		return "", err
	}
	disk, err := s.disk(item.Storage)
	if err != nil {
		return "", err
	}
	if disk.Visibility() != filesystem.VisibilityPrivate {
		return "", filesystem.ErrInvalidVisibility
	}
	return disk.TemporaryURL(ctx, reference(item), expiresAt)
}

func (s *service) deletePhysical(
	ctx context.Context,
	items []File,
) error {
	var deleteErrors []error
	for _, item := range items {
		disk, err := s.disk(item.Storage)
		if err != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf(
				"resolve disk for file %d: %w",
				item.ID,
				err,
			))
			continue
		}
		if err := disk.Delete(ctx, item.Path); err != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf(
				"delete physical file %d: %w",
				item.ID,
				err,
			))
		}
	}
	return errors.Join(deleteErrors...)
}

func (s *service) file(ctx context.Context, id ID) (File, error) {
	if id <= 0 {
		return File{}, fmt.Errorf("%w: file id is invalid", ErrInvalidInput)
	}
	item, err := s.repository.FileByID(ctx, id)
	if err != nil {
		return File{}, fmt.Errorf("get file %d: %w", id, err)
	}
	return Clone(item), nil
}

func (s *service) disk(code filesystem.Code) (filesystem.Disk, error) {
	if code == "" {
		return nil, fmt.Errorf("%w: file storage is empty", ErrInvalidInput)
	}
	disk, exists := s.disks.Disk(code)
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrStorageNotFound, code)
	}
	return disk, nil
}

func (s *service) managementRepository() (ManagementRepository, error) {
	repository, ok := s.repository.(ManagementRepository)
	if !ok {
		return nil, errors.New("file management repository is unavailable")
	}
	return repository, nil
}

func validateItemReferences(items []ItemReference) error {
	seen := make(map[ItemReference]struct{}, len(items))
	for _, item := range items {
		if item.ID <= 0 || (item.Kind != ItemFile && item.Kind != ItemFolder) {
			return fmt.Errorf("%w: filesystem item reference is invalid", ErrInvalidInput)
		}
		if _, exists := seen[item]; exists {
			return fmt.Errorf("%w: filesystem item reference is duplicated", ErrInvalidInput)
		}
		seen[item] = struct{}{}
	}
	return nil
}

func reference(item File) filesystem.Reference {
	return filesystem.Reference{
		ID:   strconv.FormatInt(int64(item.ID), 10),
		Path: item.Path,
	}
}

func validateContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s context is nil", operation)
	}
	return ctx.Err()
}

func normalizeName(value string) (string, error) {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return "", fmt.Errorf("%w: file name is empty", ErrInvalidInput)
	case value == ".", value == "..":
		return "", fmt.Errorf("%w: file name is reserved", ErrInvalidInput)
	case strings.Contains(value, "/"):
		return "", fmt.Errorf("%w: file name contains a path separator", ErrInvalidInput)
	case strings.ContainsRune(value, '\x00'):
		return "", fmt.Errorf("%w: file name contains NUL", ErrInvalidInput)
	default:
		return value, nil
	}
}

func newObjectKey(now time.Time) (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate file object key: %w", err)
	}
	return path.Join(
		"objects",
		now.Format("2006"),
		now.Format("01"),
		hex.EncodeToString(random),
	), nil
}

func wrapCleanupError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("clean up unregistered physical file: %w", err)
}

type byteCounter struct {
	count int64
}

func (c *byteCounter) Write(source []byte) (int, error) {
	c.count += int64(len(source))
	return len(source), nil
}

var _ ManagementService = (*service)(nil)
