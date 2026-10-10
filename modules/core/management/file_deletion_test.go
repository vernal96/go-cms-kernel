package management

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type deletionHTTPService struct {
	result media.FileDeletion
	err    error
	input  media.DeleteFileInput
	calls  int
}

func (s *deletionHTTPService) Delete(_ context.Context, _ security.Actor, input media.DeleteFileInput) (media.FileDeletion, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}
func (s *deletionHTTPService) Status(context.Context, security.Actor, int64, string) (media.FileDeletion, error) {
	s.calls++
	return s.result, s.err
}

type deletionHTTPModule struct {
	settingsHTTPModule
	deletions media.FileDeletionService
}

func (m deletionHTTPModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return m, nil
}
func (m deletionHTTPModule) MediaFileDeletions() media.FileDeletionService { return m.deletions }

func TestMediaFileDeletionHTTPStatusesConflictsAndSiteAuthorization(t *testing.T) {
	ctx := context.Background()
	service := &deletionHTTPService{}
	factory, err := kernel.NewProfileRuntimeFactory(extensionTestDatabaseResolver{}, kernel.RuntimeServices{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), EventBus: extensionTestBus{}})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "test", Modules: []kernel.Module{deletionHTTPModule{deletions: service}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := site.NewRuntimeFromBlueprint(ctx, site.Site{ID: 7, ProfileCode: "test", Name: "Site", Domain: "delete.test", Locale: "ru-RU", Settings: map[string]any{}}, blueprint)
	if err != nil {
		t.Fatal(err)
	}
	denied := map[permission.Code]error{}
	sites := &Sites{authorization: authorization{sites: extensionTestSites{runtime: runtime}, authorizer: managementAuthorizer{denied: denied}, policy: extensionTestPolicy{}}}
	router := chi.NewRouter()
	registerContentRoutes(router, sites, nil)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	raw, _ := json.Marshal(map[string]any{"expected_file_id": 123, "expected_updated_at": now})
	for _, test := range []struct {
		status string
		err    error
		want   int
		code   string
	}{{"completed", nil, 200, ""}, {"pending", nil, 202, ""}, {"", media.ErrFileInUse, 409, "media_file_in_use"}, {"", media.ErrFileDeleteConflict, 409, "media_file_delete_conflict"}, {"", security.ErrForbidden, 403, "forbidden"}} {
		service.result = media.FileDeletion{OperationID: "op", Status: test.status, MediaID: 5, DeletedFileIDs: []file.ID{123}, StatusURL: "/api/sites/7/media-file-deletions/op"}
		service.err = test.err
		response := call("POST", "/sites/7/media/5/delete-file", string(raw))
		if response.Code != test.want || test.code != "" && !strings.Contains(response.Body.String(), test.code) {
			t.Fatalf("status=%s got=%d %s", test.status, response.Code, response.Body.String())
		}
		if service.input.SiteID != 7 || service.input.MediaID != 5 || service.input.ExpectedFileID != 123 || !service.input.ExpectedUpdatedAt.Equal(now) {
			t.Fatal(service.input)
		}
	}
	service.err = nil
	service.result.Status = "completed"
	if response := call("GET", "/sites/7/media-file-deletions/op", ""); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	before := service.calls
	denied[SiteReadPermission] = security.ErrForbidden
	if response := call("POST", "/sites/7/media/5/delete-file", string(raw)); response.Code != 403 || service.calls != before {
		t.Fatal("forbidden site reached deletion", response.Code)
	}
	delete(denied, SiteReadPermission)
	if response := call("POST", "/sites/7/media/5/delete-file", "{}"); response.Code != 422 || service.calls != before {
		t.Fatal("missing precondition reached deletion", response.Code)
	}
}
