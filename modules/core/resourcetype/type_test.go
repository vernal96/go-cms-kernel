package resourcetype

import (
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/template"
)

func TestPageTypeNormalizesDefaultsAndRejectsIncompatibleFields(
	t *testing.T,
) {
	page := standardType(t, Page)
	templateCode := template.Code("article")

	normalized, err := page.Normalize(Payload{
		Template: &templateCode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ContentType == nil ||
		*normalized.ContentType != "html" {
		t.Fatalf("content type = %#v", normalized.ContentType)
	}
	if normalized.Fields == nil {
		t.Fatal("settings map was not initialized")
	}

	withoutTemplate, err := page.Normalize(Payload{})
	if err != nil {
		t.Fatal(err)
	}
	if withoutTemplate.Template != nil || withoutTemplate.ContentType == nil ||
		*withoutTemplate.ContentType != "html" || withoutTemplate.Fields == nil {
		t.Fatalf("blank page normalization = %#v", withoutTemplate)
	}
	emptyTemplate := template.Code("")
	if _, err := page.Normalize(Payload{Template: &emptyTemplate}); err == nil {
		t.Fatal("page accepted an empty template code")
	}

	invalidContentType := "HTML"
	_, err = page.Normalize(Payload{
		Template:    &templateCode,
		ContentType: &invalidContentType,
	})
	if err == nil || !strings.Contains(err.Error(), "content_type") {
		t.Fatalf("invalid content type error = %v", err)
	}

	externalURL := "/outside"
	_, err = page.Normalize(Payload{
		Template:    &templateCode,
		ExternalURL: &externalURL,
	})
	if err == nil || !strings.Contains(err.Error(), "external_url") {
		t.Fatalf("external url error = %v", err)
	}
}

func TestLinkTypeURLPolicy(t *testing.T) {
	link := standardType(t, Link)
	if capabilities := link.Metadata().Capabilities; !capabilities.SupportsExternalURL || capabilities.SupportsTargetResource {
		t.Fatalf("link capabilities = %#v", capabilities)
	}

	for _, value := range []string{
		"https://example.com/docs",
		"http://example.com",
		"mailto:editor@example.com",
		"tel:+79991234567",
		"/relative/path?draft=1",
	} {
		t.Run(value, func(t *testing.T) {
			normalized, err := link.Normalize(Payload{
				ExternalURL: stringPointer(value),
			})
			if err != nil {
				t.Fatal(err)
			}
			if normalized.ExternalURL == nil ||
				*normalized.ExternalURL != value {
				t.Fatalf(
					"normalized external url = %#v",
					normalized.ExternalURL,
				)
			}
		})
	}

	for _, value := range []string{
		"",
		"example.com",
		"ftp://example.com",
		"//example.com/path",
		" https://example.com",
	} {
		t.Run("invalid_"+value, func(t *testing.T) {
			_, err := link.Normalize(Payload{
				ExternalURL: stringPointer(value),
			})
			if err == nil {
				t.Fatalf("accepted invalid url %q", value)
			}
		})
	}

	contentType := "html"
	_, err := link.Normalize(Payload{
		ContentType: &contentType,
		ExternalURL: stringPointer("/docs"),
	})
	if err == nil || !strings.Contains(err.Error(), "content_type") {
		t.Fatalf("incompatible content type error = %v", err)
	}
}

func TestResourceLinkTypeRequiresTarget(t *testing.T) {
	resourceLink := standardType(t, ResourceLink)
	if capabilities := resourceLink.Metadata().Capabilities; !capabilities.SupportsTargetResource || capabilities.SupportsExternalURL {
		t.Fatalf("resource_link capabilities = %#v", capabilities)
	}

	if _, err := resourceLink.Normalize(Payload{}); err == nil {
		t.Fatal("resource_link accepted missing target")
	}

	targetID := int64(42)
	normalized, err := resourceLink.Normalize(Payload{
		TargetResourceID: &targetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.TargetResourceID == nil ||
		*normalized.TargetResourceID != targetID {
		t.Fatalf("target id = %#v", normalized.TargetResourceID)
	}

	externalURL := "/docs"
	if _, err := resourceLink.Normalize(Payload{
		TargetResourceID: &targetID,
		ExternalURL:      &externalURL,
	}); err == nil || !strings.Contains(err.Error(), "external_url") {
		t.Fatalf("incompatible external url error = %v", err)
	}
}

func TestLibraryTypeCapabilitiesAndURLPattern(t *testing.T) {
	library := standardType(t, Library)
	metadata := library.Metadata()
	capabilities := metadata.Capabilities
	if !capabilities.SupportsTemplate || !capabilities.SupportsContent ||
		!capabilities.SupportsWidgets || !capabilities.SupportsFields ||
		capabilities.MutableType || !capabilities.OwnsLibraryItems ||
		capabilities.DefaultIcon == "" {
		t.Fatalf("library capabilities = %#v", capabilities)
	}
	if metadata.Label != "Библиотека" || len(metadata.SettingsFields) != 2 ||
		metadata.SettingsDefaults["item_url_pattern"] != DefaultItemURLPattern ||
		len(metadata.ContentTypes) != 1 || metadata.ContentTypes[0].Code != "html" {
		t.Fatalf("library metadata = %#v", metadata)
	}

	normalized, err := library.Normalize(Payload{TypeSettings: map[string]any{
		"item_url_pattern":      "/{year}/{slug}",
		"default_item_template": "article",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.TypeSettings["item_url_pattern"] != "/{year}/{slug}" {
		t.Fatalf("library settings = %#v", normalized.TypeSettings)
	}
	normalized, err = library.Normalize(Payload{TypeSettings: map[string]any{
		"item_url_pattern": "", "default_item_template": nil,
	}})
	if err != nil || normalized.TypeSettings["item_url_pattern"] != DefaultItemURLPattern {
		t.Fatalf("normalized optional library settings = %#v, %v", normalized.TypeSettings, err)
	}
	if _, exists := normalized.TypeSettings["default_item_template"]; exists {
		t.Fatalf("empty default template was preserved: %#v", normalized.TypeSettings)
	}
	if err := ValidateLibrarySettings(map[string]any{"item_url_pattern": 42}); err == nil {
		t.Fatal("accepted non-string item URL pattern")
	}
	for _, pattern := range []string{"/{year}", "/{unknown}/{slug}", "{slug}", "/{slug}/"} {
		if err := ValidateItemURLPattern(pattern); err == nil {
			t.Fatalf("accepted invalid pattern %q", pattern)
		}
	}
}

func standardType(t *testing.T, code Code) Type {
	t.Helper()

	for _, resourceType := range StandardTypes() {
		if resourceType.Code() == code {
			return resourceType
		}
	}
	t.Fatalf("standard type %q is missing", code)
	return nil
}

func stringPointer(value string) *string {
	return &value
}
