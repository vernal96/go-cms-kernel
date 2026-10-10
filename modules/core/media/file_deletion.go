package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

var (
	ErrFileInUse             = errors.New("media file has other references")
	ErrFileDeleteConflict    = errors.New("media file deletion conflicts with current state")
	ErrFileDeleteUnsupported = errors.New("media occurrence does not support file deletion")
)

// FileOccurrence is a saved selection, not an inferred number inside arbitrary JSON.
type FileOccurrence struct {
	OwnerKind string   `json:"owner_kind"`
	OwnerID   int64    `json:"owner_id"`
	SiteID    int64    `json:"site_id"`
	Container string   `json:"container"`
	Path      []string `json:"path"`
	MediaID   ID       `json:"media_id"`
	Target    string   `json:"target"`
	LibraryID int64    `json:"-"`
}

// FileOccurrenceOwner contributes a module-owned mutation to the deletion unit
// of work. Implementations must use that unit of work, never commit independently.
type FileOccurrenceOwner interface {
	Kind() string
	UpdatePermission() permission.Code
	ClearFileOccurrence(context.Context, *security.UserID, FileOccurrence) (int64, error)
}

type DeleteFileInput struct {
	SiteID            int64
	MediaID           ID
	ExpectedFileID    file.ID
	ExpectedUpdatedAt time.Time
}

type ClearedFileReference struct {
	FileOccurrence
	OwnerVersion int64 `json:"owner_version"`
}

type FileDeletion struct {
	OperationID      string                `json:"operation_id"`
	Status           string                `json:"status"`
	SiteID           int64                 `json:"-"`
	MediaID          ID                    `json:"media_id"`
	DeletedFileIDs   []file.ID             `json:"deleted_file_ids"`
	ClearedReference *ClearedFileReference `json:"cleared_reference"`
	StatusURL        string                `json:"status_url"`
}

// PreparedFileOwner keeps the owner lifecycle prepared until the SQL outcome is
// known. Publish and Abort release any runtime preparations and process locks.
type PreparedFileOwner struct {
	Apply   func(context.Context) (*ClearedFileReference, error)
	Publish func()
	Abort   func()
}

type PrepareFileOwner func(context.Context, *FileOccurrence) (PreparedFileOwner, error)

type FileDeletionRepository interface {
	FileOccurrences(context.Context, ID) ([]FileOccurrence, error)
	DeleteMediaFile(context.Context, *security.UserID, DeleteFileInput, PrepareFileOwner) (FileDeletion, error)
	FileDeletion(context.Context, int64, string) (FileDeletion, error)
	CleanFileDeletion(context.Context, int64, string, file.DeletePhysical) (FileDeletion, error)
	PendingFileDeletions(context.Context, int) ([]FileDeletion, error)
}

type FileDeletionService interface {
	Delete(context.Context, security.Actor, DeleteFileInput) (FileDeletion, error)
	Status(context.Context, security.Actor, int64, string) (FileDeletion, error)
}

// PruneFileOccurrence removes exactly the selected ID. Array removal preserves
// order; deleting an object member retains its repeater row and siblings.
func PruneFileOccurrence(value any, path []string, expected ID) (any, error) {
	if len(path) == 0 {
		var id int64
		switch v := value.(type) {
		case int64:
			id = v
		case int:
			id = int64(v)
		case json.Number:
			id, _ = v.Int64()
		default:
			return nil, ErrFileDeleteConflict
		}
		if id != int64(expected) {
			return nil, ErrFileDeleteConflict
		}
		return nil, nil
	}
	switch v := value.(type) {
	case map[string]any:
		child, exists := v[path[0]]
		if !exists {
			return nil, ErrFileDeleteConflict
		}
		next, err := PruneFileOccurrence(child, path[1:], expected)
		if err != nil {
			return nil, err
		}
		result := make(map[string]any, len(v))
		for key, item := range v {
			result[key] = item
		}
		if len(path) == 1 {
			delete(result, path[0])
		} else {
			result[path[0]] = next
		}
		return result, nil
	case []any:
		index, err := strconv.Atoi(path[0])
		if err != nil || index < 0 || index >= len(v) {
			return nil, ErrFileDeleteConflict
		}
		next, err := PruneFileOccurrence(v[index], path[1:], expected)
		if err != nil {
			return nil, err
		}
		result := append([]any{}, v...)
		if len(path) == 1 {
			result = append(result[:index], result[index+1:]...)
		} else {
			result[index] = next
		}
		return result, nil
	default:
		return nil, fmt.Errorf("%w: invalid occurrence path", ErrFileDeleteConflict)
	}
}
