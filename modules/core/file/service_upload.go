package file

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/security"
)

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
