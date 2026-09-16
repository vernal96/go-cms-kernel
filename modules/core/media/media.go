package media

import (
	"context"
	"errors"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

type ID int64
type UsageKind string

var (
	ErrNotFound         = errors.New("media not found")
	ErrInvalidReference = errors.New("invalid media reference")
	ErrAlreadyAttached  = errors.New("media is already attached")
	ErrUnknownUsage     = errors.New("unknown media usage")
)

type Media struct {
	ID        ID
	FileID    file.ID
	Title     *string
	Params    map[string]any
	CreatedAt time.Time
	UpdatedAt time.Time
	CreatedBy *security.UserID
	UpdatedBy *security.UserID
}

type ResolvedMedia struct {
	Media Media
	File  file.File
}

type CreateInput struct {
	FileID file.ID
	Title  *string
	Params map[string]any
}

type UpdateInput struct {
	ID     ID
	FileID file.ID
	Title  *string
	Params map[string]any
}

type Usage struct {
	Kind    UsageKind
	OwnerID int64
}

type ValidateUsages func(context.Context, []Usage) error

type Repository interface {
	Create(
		context.Context,
		*security.UserID,
		Media,
	) (Media, error)
	ByID(context.Context, ID) (Media, error)
	Update(
		context.Context,
		*security.UserID,
		Media,
		ValidateUsages,
	) (Media, error)
	Delete(context.Context, ID) error
}

type Service interface {
	Create(
		context.Context,
		security.Actor,
		CreateInput,
	) (Media, error)
	Get(context.Context, security.Actor, ID) (Media, error)
	Resolve(
		context.Context,
		security.Actor,
		ID,
	) (ResolvedMedia, error)
	Update(
		context.Context,
		security.Actor,
		UpdateInput,
	) (Media, error)
	Delete(context.Context, security.Actor, ID) error
}

type FilePolicy func(context.Context, file.File, Usage) error

type FilePolicies map[UsageKind]FilePolicy

func Clone(item Media) Media {
	item.Title = cloneString(item.Title)
	item.Params = cloneMap(item.Params)
	item.CreatedBy = cloneUserID(item.CreatedBy)
	item.UpdatedBy = cloneUserID(item.UpdatedBy)
	return item
}

func cloneUserID(value *security.UserID) *security.UserID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}

	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneValue(value)
	}
	return result
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}
