---
name: go-cms-administration
description: Use for GO CMS global system administration capabilities reserved for the protected built-in admin group: administration navigation/pages, destructive maintenance actions, global cleanup/purge, system diagnostics/maintenance, backend membership enforcement, and matching admin frontend guards.
---

# GO CMS Administration

Follow root `AGENTS.md`. Load `go-cms-admin-ui` for backend-driven navigation/frontend plugin integration, `go-cms-authorization` for membership/security enforcement, `go-cms-api` for administration HTTP endpoints, and the relevant domain skill for the object being maintained (for example `go-cms-resource-revisions`).

## Purpose

`Administration` is a global system-management area, not a normal site-scoped feature page and not a collection of arbitrary buttons granted through ordinary business permissions.

Use it for dangerous or operationally global actions such as:

- purge all resource revision history;
- future cache/runtime maintenance;
- future system diagnostics/maintenance operations;
- other explicitly global CMS operations that should be restricted to the protected administrative group.

Do not move ordinary module settings or site-specific management into this area merely because they are advanced.

## Administrative identity

The project has a protected built-in administrative group identified by the stable group code `admin` (`group.AdminCode`).

When a requirement says an administration capability is available only to the administrative group, enforce membership in that exact protected group. Do not substitute generic `IsSuper`/privileged status unless the product requirement explicitly says every super group should gain access.

Avoid scattered string literals such as `"admin"` in handlers/components. Reuse the domain constant/contract and centralize the membership decision in an appropriate backend access/policy service.

System actors used internally may have an explicit trusted bypass according to existing backend conventions, but normal authenticated users must satisfy the admin-group rule.

## Security boundary

Use defense in depth:

```text
backend navigation visibility
frontend route access/UX guard
backend endpoint/application-service authorization
```

The backend operation is authoritative. Hiding `Administration` from navigation or guarding a Vue route is never sufficient security.

Direct API calls by non-admin users must return the established forbidden response even when they know the URL.

Do not expose a destructive global operation through an ordinary assignable permission if the resolved product rule says only the built-in `admin` group may perform it. Resource-local permissions may remain ordinary permission codes; global administration is a separate boundary.

## Navigation

`Administration` is a global top-level navigation item. It is not tied to the currently selected site runtime.

The backend should omit it entirely for actors who are not members of the protected admin group. Reuse the existing backend-driven navigation composition mechanism rather than hard-coding visibility solely in the frontend.

If the existing navigation item contract only supports ordinary permissions, extend the reusable visibility/access contract deliberately rather than smuggling an `admin` check into one Vue component. Keep the mechanism small and explicit; do not create a generic expression language for menu authorization.

The frontend route/plugin still needs a controlled unauthorized fallback/guard for direct navigation.

## API namespace

Global system operations belong under an explicit administration namespace, following established API conventions, for example:

```text
GET    /api/administration/resource-revisions
DELETE /api/administration/resource-revisions
```

Do not place a global purge under a site/resource endpoint and do not reuse `/api/admin` merely because the frontend application is called admin. `/api/admin` remains module/admin-specific where the current architecture uses it; administration endpoints are CMS management APIs with a stronger access boundary.

Application/service code must enforce admin-group membership independently of the handler.

## Destructive actions

Global destructive operations require an explicit confirmation UX.

For high-impact actions such as purging all revision history:

- show a clear description of what will be removed;
- show a count (and size if cheaply/accurately available) before deletion;
- state what will remain unaffected;
- require a deliberate confirmation, preferably typed text such as `DELETE` for irreversible global actions;
- disable duplicate submissions while the request is active;
- surface success/failure clearly.

Do not rely on a generic browser confirm dialog when the project already has admin UI components suitable for a deliberate destructive confirmation.

The backend must not trust the confirmation text as authorization. It is UX safety only; membership enforcement remains mandatory.

## Resource revision administration

For global revision maintenance, administration should expose aggregate metadata such as revision count and the global purge action.

Global purge:

```text
- deletes revision history only;
- does not delete or mutate current resources;
- does not reset Resource.Version;
- does not create a new resource revision;
- should later be emitted to a separate audit system when such a module exists.
```

Concrete PostgreSQL optimization such as `TRUNCATE` belongs inside the adapter/repository implementation, not in administration handlers/domain contracts.

Resource-local history purge remains on the resource history API and follows normal resource/site permissions; do not require the global admin group merely to clear one resource when the resolved permission model allows delegated resource-history deletion.

## Future extensibility

Keep the `Administration` page able to grow into multiple system sections/cards without turning it into a service locator or a dynamic plugin framework prematurely.

A simple first page can contain a `Resource history` section. Add generic contribution/provider machinery only when multiple independent modules genuinely need to contribute administration sections.

## Tests

Cover at least:

- protected `admin` group member sees the global navigation item;
- non-admin super/privileged group does not see it when product semantics require exact admin membership;
- ordinary user cannot call administration API directly;
- admin member can load administration statistics and execute the intended action;
- frontend direct route access is handled safely;
- destructive confirmation does not replace backend authorization;
- global operation preserves the domain invariants of the maintained subsystem.
