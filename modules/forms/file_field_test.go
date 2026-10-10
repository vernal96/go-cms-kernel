package forms

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	corefile "github.com/vernal96/go-cms-kernel/modules/core/file"
	coremedia "github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

func TestElementImageUsesConfiguredFileOptionsAndMediaURL(t *testing.T) {
	options := field.FileOptions{Disk: "public", VirtualPath: "configured/site", SettingsCode: "site_image", MIMETypes: []string{"image/png"}}
	catalog, err := newElementCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	imageType, exists := catalog.Type(ElementImage)
	if !exists {
		t.Fatal("image element type is missing")
	}
	config := imageType.Metadata().Fields[0].Options
	encoded, err := field.EncodeOptionsJSON(config)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := field.DecodeOptions[field.FileOptions](encoded)
	if err != nil || decoded.Disk != options.Disk || decoded.VirtualPath != options.VirtualPath || decoded.SettingsCode != options.SettingsCode || len(decoded.MIMETypes) != 1 || decoded.MIMETypes[0] != "image/png" {
		t.Fatalf("image file options = %#v, err=%v", decoded, err)
	}
	fileService := &filesStub{}
	mediaService := &mediaStub{resolved: coremedia.ResolvedMedia{Media: coremedia.Media{ID: 19, FileID: 73}, File: corefile.File{ID: 73, Storage: filesystem.Code("public"), MIMEType: "image/png"}}}
	service := &Service{elements: catalog, fieldTypes: formsFieldResolver(), files: fileService, media: mediaService}
	raw := json.RawMessage(`{"file_id":19,"alt":"Logo"}`)
	references, err := service.elementReferences(Element{Type: ElementImage, Config: raw})
	if err != nil || len(references) != 1 || references[0].Target != field.ReferenceFile || references[0].ID != 19 || len(references[0].Path) != 1 || references[0].Path[0] != "file_id" {
		t.Fatalf("normalized image references = %#v, err=%v", references, err)
	}
	if err := service.validateImage(context.Background(), security.System(), raw); err != nil {
		t.Fatalf("valid media selection rejected: %v", err)
	}
	if fileService.urlFileID != 73 {
		t.Fatalf("URL resolved file ID = %d, want current Media file ID 73", fileService.urlFileID)
	}
	url, err := service.PublicImageURL(context.Background(), raw)
	if err != nil || url != "/files/current" || fileService.urlFileID != 73 {
		t.Fatalf("public image URL = %q, file ID=%d, err=%v", url, fileService.urlFileID, err)
	}
	mediaService.resolved.File.MIMEType = "application/pdf"
	if err := service.validateImage(context.Background(), security.System(), raw); !errors.Is(err, ErrInvalid) {
		t.Fatalf("MIME mismatch error = %v, want ErrInvalid", err)
	}
	mediaService.resolved.File.MIMEType = "image/png"
	mediaService.resolved.File.Storage = filesystem.Code("private")
	if err := service.validateImage(context.Background(), security.System(), raw); !errors.Is(err, ErrInvalid) {
		t.Fatalf("disk mismatch error = %v, want ErrInvalid", err)
	}
}

func TestFormsImageOptionsAreRequiredAtModuleValidation(t *testing.T) {
	config := Config{
		ActionMaxAttempts:      1,
		DefaultCaptchaProvider: "test",
		Public: PublicLimits{
			MaxRequestSize: 1, MaxScalarFields: 1, MaxScalarValueSize: 1,
			MaxUploadFileSize: 1, MaxUploadCount: 1, MaxTotalUploadBytes: 1,
			SubmissionTimeout: 1, RateLimit: 1, RateWindow: 1, RateEntries: 1,
		},
	}
	if _, err := normalizeConfig(config); err == nil {
		t.Fatal("Forms module accepted missing element image disk/path/settings code")
	}
	config.ElementImage = testImageFileOptions()
	if _, err := normalizeConfig(config); err != nil {
		t.Fatalf("Forms module rejected configured image options: %v", err)
	}
}

func TestSubmissionFileFieldsResolveMediaAndEnforceDiskAndMIME(t *testing.T) {
	options := field.FileOptions{Disk: "public", VirtualPath: "forms/uploads", SettingsCode: "form_upload", MIMETypes: []string{"image/*"}}
	schema, err := field.CompilePersistent([]field.Definition{{Key: "attachment", Type: field.TypeFile, Label: "Attachment", Required: true, Options: options}}, formsFieldResolver())
	if err != nil {
		t.Fatal(err)
	}
	values, err := schema.Validate(map[string]any{"attachment": int64(19)})
	if err != nil {
		t.Fatal(err)
	}
	mediaService := &mediaStub{resolved: coremedia.ResolvedMedia{Media: coremedia.Media{ID: 19, FileID: 73}, File: corefile.File{ID: 73, Storage: filesystem.Code("public"), MIMEType: "image/png"}}}
	service := &Service{media: mediaService}
	if err := service.validateFileReferences(context.Background(), schema, values); err != nil {
		t.Fatalf("valid selected Media was rejected: %v", err)
	}
	mediaService.resolved.File.MIMEType = "application/pdf"
	if err := service.validateFileReferences(context.Background(), schema, values); err == nil {
		t.Fatal("MIME mismatch was accepted")
	}
	mediaService.resolved.File.MIMEType = "image/png"
	mediaService.resolved.File.Storage = filesystem.Code("private")
	if err := service.validateFileReferences(context.Background(), schema, values); err == nil {
		t.Fatal("disk mismatch was accepted")
	}
}
