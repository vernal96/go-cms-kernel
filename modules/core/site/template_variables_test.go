package site

import (
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

func TestTemplateVariablesExposeOnlySafeBuiltinsAndProfileParams(t *testing.T) {
	t.Parallel()
	variables := NewTemplateVariables(Site{ID: 7, ProfileCode: "dev", Domain: "example.test", Locale: "ru-RU", IsPublic: true, Settings: map[string]any{"title": "ACME"}}, []field.Definition{{Key: "title", Type: field.TypeString, Label: "Title"}})
	metadata := variables.Metadata()
	if len(metadata) != 6 || metadata[0].Variable != "site.id" || metadata[5].Variable != "site.field.title" {
		t.Fatalf("metadata = %#v", metadata)
	}
	wants := map[string]any{"site.id": int64(7), "site.profile_code": "dev", "site.domain": "example.test", "site.locale": "ru-RU", "site.is_public": true, "site.field.title": "ACME"}
	for variable, want := range wants {
		got, exists := variables.Value(variable)
		if !exists || got != want {
			t.Fatalf("%s = %#v, %t", variable, got, exists)
		}
	}
	for _, forbidden := range []string{"site.created_by", "site.updated_at", "site.file_references", "site.secret"} {
		if _, exists := variables.Value(forbidden); exists {
			t.Fatalf("unsafe variable %q exposed", forbidden)
		}
	}
}
