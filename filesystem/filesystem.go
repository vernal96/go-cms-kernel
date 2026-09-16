package filesystem

import (
	"context"
	"errors"
	"io"
	"time"
)

type Code string
type Alias string

type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

var (
	ErrNotFound          = errors.New("filesystem object not found")
	ErrConflict          = errors.New("filesystem object already exists")
	ErrDiskNotFound      = errors.New("filesystem disk not found")
	ErrInvalidVisibility = errors.New("invalid filesystem visibility")
	ErrUnsupported       = errors.New("filesystem operation is unsupported")
	ErrUnauthorized      = errors.New("filesystem URL is unauthorized")
)

type Reference struct {
	ID   string
	Path string
}

type DiskInfo struct {
	Code       Code
	Label      string
	Visibility Visibility
}

type Disk interface {
	Code() Code
	Visibility() Visibility
	Ping(context.Context) error
	PutNew(context.Context, string, io.Reader, string) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
	URL(context.Context, Reference) (string, error)
	TemporaryURL(context.Context, Reference, time.Time) (string, error)
	Close() error
}

// FactoryLabelProvider is an optional application-composition capability.
// Labels are human-facing disk names for admin/catalog UIs; disk codes remain
// stable machine identifiers stored in domain data and used for resolution.
type FactoryLabelProvider interface {
	Label() string
}

// OverwriteDisk is an optional capability for infrastructure that needs
// atomic replace semantics. The core file service intentionally continues to
// use Disk.PutNew so uploaded files can never be overwritten.
type OverwriteDisk interface {
	Put(context.Context, string, io.Reader, string) error
}

// PrefixWalker is an optional maintenance capability. Hot-path cache
// invalidation must not depend on it.
type PrefixWalker interface {
	WalkPrefix(context.Context, string, func(string) error) error
}

type PrefixScanPage struct {
	Keys []string
	Done bool
}

// PrefixScan is a stateful bounded scan. Each Next call advances retained
// traversal/continuation state instead of restarting from the prefix root.
// Implementations must release resources on Close and when Done is reached.
type PrefixScan interface {
	Next(context.Context, int) (PrefixScanPage, error)
	Close() error
}

// PrefixScannerProvider is an optional maintenance capability that maps to a
// stateful directory traversal locally and continuation-token scans on object
// stores.
type PrefixScannerProvider interface {
	OpenPrefixScan(context.Context, string) (PrefixScan, error)
}

type KeyDistribution string

const (
	KeyDistributionHierarchical KeyDistribution = "hierarchical"
	KeyDistributionSelfManaged  KeyDistribution = "self_managed"
)

// KeyDistributionProvider lets higher-level infrastructure choose an
// efficient object-key layout without identifying the concrete disk driver.
type KeyDistributionProvider interface {
	KeyDistribution() KeyDistribution
}

// TemporaryURLVerifier is implemented by disks whose temporary URLs are
// delivered by this application (currently localstorage).
type TemporaryURLVerifier interface {
	VerifyTemporaryURL(Reference, time.Time, string) error
}

type Factory interface {
	Code() Code
	Open(context.Context) (Disk, error)
}

type Resolver interface {
	Disk(Code) (Disk, bool)
}

type Catalog interface {
	Resolver
	Disks() []DiskInfo
}

type Binding struct {
	Alias Alias
	Code  Code
}

type ModuleManager interface {
	Disk(Alias) (Disk, bool)
	Binding(Alias) (Binding, bool)
}

func ValidVisibility(value Visibility) bool {
	return value == VisibilityPublic || value == VisibilityPrivate
}
