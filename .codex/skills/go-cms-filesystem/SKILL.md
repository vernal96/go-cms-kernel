---
name: go-cms-filesystem
description: Use for GO CMS filesystem architecture or implementation: filesystem.Disk/Factory/Manager, project disk declarations, local/S3 drivers, disk labels and visibility, module filesystem aliases/bindings, CMS files/folders, FileExplorer/file picker, upload/download/preview URLs, filesystem-backed cache, or tests involving storage selection.
---

# GO CMS Filesystem

Follow root `AGENTS.md`. This skill defines the filesystem model and implementation rules for GO CMS.

## Mental model

Keep these concepts separate:

- **Driver** = how bytes are physically stored (`local`, `s3`, future `azure`, `gcs`, etc.).
- **Disk** = one configured physical storage instance with a stable `Code`, human-facing `Label`, `Visibility`, driver, and driver-specific settings.
- **Code** = stable machine identifier persisted in CMS data and used to resolve a disk (`media`, `documents`, `private`).
- **Label** = human-facing name shown in admin/catalog UIs (`Медиатека`, `Документы`, `Приватные файлы`). It may change without changing persisted references.
- **Visibility** = access policy (`public` or `private`). It is independent from the driver.
- **Module alias** = module-local logical capability mapped to a physical disk (`spool -> private`, `attachments -> documents`).
- **Path/key** = object location inside one disk.

Do not collapse these concepts.

Example:

```text
project disks
  media
    label: Медиатека
    driver: s3
    visibility: public

  documents
    label: Документы
    driver: s3
    visibility: private

  internal
    label: Служебные файлы
    driver: local
    visibility: private

module bindings
  mail.spool  -> internal
  forms.spool -> internal
```

## Application ownership

Physical disks are application-owned infrastructure.

`filesystem.Manager` owns configured disk instances and their lifecycle. The project layer declares which disks exist; kernel/module code must not hardcode project disk names such as `public`, `private`, or `media` unless the contract explicitly requires one particular project code.

The manager resolves disks by stable code:

```go
disk, ok := manager.Disk("media")
```

The catalog exposed to admin/UI uses `filesystem.DiskInfo`:

```go
type DiskInfo struct {
    Code       Code
    Label      string
    Visibility Visibility
}
```

`Label` is presentation metadata. Never persist it as the storage identity of a file/folder/reference.

## Project disk declarations

The project layer may declare any number of disks. Do not model `public` and `private` as the only possible disks.

Project disks are separate Go declarations under `backend/internal/filesystems`,
registered explicitly as factories in `internal/config.Config.Application`.
Each declaration contains its code, label, driver and visibility and chooses which
settings are fixed in Go versus supplied by its typed environment configuration.

For example, the `public` and `private` packages fix their identity and local
storage driver in Go, while `Files.Public` / `Files.Private` load roots, base URLs
and a private signing key from individual `FILES_PUBLIC_*` / `FILES_PRIVATE_*`
variables. Constructors can also receive these values directly from Go.
Environment loading happens once during project configuration loading; request
handling and factory registration must not read environment variables.

Use the existing `corefiles.NewFactory` for project local/S3 declarations. See
`backend/internal/filesystems/README.md` for complete Go-only and mixed S3 examples.
Do not restore a JSON disk list in env or automatic/global registration.

Adding another disk must not require changing `kernel/filesystem` or adding another hardcoded constructor like `PublicFactory`/`PrivateFactory`.

## Drivers

Drivers implement the generic filesystem contracts. Current infrastructure includes local storage and S3-compatible storage.

A driver must not know CMS entities such as Resource, Media, User, or Site.

Driver-specific configuration belongs in the project/connector layer. The kernel contract must remain technology-neutral.

Do not add methods such as `S3Bucket()` or `LocalRoot()` to `filesystem.Disk`.

Use optional capability interfaces only when higher layers genuinely need a behavior that not every driver supports, for example:

```text
OverwriteDisk
PrefixScannerProvider
KeyDistributionProvider
TemporaryURLVerifier
```

## Visibility

Visibility is a disk property, not a driver type.

Valid combinations include:

```text
local + public
local + private
s3 + public
s3 + private
```

Do not infer visibility from a disk code such as `public` or `private`.

Private disks must use the established temporary/signed URL behavior. Public disks may expose normal public URLs according to their driver configuration.

## Stable disk codes

A disk code is persisted in `core.files.storage` and `core.file_folders.storage` and participates in filesystem namespace constraints.

Treat a code as stable once files exist.

