package kernel

import (
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
)

type fileFieldTestDisks map[filesystem.Code]bool

func (d fileFieldTestDisks) Disk(code filesystem.Code) (filesystem.Disk, bool) {
	return nil, d[code]
}

func TestValidateProfileFileDisksIncludesNestedFields(t *testing.T) {
	profile := Profile{
		Code: "test",
		Params: []field.Definition{{Key: "logo", Type: field.TypeFile, Options: field.FileOptions{
			Disk: "public", VirtualPath: "site/logo", SettingsCode: "image",
		}}},
		Templates: []template.Definition{{Code: "page", Fields: []field.Definition{{
			Key: "rows", Type: field.TypeRepeater, Options: field.RepeaterOptions{Fields: []field.Definition{{
				Key: "attachment", Type: field.TypeFile, Options: field.FileOptions{
					Disk: "private", VirtualPath: "uploads", SettingsCode: "document",
				}},
			}}},
		}}},
	}
	if err := validateProfileFileDisks(profile, fileFieldTestDisks{"public": true, "private": true}); err != nil {
		t.Fatalf("valid file disks rejected: %v", err)
	}
	err := validateProfileFileDisks(profile, fileFieldTestDisks{"public": true})
	if err == nil || !strings.Contains(err.Error(), `unknown disk "private"`) {
		t.Fatalf("nested unknown disk error = %v", err)
	}
}

func TestValidateProfileFileDisksRejectsMissingOptions(t *testing.T) {
	profile := Profile{Code: "test", Params: []field.Definition{{Key: "logo", Type: field.TypeFile}}}
	err := validateProfileFileDisks(profile, fileFieldTestDisks{})
	if err == nil || !strings.Contains(err.Error(), `unknown disk ""`) {
		t.Fatalf("missing options error = %v", err)
	}
}
