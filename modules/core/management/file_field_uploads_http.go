package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

func (h *filesHTTP) uploadFieldFile(response http.ResponseWriter, request *http.Request) {
	deadline := time.Now().Add(h.uploadTimeout)
	controller := http.NewResponseController(response)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
	request.Body = http.MaxBytesReader(response, request.Body, h.maxUploadSize+(1<<20))
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			httptransport.WriteJSONError(response, http.StatusRequestEntityTooLarge, "file_too_large", "uploaded file is too large")
			return
		}
		writeBadRequest(response, "multipart request is invalid")
		return
	}
	defer request.MultipartForm.RemoveAll()
	// Exactly one target and file; reject disk/folder/MIME overrides explicitly.
	if len(request.MultipartForm.Value) != 1 || len(request.MultipartForm.Value["target"]) != 1 || len(request.MultipartForm.File) != 1 || len(request.MultipartForm.File["file"]) != 1 {
		writeBadRequest(response, "target and file are required")
		return
	}
	var target FileFieldTarget
	decoder := json.NewDecoder(bytes.NewBufferString(request.FormValue("target")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		writeBadRequest(response, "target is invalid")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeBadRequest(response, "target contains trailing data")
		return
	}
	uploaded, header, err := request.FormFile("file")
	if err != nil {
		writeBadRequest(response, "file is required")
		return
	}
	defer uploaded.Close()
	if header.Size > h.maxUploadSize {
		httptransport.WriteJSONError(response, http.StatusRequestEntityTooLarge, "file_too_large", "uploaded file is too large")
		return
	}
	result, err := h.files.UploadFieldFile(request.Context(), actor(request), h.sites, h.resources, target, header.Filename, io.LimitReader(uploaded, h.maxUploadSize+1))
	writeResult(response, http.StatusCreated, result, err)
}
