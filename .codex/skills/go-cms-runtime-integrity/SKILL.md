---
name: go-cms-runtime-integrity
description: Use only for changes involving SiteRuntime, ProfileBlueprint, ModuleContext/RuntimeScope, site runtime rebuild/publication, runtime-scoped bindings/HTTP artifacts, or cache coherence between site reads and management writes.
---

# GO CMS Runtime Integrity

Follow root/backend instructions. Inspect only the affected runtime builder/catalog, its caller, relevant module runtime and focused tests before expanding scope.

## Runtime model

Preserve:

```text
Profile declaration
  -> immutable ProfileBlueprint
    -> per-site SiteRuntime
      -> per-site RuntimeRegistry
      -> per-site ModuleRuntime instances
      -> per-site bindings/artifacts where required
```

Site runtimes are built on boot/create/update/reload and kept in process memory. Never rebuild a full runtime per request or store runtime objects in Redis/external cache as a substitute for process state.

## Runtime scope

When module `Build` needs site data, expose the minimum stable data through generic kernel-owned runtime scope contracts.

- Do not make generic kernel import core site domain types.
- Defensively copy mutable maps/slices exposed as runtime configuration.
- Do not store request actor/context in long-lived runtime scope.
- Keep module dependency lookup explicit and separate from runtime scope.

## Atomic publication

Prepare and validate the complete candidate state before publishing it. If preparation fails, readers continue using the previous complete runtime/HTTP state. Avoid mutating already-published runtime objects during a candidate build.

HTTP artifacts may be compiled once per site, but transport/server code should own transport-specific handler state. Runtime and its required HTTP artifact must switch coherently.

When later profile modules explicitly contribute into an earlier dependency during `Build`, keep that registry site-scoped and unpublished. A generic `RuntimeBuildFinalizer` may seal/validate it only after all modules have built; invoke finalizers deterministically in profile order and fail the candidate runtime before publication if any finalizer fails. Do not use a global mutable registry or seal the dependency before its declared contributors run.

## Runtime deactivation transitions

Runtime-owned work that must be drained or cleaned before a SiteRuntime is removed participates through the generic kernel runtime-transition capability. Keep feature-specific behavior out of the Site catalog.

Preserve transaction-like semantics:

```text
prepare participants in deterministic runtime/module order
  -> participant may enter a temporary draining state
  -> perform fallible validation and safe ephemeral cleanup
  -> prepare detached catalog/HTTP/background state
  -> persist the Site mutation
  -> publish/finalize every preparation

any later preparation or repository mutation fails
  -> abort prepared participants in reverse order
  -> restore the old published runtime's temporary state
```

- Transition preparation must finish before the Site repository update/delete.
- A preparation that mutates temporary state must expose both non-failing commit and idempotent abort behavior.
- If one participant fails during its own prepare, it must undo its partial temporary state before returning the error.
- Later participant/preparer failure and repository mutation failure abort all earlier preparations.
- Derive reasons from semantic Site changes. Site deletion and profile-code change are deactivation transitions; an ordinary same-profile settings update is not.
- Profile changes invoke participants even when both profiles contain the same optional module because their runtime infrastructure/configuration may differ.
- Existing snapshot publication remains atomic for request handlers and background-task owners. Publish replacements only after preparation and persistence succeed.
- Runtime draining is process-local while SiteRuntime publication is process-local. Do not imply distributed exclusion; introduce distributed coordination only as a separate explicit architecture decision.

## Cache coherence

For every affected cached mutable entity trace:

```text
read -> populate cache -> real mutation path -> invalidate/update -> read fresh value
```

A cached runtime reader plus an uncached admin writer is a correctness bug unless a centralized invalidation bridge covers the writer. Invalidation is domain/persistence policy, not an HTTP caller responsibility.

When site namespaces are used, verify both isolation and the ability of application/admin mutations to invalidate every affected scope.

## Dependency/build failures

Runtime construction must fail clearly for missing/undeclared/self/duplicate dependencies and invalid ordering. `core` remains required and first; `admin` remains required.

## Tests

Choose tests matching the changed invariant rather than running every runtime scenario. Common high-value cases:

- two sites receive correct distinct final runtimes;
- runtime scope is propagated without retaining request state;
- failed rebuild leaves the previous snapshot active;
- cached read becomes fresh after real service/admin mutation;
- site cache namespaces do not collide;
- precompiled HTTP/runtime state switches atomically.

Run focused packages while iterating; run broad backend checks once near completion only when the refactor crosses package boundaries.
