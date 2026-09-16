package mail

import (
	"encoding/json"

	"github.com/vernal96/go-cms-kernel/modules/core/file"
)

type storedAttachment struct {
	Source   AttachmentSource `json:"source"`
	FileID   *file.ID         `json:"file_id,omitempty"`
	SpoolKey string           `json:"spool_key,omitempty"`
	Filename string           `json:"filename"`
	MIMEType string           `json:"mime_type"`
	Size     int64            `json:"size"`
	Checksum string           `json:"checksum_sha256"`
}

func EncodeAttachmentsForStorage(items []Attachment) ([]byte, error) {
	stored := make([]storedAttachment, len(items))
	for index, item := range items {
		stored[index] = storedAttachment{Source: item.Source, FileID: item.FileID, SpoolKey: item.spoolKey, Filename: item.Filename, MIMEType: item.MIMEType, Size: item.Size, Checksum: item.Checksum}
	}
	return json.Marshal(stored)
}

func DecodeAttachmentsFromStorage(raw []byte) ([]Attachment, error) {
	var stored []storedAttachment
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	result := make([]Attachment, len(stored))
	for index, item := range stored {
		result[index] = newStoredAttachment(item.Source, item.FileID, item.SpoolKey, item.Filename, item.MIMEType, item.Size, item.Checksum)
	}
	return result, nil
}
