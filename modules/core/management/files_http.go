package management

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type filesHTTP struct {
	files         *Files
	maxUploadSize int64
	uploadTimeout time.Duration
}

func registerFileRoutes(router chi.Router, handler *filesHTTP) {
	if handler.files.images != nil {
		registerImageRoutes(router, handler)
	}
	router.Get("/files/disks", handler.filesystemDisks)
	router.Get("/files/items", handler.filesystemItems)
	router.Get("/files/folders/resolve", handler.resolveFilesystemFolder)
	router.Post("/files/folders/ensure", handler.ensureFilesystemFolder)
	router.Post("/files/folders", handler.createFilesystemFolder)
	router.Patch("/files/folders/{folderID}", handler.renameFilesystemFolder)
	router.Post("/files/uploads", handler.uploadFilesystemFile)
	router.Get("/files/{fileID}", handler.getFilesystemFile)
	router.Patch("/files/{fileID}", handler.renameFilesystemFile)
	router.Get("/files/{fileID}/preview", handler.previewFilesystemFile)
	router.Get("/files/{fileID}/download", handler.downloadFilesystemFile)
	router.Post("/files/move", handler.moveFilesystemItems)
	router.Post("/files/delete-impact", handler.deleteImpact)
	router.Post("/files/delete", handler.deleteFilesystemItems)
}

func (h *filesHTTP) resolveFilesystemFolder(response http.ResponseWriter, request *http.Request) {
	storage := filesystem.Code(request.URL.Query().Get("disk"))
	folderPath := request.URL.Query().Get("path")
	if storage == "" || folderPath == "" {
		writeBadRequest(response, "disk and path are required")
		return
	}
	result, err := h.files.ResolveFilesystemFolder(request.Context(), actor(request), storage, folderPath)
	writeResult(response, http.StatusOK, result, err)
}

type filesystemEnsureFolderRequest struct {
	Disk filesystem.Code `json:"disk"`
	Path string          `json:"path"`
}

func (h *filesHTTP) ensureFilesystemFolder(response http.ResponseWriter, request *http.Request) {
	var payload filesystemEnsureFolderRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if payload.Disk == "" || payload.Path == "" {
		writeBadRequest(response, "disk and path are required")
		return
	}
	result, err := h.files.EnsureFilesystemFolder(request.Context(), actor(request), payload.Disk, payload.Path)
	writeResult(response, http.StatusOK, result, err)
}

func (h *filesHTTP) filesystemDisks(response http.ResponseWriter, request *http.Request) {
	result, err := h.files.FilesystemDisks(request.Context(), actor(request))
	writeResult(response, http.StatusOK, result, err)
}

func (h *filesHTTP) filesystemItems(response http.ResponseWriter, request *http.Request) {
	storage := filesystem.Code(request.URL.Query().Get("disk"))
	if storage == "" {
		writeBadRequest(response, "disk is required")
		return
	}
	folderID, ok := optionalFolderID(response, request.URL.Query().Get("folder_id"))
	if !ok {
		return
	}
	result, err := h.files.BrowseFilesystem(request.Context(), actor(request), storage, folderID)
	writeResult(response, http.StatusOK, result, err)
}

type filesystemFolderRequest struct {
	Disk     filesystem.Code `json:"disk"`
	ParentID *file.FolderID  `json:"parent_id"`
	Name     string          `json:"name"`
}

func (h *filesHTTP) createFilesystemFolder(response http.ResponseWriter, request *http.Request) {
	var payload filesystemFolderRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.files.CreateFilesystemFolder(request.Context(), actor(request), file.CreateFolderInput{
		Storage: payload.Disk, ParentID: payload.ParentID, Name: payload.Name,
	})
	writeResult(response, http.StatusCreated, result, err)
}

type filesystemRenameRequest struct {
	Name string `json:"name"`
}

func (h *filesHTTP) renameFilesystemFolder(response http.ResponseWriter, request *http.Request) {
	id, ok := filesystemFolderID(response, request)
	if !ok {
		return
	}
	var payload filesystemRenameRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.files.RenameFilesystemFolder(request.Context(), actor(request), file.RenameFolderInput{ID: id, Name: payload.Name})
	writeResult(response, http.StatusOK, result, err)
}

