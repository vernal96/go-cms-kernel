package postgres

import (
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/forms"
)

func TestFieldOptionsCodecPreservesContributedAndFileOptions(t *testing.T) {
	tests := []forms.FormField{
		{Code: "custom", Type: "custom.type", Options: map[string]any{"mode": "strict", "limit": float64(3)}},
		{Code: "asset", Type: field.TypeFile, Options: field.FileOptions{Storages: []filesystem.Code{"public"}, MIMETypes: []string{"image/*"}}},
	}
	for _, testCase := range tests {
		raw, err := encodeFieldOptions(testCase)
		if err != nil {
			t.Fatalf("encode %s: %v", testCase.Code, err)
		}
		decoded, err := decodeFieldOptions(testCase.Type, raw)
		if err != nil {
			t.Fatalf("decode %s: %v", testCase.Code, err)
		}
		if !reflect.DeepEqual(decoded, testCase.Options) {
			t.Fatalf("%s options = %#v, want %#v", testCase.Code, decoded, testCase.Options)
		}
	}
}
