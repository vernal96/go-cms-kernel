package core

import (
	"errors"
	"log/slog"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/cache"
	image "github.com/vernal96/go-cms-kernel/modules/core/image"
	"github.com/vernal96/go-cms-kernel/modules/core/image/adapters/imaging"
	"github.com/vernal96/go-cms-kernel/modules/core/management"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
)

// ConfigureManagementImages builds profile-bound caches once, at boot. This
// global filesystem API uses a profile namespace, not a mutable active site.
func (s *Services) ConfigureManagementImages(files *management.Files, profiles []kernel.Profile, caches cache.Resolver, logger *slog.Logger) error {
	limits := image.DefaultLimits()
	var configured *image.Limits
	for _, profile := range profiles {
		for _, module := range profile.Modules {
			if module.Module.Code() != ModuleCode {
				continue
			}
			cfg, ok := module.Config.(Config)
			if ok && cfg.Images != nil {
				if configured != nil && *configured != *cfg.Images {
					return errors.New("global Media image limits must agree across profiles")
				}
				v := *cfg.Images
				configured = &v
				limits = v
			}
		}
	}
	processor, err := imaging.New(limits)
	if err != nil {
		return err
	}
	editor, err := media.NewImageService(s.database.Media(), s.Files, media.FilePolicies{resource.ImageMediaUsage: resource.ValidateImageMediaFile, user.AvatarMediaUsage: user.ValidateAvatarMediaFile}, s.Authorization, processor, limits, logger)
	if err != nil {
		return err
	}
	thumbnails := map[string]*image.Thumbnails{}
	for _, profile := range profiles {
		for _, module := range profile.Modules {
			if module.Module.Code() != ModuleCode {
				continue
			}
			config, ok := module.Config.(Config)
			if !ok {
				config = Config{}
			}
			l := limits
			if config.Images != nil {
				l = *config.Images
			}
			p, err := imaging.New(l)
			if err != nil {
				return err
			}
			manager, err := cache.NewModuleManager(caches, string(profile.Code), string(ModuleCode), module.Caches)
			if err != nil {
				return err
			}
			store, _ := manager.Store(ThumbnailCacheAlias)
			thumbnails[string(profile.Code)] = image.NewThumbnails(s.Files, p, store, l)
		}
	}
	s.Images = editor
	files.ConfigureImages(editor, thumbnails)
	return nil
}
