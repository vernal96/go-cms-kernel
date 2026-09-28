# Changelog

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
