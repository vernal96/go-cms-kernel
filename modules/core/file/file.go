package file

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/security"
)

type ID int64
type FolderID int64

var (
	ErrNotFound         = errors.New("file not found")
	ErrFolderNotFound   = errors.New("file folder not found")
	ErrConflict         = errors.New("file namespace conflict")
	ErrInvalidTree      = errors.New("invalid file folder tree")
	ErrInvalidReference = errors.New("invalid file reference")
	ErrStorageNotFound  = errors.New("file storage not found")
	ErrStorageMismatch  = errors.New("file storage mismatch")
	ErrUnauthorized     = errors.New("file delivery is unauthorized")
	ErrInUse            = errors.New("file is in use")
	ErrInvalidInput     = errors.New("invalid file input")
)

type ItemKind string

const (
	ItemFile   ItemKind = "file"
	ItemFolder ItemKind = "folder"
)

type Folder struct {
	ID        FolderID
	ParentID  *FolderID
	Storage   filesystem.Code
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
	CreatedBy *security.UserID
	UpdatedBy *security.UserID
}

type File struct {
	ID             ID
	FolderID       *FolderID
	Storage        filesystem.Code
	Name           string
	MIMEType       string
	Size           int64
	ChecksumSHA256 string
	Path           string
	ParentID       *ID
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CreatedBy      *security.UserID
	UpdatedBy      *security.UserID
}

type CreateFolderInput struct {
	ParentID *FolderID
	Storage  filesystem.Code
	Name     string
}

type UploadInput struct {
	FolderID *FolderID
	Storage  filesystem.Code
	Name     string
	ParentID *ID
	Content  io.Reader
}

type MoveFileInput struct {
	ID       ID
	FolderID *FolderID
}

type MoveFolderInput struct {
	ID       FolderID
	ParentID *FolderID
}

type RenameFileInput struct {
	ID   ID
	Name string
}

type RenameFolderInput struct {
	ID   FolderID
	Name string
}

type ItemReference struct {
	Kind ItemKind
	ID   int64
}

type MoveItemsInput struct {
	Storage  filesystem.Code
	FolderID *FolderID
	Items    []ItemReference
}

type DeleteItemsInput struct {
	Items []ItemReference
}

type FolderEntry struct {
	Folder    Folder
	ItemCount int
}

type BrowserListing struct {
	Storage     filesystem.Code
	Visibility  filesystem.Visibility
	Folder      *Folder
	Breadcrumbs []Folder
	Folders     []FolderEntry
	Files       []File
}

type Listing struct {
	Folder  *Folder
	Folders []Folder
	Files   []File
}

type OpenedFile struct {
	File File
	Body io.ReadCloser
}

type DeliveryAuthorization struct {
	ExpiresAt time.Time
	Signature string
}

type DeletePhysical func(context.Context, []File) error

type Repository interface {
	NameAvailable(
		context.Context,
		filesystem.Code,
		*FolderID,
		string,
	) error
	CreateFolder(context.Context, Folder) (Folder, error)
	FolderByID(context.Context, FolderID) (Folder, error)
	ListFolders(
		context.Context,
		filesystem.Code,
		*FolderID,
	) ([]Folder, error)
	CreateFile(context.Context, File) (File, error)
	FileByID(context.Context, ID) (File, error)
	ListFiles(
		context.Context,
		filesystem.Code,
		*FolderID,
	) ([]File, error)
	MoveFile(
		context.Context,
		*security.UserID,
		ID,
		*FolderID,
	) (File, error)
	MoveFolder(
		context.Context,
		*security.UserID,
		FolderID,
		*FolderID,
	) (Folder, error)
	DeleteFile(context.Context, ID, DeletePhysical) error
	DeleteFolder(context.Context, FolderID, DeletePhysical) error
}

type ManagementRepository interface {
	Repository
	CreateAvailableFolder(context.Context, Folder) (Folder, error)
	CreateAvailableFile(context.Context, File) (File, error)
	FolderAncestors(context.Context, FolderID) ([]Folder, error)
	ListFolderEntries(
		context.Context,
		filesystem.Code,
		*FolderID,
	) ([]FolderEntry, error)
	RenameFile(
		context.Context,
		*security.UserID,
		ID,
		string,
	) (File, error)
	RenameFolder(
		context.Context,
		*security.UserID,
		FolderID,
		string,
	) (Folder, error)
	MoveItems(
		context.Context,
		*security.UserID,
		MoveItemsInput,
	) ([]Folder, []File, error)
	DeleteItems(context.Context, []ItemReference, DeletePhysical) error
}

type Service interface {
	CreateFolder(
		context.Context,
		security.Actor,
		CreateFolderInput,
	) (Folder, error)
	GetFolder(
		context.Context,
		security.Actor,
		FolderID,
	) (Folder, error)
	ListFolder(
		context.Context,
		security.Actor,
		filesystem.Code,
		*FolderID,
	) (Listing, error)
	Upload(context.Context, security.Actor, UploadInput) (File, error)
	GetFile(context.Context, security.Actor, ID) (File, error)
	Open(context.Context, security.Actor, ID) (OpenedFile, error)
	OpenDelivery(
		context.Context,
		ID,
		DeliveryAuthorization,
	) (OpenedFile, error)
	MoveFile(
		context.Context,
		security.Actor,
		MoveFileInput,
	) (File, error)
	MoveFolder(
		context.Context,
		security.Actor,
		MoveFolderInput,
	) (Folder, error)
	DeleteFile(context.Context, security.Actor, ID) error
	DeleteFolder(context.Context, security.Actor, FolderID) error
	URL(context.Context, security.Actor, ID) (string, error)
	TemporaryURL(
		context.Context,
		security.Actor,
		ID,
		time.Time,
	) (string, error)
}

type ManagementService interface {
	Service
	Disks(context.Context, security.Actor) ([]filesystem.DiskInfo, error)
	Browse(
		context.Context,
		security.Actor,
		filesystem.Code,
		*FolderID,
	) (BrowserListing, error)
	ResolveFolder(
		context.Context,
		security.Actor,
		filesystem.Code,
		string,
	) (Folder, error)
	EnsureFolderPath(
		context.Context,
		security.Actor,
		filesystem.Code,
		string,
	) (Folder, error)
	CreateAvailableFolder(
		context.Context,
		security.Actor,
		CreateFolderInput,
	) (Folder, error)
	UploadAvailable(
		context.Context,
		security.Actor,
		UploadInput,
	) (File, error)
	RenameFile(
		context.Context,
		security.Actor,
		RenameFileInput,
	) (File, error)
	RenameFolder(
		context.Context,
		security.Actor,
		RenameFolderInput,
	) (Folder, error)
	MoveItems(
		context.Context,
		security.Actor,
		MoveItemsInput,
	) ([]Folder, []File, error)
	DeleteItems(
		context.Context,
		security.Actor,
		DeleteItemsInput,
	) error
}

func CloneFolder(item Folder) Folder {
	item.ParentID = cloneFolderID(item.ParentID)
	item.CreatedBy = cloneUserID(item.CreatedBy)
	item.UpdatedBy = cloneUserID(item.UpdatedBy)
	return item
}

func Clone(item File) File {
	item.FolderID = cloneFolderID(item.FolderID)
	item.ParentID = cloneID(item.ParentID)
	item.CreatedBy = cloneUserID(item.CreatedBy)
	item.UpdatedBy = cloneUserID(item.UpdatedBy)
	return item
}

func cloneUserID(value *security.UserID) *security.UserID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneID(value *ID) *ID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneFolderID(value *FolderID) *FolderID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
