package kernel

import (
	"fmt"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

func validateProfileFileDisks(profile Profile, disks filesystem.Resolver) error {
	if err := validateFileFieldDisks(profile.Params, disks); err != nil {
		return fmt.Errorf("profile %q params: %w", profile.Code, err)
	}
	for _, template := range profile.Templates {
		if err := validateFileFieldDisks(template.Fields, disks); err != nil {
			return fmt.Errorf("profile %q template %q: %w", profile.Code, template.Code, err)
		}
	}
	return nil
}

func validateFileFieldDisks(definitions []field.Definition, disks filesystem.Resolver) error {
	for _, definition := range definitions {
		if definition.Type == field.TypeFile {
			options, err := field.FileOptionsValue(definition.Options)
			if err != nil {
				return fmt.Errorf("field %q file options: %w", definition.Key, err)
			}
			if disks != nil {
				if _, exists := disks.Disk(options.Disk); !exists {
					return fmt.Errorf("field %q references unknown disk %q", definition.Key, options.Disk)
				}
			}
		}
		if definition.Type != field.TypeRepeater {
			continue
		}
		options, err := field.DecodeOptions[field.RepeaterOptions](definition.Options)
		if err != nil {
			return fmt.Errorf("field %q repeater options: %w", definition.Key, err)
		}
		if err := validateFileFieldDisks(options.Fields, disks); err != nil {
			return fmt.Errorf("field %q: %w", definition.Key, err)
		}
	}
	return nil
}
