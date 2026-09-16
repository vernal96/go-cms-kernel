# Images

## Persistent edits

`Processor` and bounded `TransformOptions` are storage-independent contracts.
The pure-Go `adapters/imaging` adapter uses `disintegration/imaging` v1.6.2;
no native image runtime is required. The Media image service owns orchestration.

Every save reads the root original, uploads a sibling derivative with
`File.ParentID = original.ID`, and switches `Media.FileID` with an optimistic
`updated_at` check under the existing Media repository lock. The original is
never overwritten. `Media.Params.image` stores `{version: 1, transform: ...}`.
There is no image history table.

A failed switch attempts to delete the new derivative. A successful switch
attempts safe deletion of the previous derivative; other Media/File-field
references prevent cleanup. Cleanup failures are logged without undoing a valid
switch. Cleanup uses a separate bounded context so request cancellation does
not skip compensation. Restore clears image metadata, switches to the original,
and safely cleans the old derivative; an already restored image is a no-op.

## Formats, transforms and limits

JPEG and static PNG are decoded and encoded, retaining their format. JPEG EXIF
orientation is applied before transforms. SVG, GIF, WebP, and animated PNG are
rejected. Quality applies to JPEG; PNG remains lossless. JPEG containment uses
white padding, PNG uses transparent padding.

Order: EXIF orientation → scale/flip → clockwise rotation → crop → output fit.
Crop coordinates refer to that transformed original. Rotation accepts quarter
turns. Fit is `contain`, `cover`, or explicit `stretch`; nine standard anchors
are supported. A zero output dimension is inferred from crop proportions.

Defaults (configurable with `core.Config.Images`, subject to hard upper bounds):

| Limit | Default |
| --- | --- |
| Source width/height | 8192 px |
| Decoded source/intermediate pixels | 32,000,000 |
| Output width/height | 4096 px |
| Output pixels | 16,000,000 |
| Compressed input/output | 32 MiB |
| Absolute scale | 0.1–4; negative values flip |
| JPEG quality | 1–100; default 85 |
| Thumbnail width/height | 512 px |

Headers and source pixel counts are checked before full decoding. Crop bounds
are checked against actual transformed dimensions. Stable domain errors are
`ErrInvalidTransform`, `ErrUnsupportedFormat`, and `ErrLimit`.

## Management API

All routes use existing authentication and file/Media permissions.

| Method | Route | Purpose |
| --- | --- | --- |
| POST | `/api/media` | Create Media from `{file_id}` |
| GET | `/api/media/{id}/image` | Current/root files, transform, version token, edit/restore flags, limits |
| POST | `/api/media/{id}/image` | Save `{expected_updated_at, transform}` |
| POST | `/api/media/{id}/image/restore` | Restore with `{expected_updated_at}` |
| GET | `/api/files/{id}/thumbnail` | Cached raster variant |
| POST | `/api/files/delete-impact` | Preview `{items:[{kind:"file",id:1}]}` |
| POST | `/api/files/delete` | Delete with items, policy and impact token |

Edit conflicts return 409; unsupported/invalid/oversized transforms return 422.
Filesystem item responses distinguish `folder_id` from `source_file_id`.
Resource and LibraryItem DTOs expose `image_media_id`; generic File fields still
store File IDs.

Self-avatar operations use `/api/admin/profile/avatar/image` (GET/POST),
`/image/restore` (POST), `/image/source` (GET), and `/avatar/thumbnail` (GET,
relative to `/api/admin/profile`). They resolve the authenticated user's avatar
before invoking internal file/Media operations, following existing self-profile
authorization conventions.

## Thumbnails and cache

Example: `/api/files/1/thumbnail?width=128&height=128&fit=contain&position=center`.
Dimensions use a finite grid: 64, 128, 256, 512, and optionally 1024 when allowed
by configuration. Unknown/duplicate query parameters are rejected. Quality and
arbitrary editor transforms cannot be supplied to this endpoint.

