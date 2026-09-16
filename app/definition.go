package app

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/outbox"
	"github.com/vernal96/go-cms-kernel/seeds"
)

func validateDefinition(definition Definition) error {
	if definition.Logger == nil {
		return errors.New("logger factory is nil")
	}
	if definition.EventBus == nil {
		return errors.New("event bus factory is nil")
	}
	if definition.PasswordHasher == nil {
		return errors.New("password hasher factory is nil")
	}
	if definition.MaxUploadSize < 0 {
		return errors.New("maximum upload size is invalid")
	}
	if definition.UploadTimeout < 0 {
		return errors.New("upload timeout is invalid")
	}
	if definition.AvatarMaxSize < 0 {
		return errors.New("avatar maximum size is invalid")
	}
	if _, err := outbox.NormalizePublisherConfig(definition.OutboxPublisher); err != nil {
		return err
	}
	moduleApplications := make(map[kernel.ModuleCode]struct{}, len(definition.ModuleApplications))
	for index, application := range definition.ModuleApplications {
		if application == nil || nilInterface(application) {
			return fmt.Errorf("module application at index %d is nil", index)
		}
		moduleCode := application.ModuleCode()
		if moduleCode == "" {
			return fmt.Errorf("module application at index %d has empty module code", index)
		}
		if _, exists := moduleApplications[moduleCode]; exists {
			return fmt.Errorf("module application %q is defined more than once", moduleCode)
		}
		moduleApplications[moduleCode] = struct{}{}
	}

	filesystemCodes := make(
		map[filesystem.Code]struct{},
		len(definition.Filesystems),
	)
	for index, factory := range definition.Filesystems {
		if factory == nil {
			return fmt.Errorf(
				"filesystem factory at index %d is nil",
				index,
			)
		}
		code := factory.Code()
		if code == "" {
			return fmt.Errorf(
				"filesystem factory at index %d has empty code",
				index,
			)
		}
		if _, exists := filesystemCodes[code]; exists {
			return fmt.Errorf(
				"filesystem disk %q is defined more than once",
				code,
			)
		}
		filesystemCodes[code] = struct{}{}
	}
	if definition.AvatarStorage != "" {
		if _, exists := filesystemCodes[definition.AvatarStorage]; !exists {
			return fmt.Errorf("avatar filesystem disk %q is unavailable", definition.AvatarStorage)
		}
	}

	cacheCodes := make(
		map[cache.Code]struct{},
		len(definition.Caches),
	)
	for index, factory := range definition.Caches {
		if factory == nil {
			return fmt.Errorf(
				"cache factory at index %d is nil",
				index,
			)
		}
		code := factory.Code()
		if code == "" {
			return fmt.Errorf(
				"cache factory at index %d has empty code",
				index,
			)
		}
		if _, exists := cacheCodes[code]; exists {
			return fmt.Errorf(
				"cache store %q is defined more than once",
				code,
			)
		}
		cacheCodes[code] = struct{}{}
	}

	definitions := make(
		[]DatabaseDefinition,
		0,
		len(definition.AdditionalDatabases)+1,
	)
	definitions = append(definitions, definition.MainDatabase)
	definitions = append(definitions, definition.AdditionalDatabases...)

	connectionCodes := make(map[kernel.ConnectionCode]struct{}, len(definitions))
	for bindingIndex, database := range definitions {
		if database.Connector == nil {
			return fmt.Errorf(
				"database definition at index %d has nil connector factory",
				bindingIndex,
			)
		}

		connectionCode := database.Connector.Code()
		if connectionCode == "" {
			return fmt.Errorf(
				"database definition at index %d has empty connection code",
				bindingIndex,
			)
		}
		if _, exists := connectionCodes[connectionCode]; exists {
			return fmt.Errorf(
				"database connection %q is defined more than once",
				connectionCode,
			)
		}
		connectionCodes[connectionCode] = struct{}{}

		moduleCodes := make(map[kernel.ModuleCode]struct{}, len(database.Adapters))
		for adapterIndex, adapter := range database.Adapters {
			if adapter == nil {
				return fmt.Errorf(
					"database definition %q adapter at index %d is nil",
					connectionCode,
					adapterIndex,
				)
			}

			moduleCode := adapter.ModuleCode()
			if moduleCode == "" {
				return fmt.Errorf(
					"database definition %q adapter at index %d has empty module code",
					connectionCode,
					adapterIndex,
				)
			}
			if _, exists := moduleCodes[moduleCode]; exists {
				return fmt.Errorf(
					"database definition %q contains duplicate module %q",
					connectionCode,
					moduleCode,
				)
			}
			moduleCodes[moduleCode] = struct{}{}
		}
	}

	for _, database := range definitions {
		for _, source := range database.Seeds {
			found := false
			for _, adapter := range database.Adapters {
				if adapter.ModuleCode() == source.Module {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("database %q project seed %q references unavailable module %q", database.Connector.Code(), source.Source.ID, source.Module)
			}
			if err := seeds.ValidateSource(source.Source); err != nil {
				return err
			}
		}
	}

	profileCodes := make(map[kernel.ProfileCode]struct{}, len(definition.Profiles))
	for profileIndex, profile := range definition.Profiles {
		if profile.Code == "" {
			return fmt.Errorf("profile at index %d has empty code", profileIndex)
		}
		if _, exists := profileCodes[profile.Code]; exists {
			return fmt.Errorf("profile %q is defined more than once", profile.Code)
		}
		profileCodes[profile.Code] = struct{}{}

		if err := validateApplicationProfile(profile); err != nil {
			return err
		}
	}

	return nil
}

func cloneDefinition(definition Definition) Definition {
	definition.ModuleApplications = append(
		[]kernel.ModuleApplication(nil),
		definition.ModuleApplications...,
	)
	definition.Filesystems = append(
		[]filesystem.Factory(nil),
		definition.Filesystems...,
	)
	definition.Caches = append(
		[]cache.Factory(nil),
		definition.Caches...,
	)
	definition.MainDatabase.Seeds = cloneModuleSeeds(definition.MainDatabase.Seeds)
	definition.MainDatabase.Adapters = append(
		[]ModuleDatabaseFactory(nil),
		definition.MainDatabase.Adapters...,
	)
	definition.AdditionalDatabases = append(
		[]DatabaseDefinition(nil),
		definition.AdditionalDatabases...,
	)
	for index := range definition.AdditionalDatabases {
		definition.AdditionalDatabases[index].Seeds = cloneModuleSeeds(definition.AdditionalDatabases[index].Seeds)
		definition.AdditionalDatabases[index].Adapters = append(
			[]ModuleDatabaseFactory(nil),
			definition.AdditionalDatabases[index].Adapters...,
		)
	}

	definition.Profiles = append([]kernel.Profile(nil), definition.Profiles...)
	for index := range definition.Profiles {
		definition.Profiles[index].Modules = append(
			[]kernel.ProfileModule(nil),
			definition.Profiles[index].Modules...,
		)
		for moduleIndex := range definition.Profiles[index].Modules {
			definition.Profiles[index].Modules[moduleIndex].Caches = append(
				[]cache.Binding(nil),
				definition.Profiles[index].Modules[moduleIndex].Caches...,
			)
			definition.Profiles[index].Modules[moduleIndex].Filesystems = append(
				[]filesystem.Binding(nil),
				definition.Profiles[index].Modules[moduleIndex].Filesystems...,
			)
		}
		definition.Profiles[index].Params = field.CloneDefinitions(
			definition.Profiles[index].Params,
		)
		definition.Profiles[index].Templates = template.CloneDefinitions(
			definition.Profiles[index].Templates,
		)
	}

	return definition
}

func nilInterface(value any) bool {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func cloneModuleSeeds(sources []ModuleSeedSource) []ModuleSeedSource {
	result := append([]ModuleSeedSource(nil), sources...)
	for i := range result {
		result[i].Source.Tags = append([]seeds.Tag(nil), result[i].Source.Tags...)
	}
	return result
}
