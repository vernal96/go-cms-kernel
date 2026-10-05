---
name: go-cms-authorization
description: Use for GO CMS authorization architecture or implementation: users/groups/roles, site-scoped permissions, create/view/edit/delete rules, authorizer contracts, permission composition, module-contributed permissions, endpoint/service enforcement, admin visibility derived from permissions, or permission-cache invalidation. Do not use for ordinary authentication-only work with no permission semantics.
---

# GO CMS Authorization

Follow root `AGENTS.md`. Load `go-cms-api` when the task materially changes protected HTTP endpoints/contracts. Load `go-cms-cache` when permission caching/invalidation changes. Use `go-cms-requirements` first when the permission semantics themselves are not fully specified.

## Discovery

Before changing permissions, inspect:

1. current actor/authentication context;
2. authorizer/permission contracts and nearest service checks;
3. group/role persistence and site ownership model;
4. protected endpoints or admin metadata that consume the permission;
5. focused authorization tests;
6. cache/invalidation path if effective permissions are cached.

Do not infer the permission model from frontend button visibility alone.

## Core distinction

Keep these concepts separate:

```text
authentication -> who is the actor?
authorization  -> what may the actor do?
visibility     -> what should the UI show?
ownership/scope -> which site/resource/domain object is being acted on?
```

UI visibility may be derived from authorization, but hiding a menu item/button is never the security boundary.

Protected backend operations must enforce authorization independently.

## Site-scoped permissions

Treat site edit access separately from site publication/guest visibility.

A useful conceptual model for site management permissions is:

```text
view   -> site appears in management lists but restricted internals may remain inaccessible
edit   -> edit site settings/resources according to domain rules
delete -> permanently/soft delete site according to lifecycle rules
create -> create a new site; normally application/global capability rather than permission on one existing site
```

Do not automatically encode `create` as a row for each existing site if the domain meaning is global. Follow the current agreed model and use the requirements skill if this distinction is unresolved in a future task.

Permission implication (for example delete => edit => view) must be explicit policy, not an accidental UI assumption. Do not invent implication when the project expects independent flags.

## Enforcement boundary

Prefer authorization close enough to the application/domain operation that every transport path is covered, while retaining enough request/actor context to make the decision.

Avoid permission policy existing only in:

- HTTP handlers;
- Vue route guards;
- menu filtering;
- repository queries scattered across callers.

Handlers may invoke authorization and translate forbidden errors, but reusable operations should not become insecure when called from another transport/background process.

Choose one established project boundary (authorizer/application service/policy service) and keep checks consistent.

Do not pass a global service locator into domain code merely to obtain authorization.

## Permission identity and extensibility

Use stable semantic permission identifiers rather than frontend route names or concrete handler names.

Conceptually prefer:

```text
sites.view
sites.edit
sites.delete
sites.create
resources.view
resources.edit
```

Exact naming must follow existing project conventions.

If optional modules contribute permissions, use a generic extension/provider contract. Core/admin should not import every optional module merely to enumerate its permissions.

Permission registration must be deterministic. Duplicate codes are errors unless explicit replacement/extension semantics are intentionally designed.

## Groups and effective permissions

Group configuration is persistent policy; effective permissions are derived runtime/request data.

When an actor belongs to multiple groups, combination semantics must be explicit (for example union/allow unless explicit deny exists). Do not introduce deny precedence, inheritance or hierarchy unless requested and represented consistently in the model.

Keep global permissions, site-scoped permissions and resource/domain-specific permissions distinguishable. Do not overload one nullable `site_id` model until semantics become ambiguous.

If the system supports a superuser/admin bypass, keep it centralized and explicit rather than duplicating `if user.ID == ...` or role-name checks throughout the codebase.

## View permission semantics

For management lists, distinguish:

```text
can discover/list entity
can open/read management details
action permissions
```

If project policy says site `view` only exposes the site in a selector/list but not its tree/settings, model that deliberately rather than treating every GET as equivalent.

Frontend metadata should receive enough effective capabilities to render the correct controls without reimplementing group logic in JavaScript.

The backend remains authoritative for every corresponding operation.

## Resource/site ownership

Authorization checks must include the correct scope.

A permission for site A must never authorize mutation of an entity belonging to site B merely because the entity ID is valid.

For site-scoped operations, prefer authorizer inputs that make the scope explicit, conceptually:

```text
Authorize(actor, permission, SiteScope(siteID))
```

or the established equivalent.

Avoid loading an object globally, checking only a generic `resources.edit`, and forgetting its owning site.

## Permission cache coherence

If effective permissions or group/site grants are cached, authorization correctness requires immediate/defined invalidation after mutation.

Trace:

```text
cached permission read
 -> group/role/site permission mutation
 -> invalidate/update every affected dependency
 -> next authorization observes new policy
```

Use semantic dependency tags and the project cache model; do not rely on long TTL alone for mutable security policy.

Permission-cache failure policy must not accidentally grant access. When authoritative permission data cannot be established, fail closed for authorization decisions unless the project explicitly defines another safe behavior.

Use `go-cms-cache` when changing cache architecture or cross-store invalidation.

## Admin UI

Backend may expose effective capabilities such as:

```text
can_view
can_edit
can_delete
can_create
```

or stable permission codes/capability metadata according to the established API design.

Use them to hide/disable unavailable controls, but do not encode the actual permission-combination algorithm in the frontend.

Navigation filtering must remove inaccessible entries/empty groups where appropriate, but target endpoints still perform their own authorization.

## API behavior

Use the established distinction between authentication and authorization errors.

Do not leak sensitive existence information when API policy intentionally maps inaccessible objects to not-found. Apply that policy consistently rather than per-handler improvisation.

Bulk/list endpoints must enforce scope in the query/service path so unauthorized entities are not loaded and filtered only after the fact when avoidable.

## Tests

Prefer behavior-driven authorization tests:

```text
no grant -> operation denied
view grant -> expected list/discovery behavior only
edit grant -> resource/site mutation allowed within granted site
site A grant -> site B operation denied
frontend visibility capability matches backend decision
permission change -> cached decision becomes fresh
endpoint cannot bypass service/authorizer enforcement
multiple-group combination follows explicit policy
```

For security-sensitive changes, include at least one negative cross-scope test.

## Anti-patterns

Do not:

```text
frontend-only authorization
menu visibility as security
role-name/string checks scattered through handlers
site permission checked without target site ownership
permission cache with TTL-only stale security decisions
optional modules imported into core/admin solely to enumerate permissions
silent implicit permission inheritance
repository filtering as the only authorization mechanism when mutation still bypasses policy
allow access when effective permissions cannot be reliably established
```
