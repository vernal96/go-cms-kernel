package postgres

import (
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/forms"
)

func TestResultValueReferencesProtectScalarAndMultipleMediaValues(t *testing.T) {
	tests := []struct {
		name  string
		value forms.ResultValue
		want  []field.Reference
	}{
		{
			name:  "scalar file field",
			value: forms.ResultValue{ID: 12, FieldType: field.TypeFile, StorageKind: field.StorageReference, Value: int64(41)},
			want:  []field.Reference{{Target: field.ReferenceMedia, ID: 41, Path: []string{"12", "0"}}},
		},
		{
			name:  "ordered multiple file value",
			value: forms.ResultValue{ID: 13, FieldType: field.TypeFile, StorageKind: field.StorageReference, Multiple: true, Position: 2, Value: int64(42), ReferenceTarget: field.ReferenceFile},
			want:  []field.Reference{{Target: field.ReferenceMedia, ID: 42, Path: []string{"13", "2"}}},
		},
		{
			name:  "nested reference",
			value: forms.ResultValue{ID: 14, FieldType: "repeater", StorageKind: field.StorageJSON, References: []field.Reference{{Target: field.ReferenceFile, ID: 43, Path: []string{"items", "1", "photo"}}}},
			want:  []field.Reference{{Target: field.ReferenceMedia, ID: 43, Path: []string{"14", "0", "items", "1", "photo"}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resultValueReferences(test.value); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("result references = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestShiftOccurrencePathPreservesOrderAfterSelectedArrayEntryRemoval(t *testing.T) {
	removed := []string{"gallery", "1"}
	tests := []struct {
		path []string
		want []string
	}{
		{path: []string{"gallery", "0"}, want: []string{"gallery", "0"}},
		{path: []string{"gallery", "2"}, want: []string{"gallery", "1"}},
		{path: []string{"gallery", "3", "caption"}, want: []string{"gallery", "2", "caption"}},
		{path: []string{"other", "3"}, want: []string{"other", "3"}},
	}
	for _, test := range tests {
		if got := shiftOccurrencePath(test.path, removed); !reflect.DeepEqual(got, test.want) {
			t.Errorf("shiftOccurrencePath(%v) = %v, want %v", test.path, got, test.want)
		}
	}
}
