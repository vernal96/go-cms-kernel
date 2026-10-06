package file

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/filesystem"
)

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