Changing only:

```text
label: "Документы" -> "Документы компании"
```

is safe.

Changing:

```text
code: documents -> company_documents
```

changes storage identity and requires an explicit data/storage migration. Never silently treat code renaming as a UI rename.

## CMS file namespace

The same logical path/name may exist independently on different disks.

Example:

```text
media:/images/logo.png
archive:/images/logo.png
```

Database constraints must keep `storage` as part of the namespace for folders/files and must prevent folder trees from crossing disk boundaries.

A CMS `File` stores the disk code plus the object path/key. It does not store the driver name.

## Admin FileExplorer

Admin filesystem UI discovers disks from the backend catalog. Do not hardcode a list of disks in the frontend.

Expected flow:

```text
GET /api/files/disks
  -> code + label + visibility

select disk by code
  -> display label to the user
  -> send code in API requests
```

The UI should normally render the human label as the primary text. Code/visibility may be secondary diagnostic text where useful.

All browse/create/upload/move operations must keep sending the stable disk code.

File picker restrictions use allowed disk codes (`allowedStorages` / field `Storages`). They must never use labels because labels are mutable presentation data.

## Module aliases and bindings

Module code must not resolve arbitrary global disk codes when a module-local storage capability is sufficient.

A profile binds an alias to a project disk:

```text
mail:
  spool -> internal

forms:
  spool -> internal
```

Module code resolves by alias through `ModuleContext.Filesystems()`.

The alias describes purpose/capability, not infrastructure technology. Good aliases:

```text
spool
attachments
exports
media
```

Bad aliases:

```text
s3
local
minio
```

Do not confuse module alias with disk `Label`.

## Internal/infrastructure objects

Not every object stored on a disk is a CMS-managed file.

Examples such as Mail/Forms transient spool objects may:

- use a module alias bound to a private disk;
- have no `core.files` row;
- never appear in FileExplorer;
- use a module/site namespace or prefix.

Do not create fake CMS File records merely so infrastructure-owned spool data can use a disk.

## File fields and picker restrictions

File field definitions may restrict allowed physical disk codes and MIME types.

Example:

```go
field.FileOptions{
    Storages: []filesystem.Code{"media", "documents"},
    MIMETypes: []string{"image/*"},
}
```

The backend must validate persisted references against these constraints; frontend filtering alone is not sufficient authorization/validation.

## Configuration changes

When changing project disk configuration, inspect all code-valued references that may point to disks, including:

```text
FILES_INTERNAL_STORAGE
FILES_AVATAR_STORAGE
CACHE_FILESYSTEM_STORAGE
MAIL_UPLOAD_STORAGE
profile module filesystem bindings
field FileOptions.Storages
```

A configured reference to a missing disk should fail early during boot/runtime assembly rather than silently falling back to another disk.

## Implementation workflow

For filesystem changes:

1. inspect `github.com/vernal96/go-cms-kernel/filesystem` contracts/manager in
   the kernel checkout;
2. inspect project disk declaration/factories;
3. inspect only the affected driver if driver behavior changes;
4. inspect core file service/repository if CMS file semantics change;
5. inspect module aliases/bindings if a module uses storage;
6. inspect `/api/files/*` and FileExplorer/FilePicker if user-facing disk selection changes;
7. update focused tests and configuration examples;
8. run focused backend/frontend validation, then broad validation for cross-cutting changes.

Explicitly answer:

```text
What is the disk code?
What is its human label?
What is the driver?
What is the visibility?
Is the caller resolving a physical code or a module alias?
Is this a CMS-managed file or infrastructure-only object?
Does changing this value affect persisted storage identity?
```

## Required tests

For catalog/config changes, cover at least:

```text
multiple arbitrary disk codes are accepted
human labels are returned by the catalog/API
duplicate disk codes fail
missing/invalid visibility fails
unknown driver fails
manager resolves the expected disk by code
module alias resolves only its configured disk
```

For FileExplorer changes, cover discovery and disk switching without hardcoded disk names.

## Anti-patterns

Do not:

```text
hardcode exactly public/private disks in kernel
use visibility as the disk identity
persist labels instead of disk codes
use driver names as module aliases
make modules know S3/local details
hardcode disk choices in FileExplorer
rename a persisted disk code as if it were presentation text
put infrastructure-only spool objects into core.files without a domain reason
add one filesystem interface per concrete technology
```

Prefer `driver -> named physical disk -> module alias` with stable codes, mutable human labels, explicit visibility, project-owned composition, and technology-neutral kernel contracts.
