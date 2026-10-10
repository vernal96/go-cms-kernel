package management

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type HTTPDependencies struct {
	Sites         *Sites
	Resources     *Resources
	Files         *Files
	MaxUploadSize int64
	UploadTimeout time.Duration
}

// HTTPRoutePrefixes returns the API-relative namespaces owned by Core management.
// The server uses these boundaries to separate authenticated management requests
// from public profile routes, including unknown paths inside these namespaces.
func HTTPRoutePrefixes() []string {
	return []string{"/sites", "/site-profiles", "/files", "/media", "/administration"}
}

// NewHTTPHandler builds the site-independent CMS management API. Authentication
// is transport-owned; global and site-scoped authorization remains in the
// domain management services.
func NewHTTPHandler(dependencies HTTPDependencies) (http.Handler, error) {
	if dependencies.Sites == nil || dependencies.Resources == nil {
		return nil, errors.New("CMS site/resource management is nil")
	}
	if dependencies.Files == nil {
		return nil, errors.New("CMS file management is nil")
	}
	if dependencies.MaxUploadSize <= 0 {
		dependencies.MaxUploadSize = 100 << 20
	}
	if dependencies.UploadTimeout <= 0 {
		dependencies.UploadTimeout = 10 * time.Minute
	}
	router := chi.NewRouter()
	registerContentRoutes(router, dependencies.Sites, dependencies.Resources)
	registerFileRoutes(router, &filesHTTP{
		files: dependencies.Files, sites: dependencies.Sites, resources: dependencies.Resources, maxUploadSize: dependencies.MaxUploadSize,
		uploadTimeout: dependencies.UploadTimeout,
	})
	return httptransport.RequireAuthenticated(router), nil
}
