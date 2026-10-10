package field

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

// FileUploadOptions resolves a file definition using object keys and repeater
// indices. Definitions, not client-supplied storage options, are authoritative.
func FileUploadOptions(definitions []Definition, path []string) (FileOptions, error) {
	if len(path) == 0 {
		return FileOptions{}, errors.New("file field path is empty")
	}
	for _, definition := range definitions {
		if definition.Key != path[0] {
			continue
		}
		if len(path) == 1 && definition.Type == TypeFile {
			return FileOptionsValue(definition.Options)
		}
		if definition.Type == TypeRepeater && len(path) >= 3 {
			index, err := strconv.Atoi(path[1])
			if err != nil || index < 0 || strconv.Itoa(index) != path[1] {
				return FileOptions{}, errors.New("file field repeater index is invalid")
			}
			options, err := DecodeOptions[RepeaterOptions](definition.Options)
			if err != nil {
				return FileOptions{}, err
			}
			return FileUploadOptions(options.Fields, path[2:])
		}
		break
	}
	return FileOptions{}, fmt.Errorf("file field %q does not exist", ReferenceKey(path))
}

// UploadFile stores a file in the exact configured virtual folder. Callers must
// resolve options from a trusted owner and authorize that owner's mutation.
// MIME rejection happens before folder creation or physical byte storage.
func UploadFile(ctx context.Context, actor security.Actor, files file.ManagementService, options FileOptions, name string, content io.Reader) (file.File, error) {
	if content == nil {
		return file.File{}, errors.New("file upload content is nil")
	}
	header := make([]byte, 512)
	count, err := io.ReadFull(content, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return file.File{}, fmt.Errorf("read file header: %w", err)
	}
	header = header[:count]
	if !FileMatches(options, options.Disk, http.DetectContentType(header)) {
		return file.File{}, ValidationErrors{{Code: "mime_type"}}
	}
	folder, err := files.EnsureFolderPath(ctx, actor, options.Disk, options.VirtualPath)
	if err != nil {
		return file.File{}, err
	}
	return files.UploadAvailable(ctx, actor, file.UploadInput{
		Storage: options.Disk, FolderID: &folder.ID, Name: name,
		Content: io.MultiReader(bytes.NewReader(header), content),
	})
}
