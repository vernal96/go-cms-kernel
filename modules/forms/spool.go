package forms

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/messageid"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
)

const SpoolFilesystemAlias filesystem.Alias = "spool"
const spoolRootPrefix = "forms-spool/"

type UploadSpool struct {
	disk            filesystem.Disk
	scannerProvider filesystem.PrefixScannerProvider
	prefix          string
	cleanupMu       sync.Mutex
	cleanupScan     filesystem.PrefixScan
}

func NewUploadSpool(siteID site.ID, disk filesystem.Disk) (*UploadSpool, error) {
	if siteID <= 0 {
		return nil, errors.New("Forms upload spool site is invalid")
	}
	if disk == nil || disk.Visibility() != filesystem.VisibilityPrivate {
		return nil, errors.New("Forms upload spool must use a private filesystem")
	}
	scanner, ok := disk.(filesystem.PrefixScannerProvider)
	if !ok {
		return nil, errors.New("Forms upload spool does not support bounded cleanup")
	}
	return &UploadSpool{disk: disk, scannerProvider: scanner, prefix: spoolRootPrefix + fmt.Sprint(siteID) + "/"}, nil
}

func (s *UploadSpool) Put(ctx context.Context, input UploadInput, maxSize int64) (ResultUpload, error) {
	if s == nil || s.disk == nil || input.Body == nil || input.Size < 0 || maxSize < 1 || input.Size > maxSize {
		return ResultUpload{}, fmt.Errorf("%w: upload size is invalid", ErrInvalid)
	}
	filename := safeFilename(input.Filename)
	if filename == "" {
		return ResultUpload{}, fmt.Errorf("%w: upload filename is invalid", ErrInvalid)
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(input.MIMEType))
	if err != nil || mediaType == "" {
		return ResultUpload{}, fmt.Errorf("%w: upload MIME type is invalid", ErrInvalid)
	}
	id, err := messageid.New()
	if err != nil {
		return ResultUpload{}, err
	}
	key := s.prefix + strconv.FormatInt(time.Now().UTC().Unix(), 10) + "-" + string(id)
	hash := sha256.New()
	counter := &uploadCountingReader{reader: io.TeeReader(io.LimitReader(input.Body, input.Size+1), hash)}
	if err := s.disk.PutNew(ctx, key, counter, mediaType); err != nil {
		return ResultUpload{}, fmt.Errorf("write Forms upload spool: %w", err)
	}
	if counter.count != input.Size {
		_ = s.disk.Delete(context.WithoutCancel(ctx), key)
		return ResultUpload{}, fmt.Errorf("%w: upload size does not match body", ErrInvalid)
	}
	return ResultUpload{
		FieldCode: input.FieldCode, Position: input.Position, Filename: filename,
		MIMEType: mediaType, Size: input.Size, Checksum: hex.EncodeToString(hash.Sum(nil)),
		SpoolReference: key,
	}, nil
}

func safeFilename(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	value = filepath.Base(value)
	value = strings.Map(func(current rune) rune {
		if current < ' ' || current == '\x7f' || current == '/' || current == '\\' {
			return -1
		}
		return current
	}, value)
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." {
		return ""
	}
	if len(value) > 255 {
		value = value[:255]
	}
	return value
}

func (s *UploadSpool) Open(ctx context.Context, reference string) (io.ReadCloser, error) {
	if s == nil || s.disk == nil || !s.validKey(reference) {
		return nil, errors.New("Forms upload spool reference is invalid")
	}
	return s.disk.Open(ctx, reference)
}

func (s *UploadSpool) Delete(ctx context.Context, reference string) error {
	if s == nil || s.disk == nil || !s.validKey(reference) {
		return errors.New("Forms upload spool reference is invalid")
	}
	err := s.disk.Delete(ctx, reference)
	if errors.Is(err, filesystem.ErrNotFound) {
		return nil
	}
	return err
}

