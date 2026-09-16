package resource

import "testing"

func TestGenerateSlug(t *testing.T) {
	tests := map[string]string{
		"Новый раздел: Лето 2026!": "novyy-razdel-leto-2026",
		"Product API v2":  "product-api-v2",
		"  Ёлка и щука  ": "elka-i-schuka",
	}
	for source, expected := range tests {
		if actual := GenerateSlug(source); actual != expected {
			t.Fatalf("GenerateSlug(%q) = %q, want %q", source, actual, expected)
		}
	}
}
