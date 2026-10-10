package mail

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

const mailUploadTimeout = 10 * time.Minute

type variableFileUploadTarget struct {
	FieldPath []string `json:"field_path"`
}

// variableFileResponse mirrors the Core field-upload file item DTO so the
// DynamicFieldsForm file control can use the returned ID directly.
type variableFileResponse struct {
	Kind         file.ItemKind   `json:"kind"`
	ID           int64           `json:"id"`
	FolderID     *file.FolderID  `json:"folder_id"`
	SourceFileID *file.ID        `json:"source_file_id"`
	Storage      filesystem.Code `json:"storage"`
	Name         string          `json:"name"`
	MIMEType     *string         `json:"mime_type,omitempty"`
	Size         *int64          `json:"size,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

func (h *mailHTTP) uploadVariableFile(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	deadline := time.Now().Add(mailUploadTimeout)
	controller := http.NewResponseController(response)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
	maxUploadSize := service.limits.MaxAttachmentSize
	request.Body = http.MaxBytesReader(response, request.Body, maxUploadSize+(1<<20))
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			httptransport.WriteJSONError(response, http.StatusRequestEntityTooLarge, "file_too_large", "uploaded file is too large")
			return
		}
		writeMailError(response, fmt.Errorf("%w: multipart request is invalid", ErrInvalid))
		return
	}
	defer request.MultipartForm.RemoveAll()
	if len(request.MultipartForm.Value) != 1 || len(request.MultipartForm.Value["target"]) != 1 || len(request.MultipartForm.File) != 1 || len(request.MultipartForm.File["file"]) != 1 {
		writeMailError(response, fmt.Errorf("%w: target and file are required", ErrInvalid))
		return
	}
	var target variableFileUploadTarget
	decoder := json.NewDecoder(bytes.NewBufferString(request.FormValue("target")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		writeMailError(response, fmt.Errorf("%w: upload target is invalid", ErrInvalid))
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeMailError(response, fmt.Errorf("%w: upload target contains trailing data", ErrInvalid))
		return
	}
	uploaded, header, err := request.FormFile("file")
	if err != nil {
		writeMailError(response, fmt.Errorf("%w: file is required", ErrInvalid))
		return
	}
	defer uploaded.Close()
	if header.Size > maxUploadSize {
		httptransport.WriteJSONError(response, http.StatusRequestEntityTooLarge, "file_too_large", "uploaded file is too large")
		return
	}
	id, err := pathID[TemplateID](request, "templateID")
	if err != nil {
		writeMailError(response, err)
		return
	}
	item, err := service.UploadVariableFile(request.Context(), actor, id, target.FieldPath, header.Filename, io.LimitReader(uploaded, maxUploadSize+1))
	if err != nil {
		writeMailError(response, err)
		return
	}
	mimeType, size := item.MIMEType, item.Size
	writeJSON(response, http.StatusCreated, variableFileResponse{
		Kind: file.ItemFile, ID: int64(item.ID), FolderID: item.FolderID, SourceFileID: item.ParentID,
		Storage: item.Storage, Name: item.Name, MIMEType: &mimeType, Size: &size,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	})
}
