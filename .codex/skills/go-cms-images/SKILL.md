---
name: go-cms-images
description: Use for GO CMS image/media processing: image editor, persistent edited derivatives, media-to-file switching, original restore, thumbnails, resize/crop/rotate/scale transforms, image cache, thumbnail HTTP delivery, FileExplorer image previews, and deletion semantics for image derivative trees.
---

# GO CMS Images

Follow root `AGENTS.md`, and the existing filesystem/cache rules. This skill defines the image-processing model for GO CMS.

## Scope

Load this skill when work materially involves one or more of:

- editing an image selected through Media;
- crop, rotate, resize, stretch/scale, fit/cover or anchor/position behavior;
- persistent edited image versions;
- restore-to-original behavior;
- image thumbnails/previews;
- image-processing adapters/libraries;
- thumbnail cache keys, generation or delivery;
- FileExplorer/FilePicker/FileField image preview optimization;
- deletion of original files that have derived image files.

When the task changes generic filesystem behavior too, also load `go-cms-filesystem`. When it changes cache bindings/stores/coherence, also load `go-cms-cache`. When it changes HTTP contracts materially, also load `go-cms-api`. Do not load them if the image skill plus current code is sufficient.

## Core mental model

Keep these concepts separate:

- **File** = a persistent CMS-managed physical file registered in `core.files`.
- **Original file** = the root persistent file uploaded by a user or another normal CMS upload flow.
- **Derived file** = a persistent user-visible edited version of an original, registered in `core.files` with `ParentID` pointing to the root original.
- **Media** = semantic image/media metadata whose `FileID` points to the currently active persistent file.
- **ImageProcessor** = technology-neutral service that transforms image bytes.
- **Transform** = normalized crop/rotate/resize/scale options passed to the processor.
- **Thumbnail** = regeneratable cached bytes for presentation. It is not a File and not a Media row.

Never collapse edited derivatives and thumbnails into one concept.

```text
persistent user data

Media
  -> current edited File
       -> ParentID = original File

regeneratable presentation cache

File + ThumbnailSpec
  -> thumbnail cache entry
```

## Existing filesystem derivative model

`file.File.ParentID` / `core.files.parent_id` is the canonical persistent parent-file relationship. Reuse it for edited images instead of adding a separate image-version table merely to represent ancestry.

The database already cascades child file rows when the parent row is removed. Filesystem repository deletion also walks the descendant tree so physical objects can be removed before the database delete.

Do not repurpose folder `parent_id` semantics. At API/DTO boundaries use explicit names when both concepts are present:

```text
folder_id
source_file_id / parent_file_id
```

Avoid an ambiguous frontend `parent_id` that sometimes means folder and sometimes means source file.

## Original is immutable

Image editing must never overwrite the original physical object.

An edit creates a new persistent File and switches `Media.FileID` to it.

Bad:

```text
original.jpg --overwrite--> modified bytes
```

Good:

```text
original.jpg
  \- edited.jpg

Media.FileID = edited.jpg
```

This preserves rollback, auditability and deterministic future edits.

## Always edit from the root original

Do not repeatedly re-encode the previously edited JPEG/PNG.

Bad:

```text
original -> edit1 -> edit2 -> edit3
```

For image-editing semantics prefer siblings rooted at the original:

```text
original
  |- edit1
  |- edit2
  \- edit3
```

When editing Media that already points to a derived file:

1. resolve the current file;
2. walk `ParentID` to the root original;
3. open/process the root original;
4. create the new derivative with `ParentID = root.ID`;
5. switch the Media row to the new file;
6. clean up the previous derived file only after the Media update succeeds and only if it is safe/unused.

The generic filesystem may still support arbitrary descendant trees for other future use; the image service is responsible for the root-original rule.

## Media edit lifecycle

Image editing belongs above the generic filesystem layer. `file.Service` must remain unaware of crop rectangles, JPEG quality, rotations or thumbnail specs.

The image/media service should orchestrate:

