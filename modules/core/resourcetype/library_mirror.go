package resourcetype

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

type libraryMirrorType struct{}

func (libraryMirrorType) Code() Code         { return LibraryMirror }
func (libraryMirrorType) PathMode() PathMode { return PathRoute }
func (libraryMirrorType) Metadata() Metadata {
	metadata := pageType{}.Metadata()
	metadata.Label = "Зеркало библиотеки"
	metadata.Capabilities.MutableType = false
	metadata.Capabilities.MirrorsLibraryItems = true
	metadata.Capabilities.DefaultIcon = "fa-solid fa-copy"
	metadata.SettingsFields = []field.Definition{{Key: "source_library_id", Type: field.TypeInteger, Label: "Библиотека-источник", Required: true, Editor: "library-source-picker"}}
	return metadata
}

func (libraryMirrorType) Normalize(payload Payload) (Payload, error) {
	if len(payload.TypeSettings) != 1 || SourceLibraryID(payload.TypeSettings) <= 0 {
		return Payload{}, errors.New("library mirror source_library_id is required")
	}
	source := SourceLibraryID(payload.TypeSettings)
	payload.TypeSettings = nil
	normalized, err := (pageType{}).Normalize(payload)
	if err != nil {
		return Payload{}, err
	}
	normalized.TypeSettings = map[string]any{"source_library_id": source}
	return normalized, nil
}

// SourceLibraryID reads the type-owned relation from normalized or stored settings.
func SourceLibraryID(settings map[string]any) int64 {
	switch value := settings["source_library_id"].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		if value > 0 && value < 1<<63 && value == float64(int64(value)) {
			return int64(value)
		}
	case json.Number:
		id, err := strconv.ParseInt(string(value), 10, 64)
		if err == nil {
			return id
		}
	}
	return 0
}
