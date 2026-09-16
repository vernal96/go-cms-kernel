package filesystem

import (
	"fmt"
)

type scopedManager struct {
	disks    map[Alias]Disk
	bindings map[Alias]Binding
}

func NewModuleManager(
	resolver Resolver,
	bindings []Binding,
) (ModuleManager, error) {
	manager := &scopedManager{
		disks:    make(map[Alias]Disk, len(bindings)),
		bindings: make(map[Alias]Binding, len(bindings)),
	}

	for index, binding := range bindings {
		if binding.Alias == "" {
			return nil, fmt.Errorf(
				"filesystem binding at index %d has empty alias",
				index,
			)
		}
		if binding.Code == "" {
			return nil, fmt.Errorf(
				"filesystem binding %q has empty disk code",
				binding.Alias,
			)
		}
		if _, exists := manager.disks[binding.Alias]; exists {
			return nil, fmt.Errorf(
				"filesystem alias %q is configured more than once",
				binding.Alias,
			)
		}
		if resolver == nil {
			return nil, fmt.Errorf(
				"resolve filesystem alias %q: %w: %q",
				binding.Alias,
				ErrDiskNotFound,
				binding.Code,
			)
		}

		disk, exists := resolver.Disk(binding.Code)
		if !exists || isNilDisk(disk) {
			return nil, fmt.Errorf(
				"resolve filesystem alias %q: %w: %q",
				binding.Alias,
				ErrDiskNotFound,
				binding.Code,
			)
		}
		if disk.Code() != binding.Code {
			return nil, fmt.Errorf(
				"filesystem resolver returned disk %q for code %q",
				disk.Code(),
				binding.Code,
			)
		}
		if !ValidVisibility(disk.Visibility()) {
			return nil, fmt.Errorf(
				"filesystem disk %q: %w: %q",
				binding.Code,
				ErrInvalidVisibility,
				disk.Visibility(),
			)
		}

		manager.bindings[binding.Alias] = binding
		manager.disks[binding.Alias] = disk
	}

	return manager, nil
}

func (m *scopedManager) Disk(alias Alias) (Disk, bool) {
	if m == nil {
		return nil, false
	}
	disk, exists := m.disks[alias]
	return disk, exists
}

func (m *scopedManager) Binding(alias Alias) (Binding, bool) {
	if m == nil {
		return Binding{}, false
	}
	binding, exists := m.bindings[alias]
	return binding, exists
}

var _ ModuleManager = (*scopedManager)(nil)
