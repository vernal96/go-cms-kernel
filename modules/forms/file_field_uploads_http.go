package forms

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

func (h *formsHTTP) uploadFieldFile(response http.ResponseWriter, request *http.Request) {
	actor, ok := h.actor(response, request)
	if !ok {
		return
	}
	formID, err := pathID[FormID](request, "formID")
	if err != nil {
		writeManagementError(response, err)
		return
	}
	limit := h.service.limits.MaxUploadFileSize
	controller := http.NewResponseController(response)
	deadline := time.Now().Add(h.service.limits.SubmissionTimeout)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
	request.Body = http.MaxBytesReader(response, request.Body, limit+(1<<20))
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			httptransport.WriteJSONError(response, http.StatusRequestEntityTooLarge, "file_too_large", "uploaded file is too large")
		} else {
			httptransport.WriteJSONError(response, http.StatusBadRequest, "invalid_request", "multipart request is invalid")
		}
		return
	}
	defer request.MultipartForm.RemoveAll()
	if len(request.MultipartForm.Value) != 1 || len(request.MultipartForm.Value["target"]) != 1 || len(request.MultipartForm.File) != 1 || len(request.MultipartForm.File["file"]) != 1 {
		httptransport.WriteJSONError(response, http.StatusBadRequest, "invalid_request", "target and file are required")
		return
	}
	var target FileFieldTarget
	decoder := json.NewDecoder(bytes.NewBufferString(request.FormValue("target")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		httptransport.WriteJSONError(response, http.StatusBadRequest, "invalid_request", "target is invalid")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		httptransport.WriteJSONError(response, http.StatusBadRequest, "invalid_request", "target contains trailing data")
		return
	}
	content, header, err := request.FormFile("file")
	if err != nil {
		httptransport.WriteJSONError(response, http.StatusBadRequest, "invalid_request", "file is required")
		return
	}
	defer content.Close()
	if header.Size > limit {
		httptransport.WriteJSONError(response, http.StatusRequestEntityTooLarge, "file_too_large", "uploaded file is too large")
		return
	}
	item, err := h.service.UploadFieldFile(request.Context(), actor, formID, target, header.Filename, io.LimitReader(content, limit+1))
	if err != nil {
		writeManagementError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{
		"kind": "file", "id": item.ID, "folder_id": item.FolderID, "source_file_id": item.ParentID,
		"storage": item.Storage, "name": item.Name, "mime_type": item.MIMEType, "size": item.Size,
		"created_at": item.CreatedAt, "updated_at": item.UpdatedAt,
	})
}
