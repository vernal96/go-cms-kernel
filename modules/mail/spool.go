package mail

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
const spoolRootPrefix = "mail-spool/"

type AttachmentSpool struct {
	disk            filesystem.Disk
	scannerProvider filesystem.PrefixScannerProvider
	prefix          string
	cleanupMu       sync.Mutex
	cleanupScan     filesystem.PrefixScan
}

func NewAttachmentSpool(siteID site.ID, disk filesystem.Disk) (*AttachmentSpool, error) {
	if siteID <= 0 {
		return nil, errors.New("mail attachment spool site is invalid")
	}
	if disk == nil {
		return nil, errors.New("mail attachment spool disk is nil")
	}
	if disk.Visibility() != filesystem.VisibilityPrivate {
		return nil, errors.New("mail attachment spool must be private")
	}
	scannerProvider, ok := disk.(filesystem.PrefixScannerProvider)
	if !ok {
		return nil, errors.New("mail attachment spool does not support bounded cleanup")
	}
	return &AttachmentSpool{disk: disk, scannerProvider: scannerProvider, prefix: spoolRootPrefix + fmt.Sprint(siteID) + "/"}, nil
}

func (s *AttachmentSpool) Put(ctx context.Context, input TransientAttachment, maxSize int64) (Attachment, error) {
	if s == nil || s.disk == nil {
		return Attachment{}, errors.New("mail transient attachments are disabled")
	}
	filename := strings.TrimSpace(input.Filename)
	if filename == "" || filepath.Base(filename) != filename || filename == "." {
		return Attachment{}, fmt.Errorf("%w: transient attachment filename is invalid", ErrInvalid)
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(input.MIMEType))
	if err != nil || mediaType == "" {
		return Attachment{}, fmt.Errorf("%w: transient attachment MIME type is invalid", ErrInvalid)
	}
	if input.Body == nil || input.Size < 0 || maxSize < 1 || input.Size > maxSize {
		return Attachment{}, fmt.Errorf("%w: transient attachment size is invalid", ErrInvalid)
	}
	id, err := messageid.New()
	if err != nil {
		return Attachment{}, err
	}
	key := s.prefix + strconv.FormatInt(time.Now().UTC().Unix(), 10) + "-" + string(id)
	hash := sha256.New()
	counter := &countingReader{reader: io.TeeReader(io.LimitReader(input.Body, input.Size+1), hash)}
	if err := s.disk.PutNew(ctx, key, counter, mediaType); err != nil {
		return Attachment{}, fmt.Errorf("write transient mail attachment: %w", err)
	}
	if counter.count != input.Size {
		_ = s.disk.Delete(context.WithoutCancel(ctx), key)
		return Attachment{}, fmt.Errorf("%w: transient attachment size does not match body", ErrInvalid)
	}
	return newStoredAttachment(AttachmentTransient, nil, key, filename, mediaType, input.Size, hex.EncodeToString(hash.Sum(nil))), nil
}

func (s *AttachmentSpool) Open(ctx context.Context, attachment Attachment) (io.ReadCloser, error) {
	if s == nil || s.disk == nil || attachment.Source != AttachmentTransient || !s.validKey(attachment.spoolKey) {
		return nil, errors.New("mail transient attachment reference is invalid")
	}
	return s.disk.Open(ctx, attachment.spoolKey)
}

func (s *AttachmentSpool) Delete(ctx context.Context, attachment Attachment) error {
	if attachment.Source != AttachmentTransient {
		return nil
	}
	if s == nil || s.disk == nil || !s.validKey(attachment.spoolKey) {
		return errors.New("mail transient attachment reference is invalid")
	}
	return s.disk.Delete(ctx, attachment.spoolKey)
}

func (s *AttachmentSpool) Cleanup(ctx context.Context, olderThan time.Time, limit int, active func(context.Context, []string) (map[string]struct{}, error)) (int, error) {
	if s == nil || s.disk == nil || limit < 1 || active == nil {
		return 0, errors.New("mail spool cleanup request is invalid")
	}
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	if s.cleanupScan == nil {
		var err error
		s.cleanupScan, err = s.scannerProvider.OpenPrefixScan(ctx, s.prefix)
		if err != nil {
			return 0, err
		}
	}
	page, err := s.cleanupScan.Next(ctx, limit)
	if err != nil {
		return 0, s.resetCleanupScanLocked(err)
	}
	if page.Done {
		if closeErr := s.cleanupScan.Close(); closeErr != nil {
			s.cleanupScan = nil
			return 0, closeErr
		}
		s.cleanupScan = nil
	}
	candidates := make([]string, 0, limit)
	for _, key := range page.Keys {
		createdAt, ok := s.createdAt(key)
		if ok && createdAt.Before(olderThan) {
			candidates = append(candidates, key)
		}
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	protected, err := active(ctx, candidates)
	if err != nil {
		return 0, s.resetCleanupScanLocked(err)
	}
	deleted := 0
	for _, key := range candidates {
		if _, exists := protected[key]; exists {
			continue
		}
		if err := s.disk.Delete(ctx, key); err != nil && !errors.Is(err, filesystem.ErrNotFound) {
			return deleted, s.resetCleanupScanLocked(err)
		}
		deleted++
	}
	return deleted, nil
}

// Purge removes every object owned by this site-scoped spool. Callers must
// first establish that no active Message can reference the prefix.
func (s *AttachmentSpool) Purge(ctx context.Context, limit int) (_ int, resultErr error) {
	if s == nil || s.disk == nil || limit < 1 {
		return 0, errors.New("mail spool purge request is invalid")
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
		page, nextErr := scan.Next(ctx, limit)
		if nextErr != nil {
			return deleted, nextErr
		}
		for _, key := range page.Keys {
			if !strings.HasPrefix(key, s.prefix) {
				return deleted, errors.New("mail spool scan escaped its site prefix")
			}
			if deleteErr := s.disk.Delete(ctx, key); deleteErr != nil && !errors.Is(deleteErr, filesystem.ErrNotFound) {
				return deleted, deleteErr
			}
			deleted++
		}
		if page.Done {
			return deleted, nil
		}
	}
}

func (s *AttachmentSpool) CloseCleanupScan() error {
	if s == nil {
		return nil
	}
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	return s.resetCleanupScanLocked(nil)
}

func (s *AttachmentSpool) resetCleanupScanLocked(cause error) error {
	if s.cleanupScan == nil {
		return cause
	}
	closeErr := s.cleanupScan.Close()
	s.cleanupScan = nil
	return errors.Join(cause, closeErr)
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	r.count += int64(n)
	return n, err
}

func (s *AttachmentSpool) validKey(key string) bool {
	return s != nil && strings.HasPrefix(key, s.prefix) && validSpoolKey(key)
}

func (s *AttachmentSpool) createdAt(key string) (time.Time, bool) {
	if !s.validKey(key) {
		return time.Time{}, false
	}
	raw := strings.TrimPrefix(key, s.prefix)
	return parseSpoolFilename(raw)
}

func validSpoolKey(key string) bool {
	if !strings.HasPrefix(key, spoolRootPrefix) || filepath.Clean(key) != key {
		return false
	}
	remainder := strings.TrimPrefix(key, spoolRootPrefix)
	siteValue, filename, ok := strings.Cut(remainder, "/")
	if !ok || siteValue == "" || filename == "" || strings.Contains(filename, "/") {
		return false
	}
	parsedSite, err := strconv.ParseInt(siteValue, 10, 64)
	if err != nil || parsedSite <= 0 {
		return false
	}
	_, ok = parseSpoolFilename(filename)
	return ok
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