```text
Media -> current File -> root File -> ImageProcessor
                              |
                              v
                        transformed bytes
                              |
                              v
                    file.Service.Upload(... ParentID=root.ID)
                              |
                              v
                       Media.FileID update
```

The operation crosses physical storage and SQL, so it is not a single ACID transaction. Use compensation:

- if transform/upload fails: Media remains unchanged;
- if the new File is uploaded but Media update fails: delete the newly created derivative;
- only after Media points to the new file may the old derived file be considered for cleanup;
- cleanup failure must not roll Media back to a broken state; return/report it explicitly according to the established service policy and keep correctness first.

Do not delete the old file before the Media switch commits.

## Restore original

Restore is not another image transform.

Resolve the root original, switch `Media.FileID` back to it, clear/update image-transform metadata, then remove the previous derived file if it is safe and unused.

If Media already points to the root original, restore is idempotent.

Expose enough resolved state for admin UI to know whether restore is available:

```text
current_file_id
original_file_id
is_derived
transform metadata where useful
```

## Media Params

Do not add a table solely for editor transform metadata unless future requirements genuinely require queryable/history data.

Use the existing `Media.Params` object for the current editor metadata. Keep image-specific keys namespaced and versionable, for example conceptually:

```json
{
  "image": {
    "version": 1,
    "transform": {
      "crop": {"x": 10, "y": 20, "width": 800, "height": 600},
      "rotate": 90,
      "scale_x": 1,
      "scale_y": 1,
      "width": 1200,
      "height": 900,
      "fit": "contain",
      "position": "center"
    }
  }
}
```

The authoritative result is still `Media.FileID`; Params describe/reopen editor state and must not be required to serve the already-generated persistent file.

## ImageProcessor contract

Keep image-processing technology replaceable.

The core/domain contract should be behavior-oriented, approximately:

```go
type Processor interface {
    Transform(ctx context.Context, source io.Reader, options TransformOptions) (Result, error)
}
```

Exact types should follow the current package conventions, but the processor must not know Media, repositories, PostgreSQL, S3, Redis, HTTP or admin UI.

Useful normalized concepts:

```text
CropRect: x, y, width, height
Rotation: degrees
ScaleX / ScaleY
Width / Height
Fit: contain | cover | stretch
Position: center | top | bottom | left | right | top-left | ...
Quality where the encoder supports it
```

Validate options before allocating large output buffers.

Prefer a pure-Go initial adapter unless current repository constraints justify a native dependency. The default implementation may use `github.com/disintegration/imaging` or an equivalent maintained library behind the interface. Do not leak that library's types through domain contracts.

## Supported image formats

Treat supported decode/encode formats as an explicit adapter capability, not as "all image/* works".

At minimum the implementation should correctly handle the formats the project actually permits for editable images. Reject unsupported formats with a stable validation/domain error instead of failing deep inside the processor.

Do not treat SVG as a normal raster-edit input unless explicit rasterization support is implemented safely.

Animated formats require an explicit product decision. Do not silently discard animation while claiming lossless support. If only the first frame is supported, make that contract explicit or reject animated input.

## Resource limits and hostile images

Image endpoints process untrusted uploads. Protect the server from decompression bombs and cache abuse.

Configuration/validation should bound at least:

- maximum source dimensions/pixels;
- maximum output dimensions/pixels;
- maximum crop/output sizes;
- accepted rotation/scale ranges;
- thumbnail width/height;
- quality range;
- transform/query cardinality where arbitrary public thumbnails are possible.

Do not rely only on compressed file byte size. A tiny compressed file may decode into an enormous raster.

Use `context.Context` throughout I/O and processing boundaries where the adapter can honor cancellation.

## Thumbnail model

A thumbnail is cache, not persistent domain data.

Never create:

- a `core.files` row;
- a `core.media` row;
- a child File;
- a thumbnail database table merely to remember generated variants.

A thumbnail should be derivable from:

```text
source File identity/content + normalized ThumbnailSpec + implementation format version
```