The cache key is
`images:thumb:v1:<file-id>:<source-sha256>:<normalized-transform-sha256>`.
Cache-aside generation stores the encoded result through the module cache
abstraction with a 24-hour TTL. No File, Media, child, or SQL thumbnail rows are
created. Live source access is checked even on cache hits and before 304 replies.
Responses include the actual MIME type, output ETag, `private, no-cache`,
`Vary: Authorization`, and `X-Content-Type-Options: nosniff`.

Core declares alias `thumbnails`; the dev profile binds it to the project's
existing filesystem cache. Services are built once at boot per profile, never
per request. The global files endpoint accepts `profile=<code>` for explicit
cache namespace selection; omission requires one configured profile, as in dev.
Image editor limits must agree across explicitly configured profiles because
Media and Files have global identity. Multi-profile clients must select a
thumbnail profile; the current admin targets the single dev profile.

## Filesystem deletion

Impact includes selected count, all affected physical files, hidden derivatives,
Media references, blocking generic File-field references, and a snapshot token.
The admin confirms `policy: "confirmed_media_cascade"` with `impact_token`.
Execution locks the file tree, Media and owners and rechecks the snapshot,
including owner identities. Core composes the resource and user mutation scopes;
the file adapter calls the owner adapters inside its existing SQL transaction.
The `media_cascade` operation clears image fields/avatars, runs owner before-hooks,
updates versions and audit fields, records resource revisions (respecting the
site's LibraryItem history policy), and appends outbox events and hook recipients.
Hooks may veto the cascade but cannot rewrite its derived owner changes.
Only after these steps succeed does deletion touch physical storage and remove
File/Media rows. A veto or outbox failure rolls back all SQL without touching
storage; affected resource caches invalidate at the common repository boundary.

Media selected in a typed resource field belongs to that field. Replacing or
clearing the value and permanently deleting its owner/subtree remove detached
Media metadata in the same transaction. Still-owned Media and physical source
Files remain intact; safe file deletion is no longer blocked by orphan metadata.
Generic File-field references always block this cascade. Ordinary internal
`DeleteFile`/`DeleteFolder` remain safe and reject referenced files.

Pre-production migrations are corrected directly: migration 000009 no longer
replaces the Media→File cascade with RESTRICT; migration 000015 declares
LibraryItem→Media as SET NULL. Already applied development migrations do not
automatically rerun: existing development databases need recreation/reapplication
of the corrected schema. No user database is reset by this implementation.

Physical storage and SQL are not one transaction. Repository deletion retains
the existing physical-then-SQL behavior; a storage/commit failure can require
operational repair. Persistent edit compensation is best effort with logging;
there is no automatic retry worker or retained edit history.

## Admin and validation

One Cropper.js 1.6.2 editor provides visual cropping, quarter-turn rotation,
zoom, flips, output dimensions/proportions, fit/anchor, reset, save and restore
for Resources, LibraryItems and self-avatar. The browser submits transform data;
the backend generates the saved image. FileExplorer/FileField use thumbnails
for small previews and load full images only on explicit preview/editor actions.

Focused tests cover transforms/limits, cache identity and authorization,
root/sibling edits, compensation, cleanup, restore, concurrency, deletion and
cache invalidation. PostgreSQL integration tests include all three owner kinds,
generic File-field blocking, stale impact and concurrent Media switches.
Frontend tests exercise thumbnail/full-preview routes, editor requests, restore,
delete warnings, and generic File field semantics.

### Template Media field

Templates can declare an optional image-backed Media field:

```go
{Key: "page_media", Type: field.TypeMedia, Label: "Медиа"}
```

Its value is a numeric Media ID, persisted with the existing `reference` storage
kind. `ReferenceValueType` identifies the referenced entity independently of the
field type code. The PostgreSQL adapter records ownership in
`core.resource_media_references` (migration 000022), sharing the same path for
Resources and LibraryItems, including replacement and revision restore. Media
remains attached to a single owner; replacing the field removes stale ownership.
Ordinary Media deletion is blocked while a template field references it.
Confirmed filesystem cascading removes the typed field value and ownership row
transactionally, then invalidates the affected resource caches.

The dev template **Страница** includes **Медиа** under **Параметры полей → Контент**.
Its control uses the same `MediaImageField` and `ImageEditor` as the built-in
resource image. Generic `file` fields retain their separate File ID semantics.
