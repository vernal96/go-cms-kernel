package media

import (
	"context"
	"fmt"
	"slices"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
)

const FileFieldUsage UsageKind = "field.file"

// FileOccurrenceValues are read from the Media update unit of work. Code is the
// owner's template, widget or element definition; Values are its saved values.
type FileOccurrenceValues struct {
	Code       string
	Values     map[string]any
	References []field.Reference
}

type FileOccurrenceReader interface {
	ReadFileOccurrence(context.Context, FileOccurrence) (FileOccurrenceValues, error)
}

// FileOccurrenceValidator is contributed by the owning site module. It checks
// current runtime definitions without putting module schemas in Media storage.
type FileOccurrenceValidator interface {
	FileOccurrenceKinds() []string
	ValidateMediaFileOccurrence(context.Context, FileOccurrence, file.File) error
}

func ValidateFileOccurrence(ref FileOccurrence, references []field.Reference, target file.File) error {
	if ref.Target != field.ReferenceFile {
		return fmt.Errorf("%w: expected File reference", ErrInvalidReference)
	}
	for _, current := range references {
		if current.ID != int64(ref.MediaID) || current.Target != ref.Target || !slices.Equal(current.Path, ref.Path) {
			continue
		}
		if !field.FileMatches(current.Options, target.Storage, target.MIMEType) {
			return field.ValidationErrors{{Key: field.ReferenceKey(ref.Path), Code: "file_constraints", Params: map[string]any{"disk": current.Options.Disk, "mime_type": target.MIMEType, "allowed_mime_types": append([]string(nil), current.Options.MIMETypes...)}}}
		}
		return nil
	}
	return fmt.Errorf("%w: saved file occurrence %s/%d/%s no longer matches its schema", ErrInvalidReference, ref.OwnerKind, ref.OwnerID, field.ReferenceKey(ref.Path))
}
