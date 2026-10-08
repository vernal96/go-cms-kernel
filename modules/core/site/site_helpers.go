package site

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

func runtimePreparationError(operation string, err error) error {
	if errors.Is(err, kernel.ErrRuntimeTransitionBlocked) {
		return fmt.Errorf("%w: %s: %w", ErrConflict, operation, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func cloneSnapshot(current *runtimeSnapshot, delta int) *runtimeSnapshot {
	capacity := len(current.byID) + delta
	if capacity < 0 {
		capacity = 0
	}
	next := &runtimeSnapshot{
		byDomain: make(map[string]*Runtime, capacity),
		byID:     make(map[ID]*Runtime, capacity),
	}
	for domain, runtime := range current.byDomain {
		next.byDomain[domain] = runtime
	}
	for id, runtime := range current.byID {
		next.byID[id] = runtime
	}
	return next
}

func NormalizeDomain(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("site domain is empty")
	}

	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}

	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	value = strings.TrimRight(value, ".")
	value = strings.ToLower(strings.TrimSpace(value))

	if value == "" {
		return "", errors.New("site domain is empty")
	}

	if net.ParseIP(value) == nil &&
		strings.ContainsAny(value, " /\\@:#") {
		return "", fmt.Errorf(
			"invalid site domain %q",
			value,
		)
	}

	return value, nil
}

func cloneSettings(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}

	result := make(map[string]any, len(source))

	for key, value := range source {
		result[key] = cloneSettingValue(value)
	}

	return result
}

func (c *Catalog) validateFileReferences(
	ctx context.Context,
	actor security.Actor,
	references []field.FileReference,
	trusted map[string]file.ID,
) error {
	if len(references) == 0 {
		return nil
	}
	if c.files == nil {
		return errors.New("site file service is unavailable")
	}
	for _, reference := range references {
		if trusted[reference.Key] == file.ID(reference.ID) {
			continue
		}
		item, err := c.files.GetFile(ctx, actor, file.ID(reference.ID))
		if err != nil {
			return fmt.Errorf("file field %q: %w", reference.Key, err)
		}
		if !field.FileMatches(reference.Options, item.Storage, item.MIMEType) {
			return fmt.Errorf(
				"file field %q rejects file with MIME type %q in storage %q; allowed MIME types: %v; allowed storages: %v: %w",
				reference.Key,
				item.MIMEType,
				item.Storage,
				reference.Options.MIMETypes,
				reference.Options.Storages,
				field.ValidationErrors{{
					Key:  reference.Key,
					Code: "file_constraints",
					Params: map[string]any{
						"mime_type":          item.MIMEType,
						"allowed_mime_types": append([]string(nil), reference.Options.MIMETypes...),
						"allowed_storages":   append([]filesystem.Code(nil), reference.Options.Storages...),
					},
				}},
			)
		}
	}
	return nil
}

func fileReferenceMap(references []field.FileReference) map[string]file.ID {
	if len(references) == 0 {
		return nil
	}
	result := make(map[string]file.ID, len(references))
	for _, reference := range references {
		result[reference.Key] = file.ID(reference.ID)
	}
	return result
}

func cloneFileReferences(source map[string]file.ID) map[string]file.ID {
	if source == nil {
		return nil
	}
	result := make(map[string]file.ID, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneUserID(value *security.UserID) *security.UserID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneSettingValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSettings(typed)

	case []any:
		result := make([]any, len(typed))

		for index, item := range typed {
			result[index] = cloneSettingValue(item)
		}

		return result

	case []string:
		return append([]string(nil), typed...)

	default:
		return typed
	}
}
