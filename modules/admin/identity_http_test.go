package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
)

func TestUserDeleteRouteDoesNotExist(t *testing.T) {
	t.Parallel()
	router := chi.NewRouter()
	registerManagementRoutes(router, &Management{})
	request := httptest.NewRequest(http.MethodDelete, "/users/1", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /users/1 status = %d", response.Code)
	}
}

func TestIdentityProtectionErrorsAreConflicts(t *testing.T) {
	t.Parallel()
	for _, err := range []error{group.ErrProtected, group.ErrLastAdministrator, user.ErrSelfBlock, user.ErrLastAdministrator} {
		response := httptest.NewRecorder()
		writeManagementError(response, err)
		if response.Code != http.StatusConflict {
			t.Fatalf("error %v status = %d", err, response.Code)
		}
	}
}
