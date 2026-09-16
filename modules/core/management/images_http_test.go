package management

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type thumbnailFiles struct {
	file.Service
	deny bool
}

func (f *thumbnailFiles) GetFile(_ context.Context, _ security.Actor, id file.ID) (file.File, error) {
	if f.deny {
		return file.File{}, security.ErrForbidden
	}
	return file.File{ID: id, MIMEType: "image/png", Storage: "private", ChecksumSHA256: "content"}, nil
}
func (f *thumbnailFiles) Open(context.Context, security.Actor, file.ID) (file.OpenedFile, error) {
	return file.OpenedFile{Body: io.NopCloser(bytes.NewBufferString("source"))}, nil
}

type httpProcessor struct{}

func (httpProcessor) Transform(context.Context, io.Reader, image.TransformOptions) (image.Result, error) {
	return image.Result{Bytes: []byte("png"), MIMEType: "image/png", Width: 128, Height: 128}, nil
}
func TestThumbnailHTTPHeadersValidationAndPrivateAccess(t *testing.T) {
	files := &thumbnailFiles{}
	thumbs := image.NewThumbnails(files, httpProcessor{}, nil, image.DefaultLimits())
	h := &filesHTTP{files: &Files{thumbnails: map[string]*image.Thumbnails{"dev": thumbs}}}
	router := chi.NewRouter()
	router.Get("/files/{fileID}/thumbnail", h.thumbnail)
	handler := httptransport.RequireAuthenticated(router)
	call := func(query, etag string, actor security.Actor) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/files/1/thumbnail"+query, nil)
		req = req.WithContext(httptransport.WithActor(req.Context(), actor))
		req.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	first := call("?width=128&height=128&fit=contain", "", security.User(1))
	if first.Code != 200 || first.Header().Get("Content-Type") != "image/png" || first.Header().Get("X-Content-Type-Options") != "nosniff" || first.Header().Get("ETag") == "" || first.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("response %d %v", first.Code, first.Header())
	}
	next := call("?fit=contain&height=128&width=128", first.Header().Get("ETag"), security.User(1))
	if next.Code != 304 || next.Body.Len() != 0 {
		t.Fatal("conditional response", next.Code)
	}
	files.deny = true
	if denied := call("", first.Header().Get("ETag"), security.User(1)); denied.Code != 403 {
		t.Fatal("private conditional bypass", denied.Code)
	}
	files.deny = false
	if guest := call("", "", security.Guest()); guest.Code != 401 {
		t.Fatal("guest response", guest.Code)
	}
	for _, q := range []string{"?width=999999", "?width=127", "?fit=wrong", "?crop=10", "?width=128&width=256"} {
		if response := call(q, "", security.User(1)); response.Code < 400 || response.Code >= 500 {
			t.Fatalf("query %s: %d", q, response.Code)
		}
	}
}
