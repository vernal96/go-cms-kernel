package field_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/security"
)

type recordingFieldUploads struct {
	file.ManagementService
	ensured int
	storage filesystem.Code
	path    string
	input   file.UploadInput
	bytes   []byte
}

func (s *recordingFieldUploads) EnsureFolderPath(_ context.Context, _ security.Actor, storage filesystem.Code, path string) (file.Folder, error) {
	s.ensured++
	s.storage, s.path = storage, path
	return file.Folder{ID: 42, Storage: storage}, nil
}

func (s *recordingFieldUploads) UploadAvailable(_ context.Context, _ security.Actor, input file.UploadInput) (file.File, error) {
	s.input = input
	var err error
	s.bytes, err = io.ReadAll(input.Content)
	return file.File{ID: 7, Storage: input.Storage, FolderID: input.FolderID}, err
}

func TestFieldUploadRejectsMIMEBeforeAnyMutation(t *testing.T) {
	files := &recordingFieldUploads{}
	options := field.FileOptions{Disk: "media", VirtualPath: "site/images", SettingsCode: "image", MIMETypes: []string{"image/*"}}
	_, err := field.UploadFile(context.Background(), security.User(1), files, options, "spoofed.png", bytes.NewBufferString("plain text"))
	var validation field.ValidationErrors
	if !errors.As(err, &validation) || validation[0].Code != "mime_type" {
		t.Fatalf("MIME rejection = %v", err)
	}
	if files.ensured != 0 || files.input.Content != nil {
		t.Fatal("rejected upload created a folder or stored bytes")
	}
}

func TestFieldUploadUsesExactConfiguredDestinationAndPreservesBytes(t *testing.T) {
	for _, allowed := range []string{"image/*", "image/png"} {
		t.Run(allowed, func(t *testing.T) {
			files := &recordingFieldUploads{}
			options := field.FileOptions{Disk: "images", VirtualPath: "deep/site/images", SettingsCode: "image", MIMETypes: []string{allowed}}
			content := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 1024)...)
			item, err := field.UploadFile(context.Background(), security.User(1), files, options, "image.png", bytes.NewReader(content))
			if err != nil {
				t.Fatal(err)
			}
			if item.ID != 7 || files.ensured != 1 || files.storage != "images" || files.path != options.VirtualPath || files.input.Storage != "images" || files.input.FolderID == nil || *files.input.FolderID != 42 {
				t.Fatalf("upload destination = %#v, ensure=%s:%s", files.input, files.storage, files.path)
			}
			if !bytes.Equal(content, files.bytes) {
				t.Fatal("sniffing changed uploaded content")
			}
		})
	}
}

func TestFileUploadOptionsResolvesOnlyFileAndRepeaterPaths(t *testing.T) {
	options := field.FileOptions{Disk: "images", VirtualPath: "site/images", SettingsCode: "image"}
	definitions := []field.Definition{
		{Key: "logo", Type: field.TypeFile, Options: options},
		{Key: "rows", Type: field.TypeRepeater, Options: field.RepeaterOptions{Fields: []field.Definition{{Key: "asset", Type: field.TypeFile, Options: options}}}},
	}
	for _, path := range [][]string{{"logo"}, {"rows", "0", "asset"}, {"rows", "12", "asset"}} {
		actual, err := field.FileUploadOptions(definitions, path)
		if err != nil || !reflect.DeepEqual(actual, options) {
			t.Fatalf("path %v = %#v, %v", path, actual, err)
		}
	}
	for _, path := range [][]string{nil, {"unknown"}, {"logo", "storage"}, {"rows", "-1", "asset"}, {"rows", "01", "asset"}, {"rows", "x", "asset"}, {"rows", "0", "unknown"}, {"rows", "0"}} {
		if _, err := field.FileUploadOptions(definitions, path); err == nil {
			t.Fatalf("invalid field path accepted: %v", path)
		}
	}
}
