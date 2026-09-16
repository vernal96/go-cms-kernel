package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type sessionResponse struct {
	User        sessionUser       `json:"user"`
	Permissions []permission.Code `json:"permissions"`
}

type sessionUser struct {
	ID              user.ID          `json:"id"`
	Login           string           `json:"login"`
	Email           string           `json:"email"`
	DisplayName     string           `json:"display_name"`
	ColorScheme     user.ColorScheme `json:"color_scheme"`
	AccentColor     user.AccentColor `json:"accent_color"`
	HasAvatar       bool             `json:"has_avatar"`
	AvatarUpdatedAt *time.Time       `json:"avatar_updated_at"`
}

// SessionHandler exposes the site-independent admin session endpoint.
func (r *Runtime) SessionHandler() (http.Handler, error) {
	if err := r.validateHTTP(); err != nil {
		return nil, err
	}
	return httptransport.RequireAuthenticated(
		r.requireAccess(http.HandlerFunc(r.serveSession)),
	), nil
}

func (r *Runtime) AdminHandler(management *Management) (http.Handler, error) {
	if err := r.validateHTTP(); err != nil {
		return nil, err
	}
	if management == nil {
		return nil, errors.New("admin management is nil")
	}
	router := chi.NewRouter()
	router.Get("/session", r.serveSession)
	registerManagementRoutes(router, management)
	return httptransport.RequireAuthenticated(r.requireAccess(router)), nil
}

func (r *Runtime) validateHTTP() error {
	if r == nil {
		return errors.New("admin runtime is nil")
	}
	if r.users == nil {
		return errors.New("admin user service is nil")
	}
	if r.authorization == nil {
		return errors.New("admin authorizer is nil")
	}
	return nil
}

func (r *Runtime) requireAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		actor, exists := httptransport.ActorFromContext(request.Context())
		if !exists {
			httptransport.WriteJSONError(
				response,
				http.StatusInternalServerError,
				"internal_error",
				"request authentication is unavailable",
			)
			return
		}

		err := r.authorization.Check(
			request.Context(),
			actor,
			AccessPermission,
		)
		switch {
		case err == nil:
			next.ServeHTTP(response, request)
		case errors.Is(err, security.ErrUnauthenticated):
			writeUnauthorized(response)
		case errors.Is(err, security.ErrForbidden):
			httptransport.WriteJSONError(
				response,
				http.StatusForbidden,
				"forbidden",
				"admin access is forbidden",
			)
		default:
			httptransport.WriteJSONError(
				response,
				http.StatusInternalServerError,
				"internal_error",
				"admin authorization failed",
			)
		}
	})
}

func (r *Runtime) serveSession(
	response http.ResponseWriter,
	request *http.Request,
) {
	actor, exists := httptransport.ActorFromContext(request.Context())
	if !exists {
		httptransport.WriteJSONError(
			response,
			http.StatusInternalServerError,
			"internal_error",
			"request authentication is unavailable",
		)
		return
	}

	current, err := r.users.Current(request.Context(), actor)
	if err != nil {
		if errors.Is(err, security.ErrUnauthenticated) ||
			errors.Is(err, user.ErrNotFound) {
			writeUnauthorized(response)
			return
		}
		httptransport.WriteJSONError(
			response,
			http.StatusInternalServerError,
			"internal_error",
			"current user lookup failed",
		)
		return
	}
	permissionCodes := AdminPermissionCodes
	if catalog, ok := r.authorization.(interface{ Codes() []permission.Code }); ok {
		permissionCodes = catalog.Codes()
	}
	permissions := make([]permission.Code, 0, len(permissionCodes))
	for _, code := range permissionCodes {
		err := r.authorization.Check(request.Context(), actor, code)
		if err == nil {
			permissions = append(permissions, code)
			continue
		}
		if errors.Is(err, security.ErrForbidden) {
			continue
		}
		if errors.Is(err, security.ErrUnauthenticated) {
			writeUnauthorized(response)
			return
		}
		httptransport.WriteJSONError(
			response,
			http.StatusInternalServerError,
			"internal_error",
			"permission lookup failed",
		)
		return
	}

	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	var avatarUpdatedAt *time.Time
	if current.AvatarMediaID != nil {
		value := current.UpdatedAt
		avatarUpdatedAt = &value
	}
	_ = json.NewEncoder(response).Encode(sessionResponse{
		User: sessionUser{
			ID:              current.ID,
			Login:           current.Login,
			Email:           current.Email,
			DisplayName:     displayName(current),
			ColorScheme:     current.ColorScheme,
			AccentColor:     current.AccentColor,
			HasAvatar:       current.AvatarMediaID != nil,
			AvatarUpdatedAt: avatarUpdatedAt,
		},
		Permissions: permissions,
	})
}

func displayName(current user.User) string {
	parts := make([]string, 0, 3)
	if current.LastName != nil {
		parts = append(parts, *current.LastName)
	}
	if current.Name != "" {
		parts = append(parts, current.Name)
	}
	if current.MiddleName != nil {
		parts = append(parts, *current.MiddleName)
	}
	if len(parts) == 0 {
		return current.Login
	}
	return strings.Join(parts, " ")
}

func writeUnauthorized(response http.ResponseWriter) {
	response.Header().Set("WWW-Authenticate", "Bearer")
	httptransport.WriteJSONError(
		response,
		http.StatusUnauthorized,
		"unauthenticated",
		"authentication required",
	)
}
