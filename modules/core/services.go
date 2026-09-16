package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
)

// Services is the application-scoped domain runtime owned by cms.core.
// Site-specific module runtimes may reference these concurrency-safe services,
// while their registries, module instances, and bindings remain site-scoped.
type Services struct {
	Sites         *site.Catalog
	Resources     *resource.Service
	Revisions     *resource.RevisionService
	LibraryItems  *resource.LibraryService
	Files         file.ManagementService
	Media         media.Service
	Images        *media.ImageService
	Users         user.Service
	Groups        group.Service
	Authorization access.Service

	database    Database
	revisions   resource.RevisionRepository
	cachePolicy *repositoryCachePolicy
	hooks       *entityhooks.Registry
}

// NewServices assembles the site-independent part of the core domain. Site
// and resource services are completed after profile blueprints are available.
func NewServices(
	database Database,
	permissions *permission.Catalog,
	filesystems filesystem.Catalog,
	passwordHashers user.PasswordHasherFactory,
	cacheInvalidator cache.Invalidator,
	hooks *entityhooks.Registry,
) (*Services, error) {
	if hooks == nil {
		hooks = entityhooks.EmptyRegistry(entityhooks.Application, "")
	}
	coherent, err := newCoherentDatabase(database, cacheInvalidator)
	if err != nil {
		return nil, err
	}
	database = coherent
	revisionRepository, ok := database.Resources().(resource.RevisionRepository)
	if !ok {
		return nil, errors.New("core resource revision repository is unavailable")
	}
	if permissions == nil {
		return nil, errors.New("core permission catalog is nil")
	}
	if filesystems == nil {
		return nil, errors.New("core filesystem catalog is nil")
	}
	if passwordHashers == nil {
		return nil, errors.New("core password hasher factory is nil")
	}

	authorization, err := access.NewService(
		database.Access(),
		permissions,
	)
	if err != nil {
		return nil, err
	}
	files, err := file.NewService(
		database.Files(),
		filesystems,
		authorization,
	)
	if err != nil {
		return nil, err
	}
	mediaService, err := media.NewService(
		database.Media(),
		files,
		media.FilePolicies{
			resource.ImageMediaUsage: resource.ValidateImageMediaFile,
			user.AvatarMediaUsage:    user.ValidateAvatarMediaFile,
		},
		authorization,
	)
	if err != nil {
		return nil, err
	}
	groups, err := group.NewService(database.Groups(), authorization, hooks)
	if err != nil {
		return nil, err
	}
	passwordHasher, err := passwordHashers.Open()
	if err != nil {
		return nil, fmt.Errorf("open core password hasher: %w", err)
	}
	if passwordHasher == nil {
		return nil, errors.New("core password hasher factory returned nil")
	}
	users, err := user.NewService(
		database.Users(),
		passwordHasher,
		mediaService,
		groups,
		authorization,
		hooks,
	)
	if err != nil {
		return nil, err
	}

	return &Services{
		Files:         files,
		Media:         mediaService,
		Users:         users,
		Groups:        groups,
		Authorization: authorization,
		database:      database,
		revisions:     revisionRepository,
		cachePolicy:   coherent.policy,
		hooks:         hooks,
	}, nil
}

// Database returns the cache-coherent core persistence boundary used by all
// application services and site runtimes.
func (s *Services) Database() Database {
	if s == nil {
		return nil
	}
	return s.database
}

// BuildContent completes the core runtime once profile definitions can build
// final site-scoped runtimes. It loads all sites before publishing the catalog.
func (s *Services) BuildContent(
	ctx context.Context,
	profiles site.ProfileResolver,
) error {
	if s == nil {
		return errors.New("core services are nil")
	}
	if s.Sites != nil || s.Resources != nil {
		return errors.New("core content services are already built")
	}

	catalog, err := site.NewCatalog(
		s.database.Sites(),
		profiles,
		s.Authorization,
		s.Files,
	)
	if err != nil {
		return err
	}
	if err := catalog.Reload(ctx); err != nil {
		return fmt.Errorf("compile site runtimes: %w", err)
	}
	resources, err := resource.NewService(
		s.database.Resources(),
		catalog,
		s.Media,
		s.Authorization,
		s.Files,
	)
	if err != nil {
		return err
	}
	libraryRepository, ok := s.database.Resources().(resource.LibraryItemRepository)
	if !ok {
		return errors.New("core library item repository is unavailable")
	}
	libraryItems, err := resource.NewLibraryService(libraryRepository, resources)
	if err != nil {
		return err
	}
	revisions, err := resource.NewRevisionService(s.revisions, resources, libraryItems, s.Authorization)
	if err != nil {
		return err
	}

	cascade, ok := s.Files.(file.CascadeService)
	if !ok {
		return errors.New("core file cascade service is unavailable")
	}
	s.Files = &cascadeFiles{ManagementService: s.Files, cascade: cascade, resources: resources, hooks: s.hooks}
	s.Sites = catalog
	s.Resources = resources
	s.Revisions = revisions
	s.LibraryItems = libraryItems
	return nil
}

func validateDatabase(database Database) error {
	if database == nil {
		return errors.New("core database is nil")
	}
	repositories := []struct {
		name  string
		value any
	}{
		{name: "site", value: database.Sites()},
		{name: "resource", value: database.Resources()},
		{name: "file", value: database.Files()},
		{name: "media", value: database.Media()},
		{name: "user", value: database.Users()},
		{name: "group", value: database.Groups()},
		{name: "access", value: database.Access()},
	}
	for _, repository := range repositories {
		if repository.value == nil {
			return fmt.Errorf("core %s repository is nil", repository.name)
		}
	}
	return nil
}