func (h *filesHTTP) uploadFilesystemFile(response http.ResponseWriter, request *http.Request) {
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
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	storage := filesystem.Code(request.FormValue("disk"))
	folderID, ok := optionalFolderID(response, request.FormValue("folder_id"))
	if !ok {
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
	result, err := h.files.UploadFilesystemFile(request.Context(), actor(request), file.UploadInput{
		Storage: storage, FolderID: folderID, Name: header.Filename,
		Content: io.LimitReader(uploaded, h.maxUploadSize+1),
	})
	writeResult(response, http.StatusCreated, result, err)
}

func (h *filesHTTP) getFilesystemFile(response http.ResponseWriter, request *http.Request) {
	id, ok := filesystemFileID(response, request)
	if !ok {
		return
	}
	result, err := h.files.FilesystemFile(request.Context(), actor(request), id)
	writeResult(response, http.StatusOK, result, err)
}

func (h *filesHTTP) renameFilesystemFile(response http.ResponseWriter, request *http.Request) {
	id, ok := filesystemFileID(response, request)
	if !ok {
		return
	}
	var payload filesystemRenameRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.files.RenameFilesystemFile(request.Context(), actor(request), file.RenameFileInput{ID: id, Name: payload.Name})
	writeResult(response, http.StatusOK, result, err)
}

type filesystemItemRequest struct {
	Kind file.ItemKind `json:"kind"`
	ID   int64         `json:"id"`
}

type filesystemMoveRequest struct {
	Disk     filesystem.Code         `json:"disk"`
	FolderID *file.FolderID          `json:"folder_id"`
	Items    []filesystemItemRequest `json:"items"`
}

func (h *filesHTTP) moveFilesystemItems(response http.ResponseWriter, request *http.Request) {
	var payload filesystemMoveRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	if err := h.files.MoveFilesystemItems(request.Context(), actor(request), file.MoveItemsInput{
		Storage: payload.Disk, FolderID: payload.FolderID, Items: filesystemReferences(payload.Items),
	}); err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

type filesystemDeleteRequest struct {
	Policy      file.DeletePolicy       `json:"policy"`
	ImpactToken string                  `json:"impact_token"`
	Items       []filesystemItemRequest `json:"items"`
}

func (h *filesHTTP) deleteFilesystemItems(response http.ResponseWriter, request *http.Request) {
	var payload filesystemDeleteRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	var err error
	switch payload.Policy {
	case "", file.DeleteSafe:
		err = h.files.DeleteFilesystemItems(request.Context(), actor(request), file.DeleteItemsInput{Items: filesystemReferences(payload.Items)})
	case file.DeleteConfirmedMediaCascade:
		err = h.files.DeleteConfirmed(request.Context(), actor(request), filesystemReferences(payload.Items), payload.ImpactToken)
	default:
		writeBadRequest(response, "invalid delete policy")
		return
	}
	if err != nil {
		writeManagementError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *filesHTTP) previewFilesystemFile(response http.ResponseWriter, request *http.Request) {
	h.serveFilesystemContent(response, request, "inline")
}

func (h *filesHTTP) downloadFilesystemFile(response http.ResponseWriter, request *http.Request) {
	h.serveFilesystemContent(response, request, "attachment")
}

func (h *filesHTTP) serveFilesystemContent(response http.ResponseWriter, request *http.Request, disposition string) {
	id, ok := filesystemFileID(response, request)
	if !ok {
		return
	}
	opened, err := h.files.OpenFilesystemFile(request.Context(), actor(request), id)
	if err != nil {
		writeManagementError(response, err)
		return
	}
	defer opened.Body.Close()
	response.Header().Set("Content-Type", opened.File.MIMEType)
	response.Header().Set("Content-Length", strconv.FormatInt(opened.File.Size, 10))
	response.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": opened.File.Name}))
	response.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(response, opened.Body)
}

func filesystemReferences(items []filesystemItemRequest) []file.ItemReference {
	result := make([]file.ItemReference, len(items))
	for index, item := range items {
		result[index] = file.ItemReference{Kind: item.Kind, ID: item.ID}
	}
	return result
}

func filesystemFileID(response http.ResponseWriter, request *http.Request) (file.ID, bool) {
	value, err := strconv.ParseInt(chi.URLParam(request, "fileID"), 10, 64)
	if err != nil || value <= 0 {
		writeBadRequest(response, "file_id is invalid")
		return 0, false
	}
	return file.ID(value), true
}

func filesystemFolderID(response http.ResponseWriter, request *http.Request) (file.FolderID, bool) {
	value, err := strconv.ParseInt(chi.URLParam(request, "folderID"), 10, 64)
	if err != nil || value <= 0 {
		writeBadRequest(response, "folder_id is invalid")
		return 0, false
	}
	return file.FolderID(value), true
}

func optionalFolderID(response http.ResponseWriter, raw string) (*file.FolderID, bool) {
	if raw == "" {
		return nil, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		writeBadRequest(response, "folder_id is invalid")
		return nil, false
	}
	result := file.FolderID(value)
	return &result, true
}

func (h *filesHTTP) deleteImpact(response http.ResponseWriter, request *http.Request) {
	var payload filesystemDeleteRequest
	if !decodeBody(response, request, &payload) {
		return
	}
	result, err := h.files.DeleteImpact(request.Context(), actor(request), filesystemReferences(payload.Items))
	writeResult(response, http.StatusOK, result, err)
}