The service flow is cache-aside:

```text
request thumbnail
  -> normalize/validate spec
  -> compute deterministic key
  -> cache hit: return cached bytes
  -> cache miss:
       open source File
       ImageProcessor.Transform(...)
       cache bytes
       return bytes
```

Use a module-local cache alias dedicated to thumbnail/image variants when that gives clearer storage policy. The project chooses which physical cache implementation backs that alias. Never hardcode Redis/file/memory technology in the image service.

A filesystem-backed cache is a good project binding for large thumbnail bytes, but it is a project choice rather than a core dependency.

## Thumbnail cache key

Keys must be deterministic and collision-resistant. Include enough data that replacing/changing the source content cannot return a stale variant.

Conceptually:

```text
images:thumb:v1:<file-id>:<source-checksum>:<normalized-transform-hash>
```

The normalized transform hash must be based on canonical fields/order, not raw query-string order.

Do not key only by file name/path. IDs and checksum/content identity are more stable.

Using source checksum in the key means old variants become unreachable after source content changes; normal cache expiry/pruning may reclaim them later.

## Thumbnail HTTP API

Keep the reusable thumbnail service transport-neutral, then expose it through existing HTTP management/public layers.

For admin filesystem usage a contract such as this is appropriate:

```text
GET /api/files/{fileID}/thumbnail?width=128&height=128&fit=contain&position=center
```

The exact route should follow current API conventions after inspecting them.

Return the real output MIME type and sensible caching headers/ETag. Preserve current authorization rules: an authenticated admin thumbnail route must not become an authorization bypass for private files.

If a public delivery route is added later, preserve the same public/private disk semantics used by normal file delivery.

Do not expose arbitrary unbounded transforms on a public endpoint without limits; otherwise every unique query can create a new expensive cache entry.

## FileExplorer and field previews

Do not download the full original merely to render a 64-128px grid tile.

FileExplorer/FilePicker should use the thumbnail endpoint for raster images. Full `/preview` remains appropriate when the user actually opens the file preview/editor.

Likewise image-oriented field controls should render a bounded thumbnail and load the original/current full image only when the editor or full preview opens.

Generic non-image `FileField` semantics remain file references. Do not silently convert all `TypeFile` fields into Media fields.

## Admin image editor

The browser editor is responsible for UX and collecting transform parameters. The backend is responsible for producing the canonical persistent result from the original.

Use a maintained cropper/editor library when it fits the current Vue stack rather than writing fragile crop math from scratch. `cropperjs` is an acceptable default when compatible with the project's current frontend build.

Expected controls include:

- crop area;
- rotate left/right;
- zoom/scale;
- horizontal/vertical scale/flip if product UX exposes it;
- output width/height where applicable;
- preserve-proportions / contain;
- cover/crop;
- position/anchor;
- reset current editor state;
- restore original when Media points to a derived File.

The frontend should submit normalized transform data to the backend. Do not make a browser-generated canvas blob the authoritative saved edit unless explicitly requested; server-side generation keeps behavior consistent and preserves root-original semantics.

## Deletion semantics

Deleting an original persistent File must account for its entire descendant tree.

The existing filesystem repository already walks descendants for physical deletion. Preserve that behavior.

The admin filesystem should expose an impact/preview step before a destructive delete when derived files exist. The warning should tell the user at least:

```text
number of selected files/folders
number of derived files that will also be deleted
whether Media references are affected
whether direct file-field references block deletion
```

Do not silently delete a tree with hidden descendants from FileExplorer without warning.

For the image-editing use case, an original may have a currently active derived file referenced by Media. Therefore a confirmed admin delete needs explicit semantics rather than accidentally failing on the Media reference.

Preferred behavior:

- safe/default service deletes continue to protect referenced files;
- admin deletion first requests impact and requires explicit confirmation when descendants/Media are affected;
- an explicitly confirmed cascade may delete Media rows that point anywhere inside the deleted file tree, allowing owning entities with established `ON DELETE SET NULL` Media FKs to clear their image reference;
- direct generic file-field references remain protected unless the field-owner update semantics are explicitly implemented. Do not leave typed field values dangling merely to make deletion succeed.

