package resource

import (
	"context"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/security"
)

func TestResourceImageEnforcesProfileMediaOptions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		options   *field.MediaOptions
		storage   string
		mime      string
		wantError bool
	}{
		{"disabled", nil, "public", "image/png", true},
		{"wrong disk", &field.MediaOptions{Disk: "public", VirtualPath: "images", SettingsCode: "image"}, "private", "image/png", true},
		{"wrong MIME", &field.MediaOptions{Disk: "public", VirtualPath: "images", SettingsCode: "image", MIMETypes: []string{"image/png"}}, "public", "image/jpeg", true},
		{"non-image", &field.MediaOptions{Disk: "public", VirtualPath: "images", SettingsCode: "image"}, "public", "application/pdf", true},
		{"valid", &field.MediaOptions{Disk: "public", VirtualPath: "images", SettingsCode: "image", MIMETypes: []string{"image/png"}}, "public", "image/png", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _, medias := newTestService(t, tc.options)
			addResolvedMedia(medias, 1, 11, tc.mime)
			resolved := medias.items[1]
			resolved.File.Storage = filesystem.Code(tc.storage)
			medias.items[1] = resolved
			id := media.ID(1)
			_, err := service.Create(context.Background(), security.System(), CreateInput{SiteID: 1, Title: "Root", ImageMediaID: &id})
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%t", err, tc.wantError)
			}
		})
	}
}