func (s *UploadSpool) Cleanup(ctx context.Context, olderThan time.Time, limit int, active func(context.Context, []string) (map[string]struct{}, error)) ([]string, error) {
	if s == nil || s.disk == nil || limit < 1 || active == nil {
		return nil, errors.New("Forms spool cleanup request is invalid")
	}
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	if s.cleanupScan == nil {
		var err error
		s.cleanupScan, err = s.scannerProvider.OpenPrefixScan(ctx, s.prefix)
		if err != nil {
			return nil, err
		}
	}
	page, err := s.cleanupScan.Next(ctx, limit)
	if err != nil {
		return nil, s.resetCleanupScanLocked(err)
	}
	if page.Done {
		if closeErr := s.cleanupScan.Close(); closeErr != nil {
			s.cleanupScan = nil
			return nil, closeErr
		}
		s.cleanupScan = nil
	}
	candidates := make([]string, 0, len(page.Keys))
	for _, key := range page.Keys {
		createdAt, ok := s.createdAt(key)
		if ok && createdAt.Before(olderThan) {
			candidates = append(candidates, key)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	protected, err := active(ctx, candidates)
	if err != nil {
		return nil, s.resetCleanupScanLocked(err)
	}
	deleted := []string{}
	for _, key := range candidates {
		if _, exists := protected[key]; exists {
			continue
		}
		if err := s.Delete(ctx, key); err != nil {
			return deleted, s.resetCleanupScanLocked(err)
		}
		deleted = append(deleted, key)
	}
	return deleted, nil
}

func (s *UploadSpool) Purge(ctx context.Context, limit int) (_ int, resultErr error) {
	if s == nil || s.disk == nil || limit < 1 {
		return 0, errors.New("Forms spool purge request is invalid")
	}
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	if err := s.resetCleanupScanLocked(nil); err != nil {
		return 0, err
	}
	scan, err := s.scannerProvider.OpenPrefixScan(ctx, s.prefix)
	if err != nil {
		return 0, err
	}
	defer func() { resultErr = errors.Join(resultErr, scan.Close()) }()
	deleted := 0
	for {
		page, err := scan.Next(ctx, limit)
		if err != nil {
			return deleted, err
		}
		for _, key := range page.Keys {
			if !strings.HasPrefix(key, s.prefix) {
				return deleted, errors.New("Forms spool scan escaped site prefix")
			}
			if err := s.disk.Delete(ctx, key); err != nil && !errors.Is(err, filesystem.ErrNotFound) {
				return deleted, err
			}
			deleted++
		}
		if page.Done {
			return deleted, nil
		}
	}
}

func (s *UploadSpool) CloseCleanupScan() error {
	if s == nil {
		return nil
	}
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	return s.resetCleanupScanLocked(nil)
}

func (s *UploadSpool) resetCleanupScanLocked(cause error) error {
	if s.cleanupScan == nil {
		return cause
	}
	err := s.cleanupScan.Close()
	s.cleanupScan = nil
	return errors.Join(cause, err)
}

func (s *UploadSpool) validKey(key string) bool {
	return s != nil && strings.HasPrefix(key, s.prefix) && validSpoolReference(key)
}

func validSpoolReference(key string) bool {
	if !strings.HasPrefix(key, spoolRootPrefix) || filepath.Clean(key) != key {
		return false
	}
	remainder := strings.TrimPrefix(key, spoolRootPrefix)
	siteValue, filename, ok := strings.Cut(remainder, "/")
	if !ok || strings.Contains(filename, "/") {
		return false
	}
	parsedSite, err := strconv.ParseInt(siteValue, 10, 64)
	if err != nil || parsedSite <= 0 {
		return false
	}
	_, ok = parseSpoolFilename(filename)
	return ok
}

func (s *UploadSpool) createdAt(key string) (time.Time, bool) {
	if !s.validKey(key) {
		return time.Time{}, false
	}
	return parseSpoolFilename(strings.TrimPrefix(key, s.prefix))
}

func parseSpoolFilename(raw string) (time.Time, bool) {
	seconds, opaqueID, ok := strings.Cut(raw, "-")
	if !ok {
		return time.Time{}, false
	}
	value, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil || value <= 0 || messageid.ID(opaqueID).Validate() != nil {
		return time.Time{}, false
	}
	return time.Unix(value, 0).UTC(), true
}

type uploadCountingReader struct {
	reader io.Reader
	count  int64
}

func (r *uploadCountingReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	r.count += int64(n)
	return n, err
}