Implement this through a clear delete mode/policy or management-level operation, not through a hidden boolean buried in unrelated code.

Because the project is pre-production, existing migrations may be corrected if a database FK currently prevents the intended clean semantics; follow root `AGENTS.md` pre-production policy rather than adding compatibility layers solely for old local data.

## Old derived cleanup

When an edit replaces one derived file with another, avoid accumulating unbounded obsolete variants.

After the Media switch succeeds:

- if the previous current file is the root original, keep it;
- if it is a derived file and no remaining live reference uses it, delete it;
- if it is still referenced, keep it;
- never delete the root original during edit/restore cleanup.

Thumbnail variants do not participate in this lifecycle because they are cache entries.

## Concurrency

Two concurrent edits of the same Media must not lose correctness.

Use the existing repository locking/version conventions where possible. The final Media update must detect/serialize conflicting edits rather than allowing an older operation to overwrite a newer result unnoticed.

Any uploaded derivative that loses a race must be compensated/cleaned up.

Thumbnail generation may use singleflight/coalescing for the same deterministic key if the current cache/runtime patterns support it, but do not add unnecessary global mutable state. Duplicate generation is preferable to incorrect locking if no reusable mechanism exists yet.

## Permissions

Editing an image is not merely "read file". Enforce the domain permission required to update the Media/owner plus the file permissions needed for reading/creating the derived file according to existing service conventions.

Thumbnail reads inherit the source-file visibility/authorization context. Never allow thumbnail generation to bypass private-file checks.

Restore requires the same update authority as editing.

Admin delete impact requires read access; destructive execution requires delete access and any additional domain checks introduced by cascade semantics.

## Recommended implementation order

Keep the change reviewable even when the overall task is broad:

1. domain transform types + `ImageProcessor` contract;
2. initial processor adapter + focused unit tests;
3. image edit/restore service orchestration using root originals and child Files;
4. thumbnail service + deterministic cache keys + limits;
5. core cache alias/binding for thumbnails if needed;
6. HTTP endpoints/DTOs for image state, edit, restore and thumbnail;
7. delete-impact/cascade semantics;
8. FileExplorer/FilePicker thumbnail usage;
9. reusable Vue image editor and integration into existing Media-backed image controls;
10. tests and broad validation.

Do not dump all behavior into `management/files_http.go` or one Vue component. Keep processor, domain orchestration, transport and UI responsibilities separate.

## Focused tests

At minimum cover behavior such as:

- editing an original creates a child File and switches Media;
- editing an already-derived Media reads the root original and creates a sibling, not a grandchild;
- failed Media update deletes the newly uploaded derivative;
- restore points Media back to root and cleans an unused old derivative;
- thumbnail cache hit avoids reprocessing;
- checksum/spec changes produce different thumbnail keys;
- invalid/oversized transform specs fail before expensive processing;
- private file thumbnails require proper authorization;
- FileExplorer requests thumbnail route instead of full preview for tiles;
- delete impact reports descendants;
- confirmed original deletion removes descendants and handles Media according to the explicit cascade policy;
- direct file-field references still prevent unsafe deletion;
- concurrent edit conflict does not leave Media pointing to an unintended stale result.

## Anti-patterns

Do not:

- overwrite originals;
- store thumbnails in `core.files`;
- store every thumbnail variant in SQL;
- make Media point to a thumbnail;
- chain repeated image edits from the previous lossy edit;
- put PostgreSQL/S3/Redis types in `ImageProcessor`;
- hardcode a concrete cache technology for thumbnails;
- download full-size images for tiny FileExplorer tiles;
- trust frontend crop data without bounds validation;
- expose unlimited public resize parameters;
- delete referenced file trees silently;
- implement final image mutation only in browser canvas code;
- leak third-party imaging-library types through GO CMS public contracts.
