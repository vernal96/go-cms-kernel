package filesystem

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

type Manager struct {
	disks     map[Code]Disk
	order     []Disk
	infos     []DiskInfo
	closeOnce sync.Once
	closeErr  error
}

func NewManager(
	ctx context.Context,
	factories []Factory,
) (_ *Manager, resultErr error) {
	if ctx == nil {
		return nil, errors.New("filesystem manager context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	manager := &Manager{
		disks: make(map[Code]Disk, len(factories)),
		infos: make([]DiskInfo, 0, len(factories)),
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, manager.Close())
		}
	}()

	for index, factory := range factories {
		if factory == nil {
			return nil, fmt.Errorf(
				"filesystem factory at index %d is nil",
				index,
			)
		}
		code := factory.Code()
		if code == "" {
			return nil, fmt.Errorf(
				"filesystem factory at index %d has empty code",
				index,
			)
		}
		if _, exists := manager.disks[code]; exists {
			return nil, fmt.Errorf(
				"filesystem disk %q is configured more than once",
				code,
			)
		}

		disk, err := factory.Open(ctx)
		if !isNilDisk(disk) {
			manager.order = append(manager.order, disk)
		}
		if err != nil {
			return nil, fmt.Errorf("open filesystem disk %q: %w", code, err)
		}
		if isNilDisk(disk) {
			return nil, fmt.Errorf(
				"filesystem factory %q returned nil disk",
				code,
			)
		}
		if disk.Code() != code {
			return nil, fmt.Errorf(
				"filesystem factory %q returned disk %q",
				code,
				disk.Code(),
			)
		}
		if !ValidVisibility(disk.Visibility()) {
			return nil, fmt.Errorf(
				"filesystem disk %q: %w: %q",
				code,
				ErrInvalidVisibility,
				disk.Visibility(),
			)
		}
		if err := disk.Ping(ctx); err != nil {
			return nil, fmt.Errorf("ping filesystem disk %q: %w", code, err)
		}

		label := string(code)
		if provider, ok := factory.(FactoryLabelProvider); ok {
			if configured := strings.TrimSpace(provider.Label()); configured != "" {
				label = configured
			}
		}

		manager.disks[code] = disk
		manager.infos = append(manager.infos, DiskInfo{
			Code:       code,
			Label:      label,
			Visibility: disk.Visibility(),
		})
	}

	return manager, nil
}

func isNilDisk(disk Disk) bool {
	if disk == nil {
		return true
	}
	value := reflect.ValueOf(disk)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (m *Manager) Disk(code Code) (Disk, bool) {
	if m == nil {
		return nil, false
	}
	disk, exists := m.disks[code]
	return disk, exists
}

func (m *Manager) Disks() []DiskInfo {
	if m == nil {
		return nil
	}
	return append([]DiskInfo(nil), m.infos...)
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		var closeErrors []error
		for index := len(m.order) - 1; index >= 0; index-- {
			disk := m.order[index]
			if err := disk.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf(
					"close filesystem disk %q: %w",
					disk.Code(),
					err,
				))
			}
		}
		m.closeErr = errors.Join(closeErrors...)
	})
	return m.closeErr
}
