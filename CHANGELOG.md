# Changelog

## Unreleased

- Return bare Font Awesome Solid icon names in template metadata, resource-tree items, and built-in resource-type defaults. Admin clients now compose the `fa-solid fa-` classes; consumers of the previous class-string API must update together with the admin.

## 0.6.0

- Include ordered template widget items in management `widget_areas` metadata. Static entries carry the site-compiled widget code; resource-widget slots retain their position. The admin can show preset widgets without exposing their configuration or changing public rendering.

## 0.5.0

- Treat template and resource icons as opaque class strings. Kernel does not select or validate an icon library; empty icon metadata uses the standard `fa-solid fa-file-lines` class.
- Document `ResourceSnapshot`, site-scoped `ResourceQuery`, recursive public site-settings projection, and readable server-side MIME restriction errors.

## 0.4.0

- Replace `ProfileModule` and untyped configuration with `[]kernel.Module` and module-specific constructors. Modules own immutable typed configuration; only modules using caches or filesystems expose their bindings.
- Add mandatory module declaration validation before site runtime construction. Invalid limits, bindings, adapters and application dependencies stop startup even for profiles without sites.
- Make module registry preparation return errors and keep configuration private. Update built-in modules and add a typed counter example.
- Mount public HTTP APIs under `/api`, retaining page/resource paths, `/_cms/files/...` delivery and `/healthz`. Old unprefixed and double-prefixed API paths are rejected.


- Make compiled validator scope explicit (`Scope() ValidatorScope`) for whole values and list items, including module contributions. Reject null required options and preserve integer membership precision across JSON round trips.
- Replace string field rules with typed, module-contributed validators compiled with site schemas. Add core validators, structured errors and admin metadata.
- Move list cardinality and phone pattern constraints from field options to validators. Forms development migrations and clients using `rules` must be updated; no compatibility alias is retained.

### Integration changes

- Declare `core.New(core.Config{...})`, `admin.New()`, `mail.New(mail.Config{...})`, `forms.New(forms.Config{...})`, `search.New()` and `seo.New(seo.Config{...})` in `[]kernel.Module`.
- Custom modules implement `Validate(context.Context, kernel.ModuleValidationContext) error`; registries implement `Registry() (kernel.ModuleRegistry, error)`. Keep declarations immutable and create separate site runtime state in `Build`.
- Replace `ModuleConfigFrom`/`RegistryForConfig` with typed configuration stored by the module. Move Core cache bindings into `core.Config.Caches` and Mail/Forms spool bindings into their `Config.Filesystems`.
- Update clients to the `/api` prefix and typed field validators. Recreate development data affected by corrected pre-production schemas; no compatibility shims are provided.

## 0.3.0

- Add `security.Authorizer.Allowed` for ordered, deduplicated batch permission checks. The PostgreSQL access adapter reads authorization facts in one statement; the access service retains permission policy and validates the catalog. Admin session and capability sets use batch checks.
- Read LibraryItem versions in the page query, joining after `LIMIT` while preserving custom-field sorting and cursor order.
- Load independent dashboard statistics concurrently in up to three tasks, preserving the sites-to-resources dependency and canceling remaining work on failure.

### Integration changes

- Custom authorizers must implement `Allowed(context.Context, security.Actor, []permission.Code) ([]permission.Code, error)`. Denied codes are omitted; authentication and operational failures remain errors. Empty input returns an empty result without I/O.
- Access repositories implement `Authorization` instead of `GroupAllowed` and `GuestAllowed`. Return the subject and requested group/guest grants together; policy remains in the service. `Subject` remains available for privileged and site-access checks.
- Statistics repositories must support concurrent calls and honor context cancellation. HTTP payloads and site-access rules are unchanged.

## 0.2.0

- Check authoritative site versions on each request; synchronize runtimes across replicas and reject stale snapshots with HTTP 503. PostgreSQL updates use optimistic concurrency.
- Add persisted, individually revocable JWT sessions. Password changes revoke all sessions and stale credentials cannot issue new sessions. Add POST /api/auth/logout.
- Bound login rate and expensive password/image work. Coalesce thumbnail rendering and cold cache loads.
- Capture cache dependency versions before loading; bound Redis generation metadata and invalidate reads if generation keys are evicted.
- Update golang.org/x/image to v0.43.0.

### Integration changes

- Configure JWT with WithSessions(application.Services().Sessions). Authentication issues actors with the user's session version.
- RememberJSONWithOptions takes dependency tags before the options callback; the callback controls TTL only.
- User Repository.RecordLogin receives the verified password hash to detect concurrent credential changes.
- Site records include Version; persistent adapters increment it on every update. VersionRepository must read authoritative storage for distributed runtime checks.
- Pre-production migrations were corrected directly. Recreate existing development databases before running 0.2.0; no conversion of old schemas is provided.
