---
name: go-cms-cache
description: Use for GO CMS cache architecture or implementation: cache.Store/Manager, module cache aliases/bindings, Redis/filesystem/memory cache connectors, TTL, keys, tags, dependency invalidation, cache coherence, Remember/result caching, pruning/clearing, observability, or tests involving cached reads and writes.
---

# GO CMS Cache

Follow root `AGENTS.md`. This skill defines the cache model and implementation rules for GO CMS.

## Mental model

Keep four concerns separate:

- **Store** = physical cache infrastructure and driver behavior (`redis`, filesystem, memory, database, null, etc.).
- **Alias** = a module-local storage policy/capability requested by module code (`hot`, `durable`, or another module-defined name).
- **Key** = identity of the cached value (`user:1`, `user:1:status`, `resource:path:<hash>`).
- **Tag** = dependency that makes cached values stale when it changes (`user:1`, `site:7`, `resource:42`).

Do not collapse these concepts into one naming scheme.

A single domain component may use multiple aliases/stores at the same time. Example:

```text
core module
  durable -> filesystem
    user:1

  hot -> redis
    user:1:status
```

The module must not know that `durable` is filesystem or that `hot` is Redis. Project composition selects the concrete stores.

## Ownership and boundaries

### Application layer

`App`/application composition owns physical cache stores and their lifecycle.

```text
App CacheManager
  filesystem_cache -> filesystem connector
  redis_cache      -> Redis connector
  memory_cache     -> memory connector
```

Physical store codes may describe infrastructure because they exist at the application composition boundary.

### Module layer

A module sees only cache aliases explicitly bound to it through its module-scoped cache manager.

```text
ProfileModule binding
  alias "durable" -> physical store "filesystem_cache"
  alias "hot"     -> physical store "redis_cache"
```

Module code uses:

```go
store, ok := ctx.Caches().Store(alias)
```

Never resolve an application store by technology name from module/domain code.

Aliases describe the storage characteristics the module relies on. They are not required to correspond one-to-one with domain entities. Avoid proliferating aliases such as `users`, `resources`, `seo`, etc. merely because those entities exist if the actual requirement is a shared storage policy.

## Keys

Keys describe **what value is cached**, not where it is stored.

Good examples:

```text
user:1
user:1:status
users:list:page:1
resource:id:42
resource:path:<hash>
```

The same logical key may exist in different stores; `(store + module/site namespace + key)` identifies the physical cache entry.

Keep key formats deterministic and version them when serialization/meaning changes.

Do not include concrete technology names in keys.

## Namespaces

Module/site scoping of cache keys is required so sites and modules cannot collide accidentally.

Keep **key isolation** separate from **dependency identity**. Do not blindly prepend a module-local key namespace to dependency tags when that would prevent cross-alias or cross-module invalidation.

A dependency such as `resource:42` may affect cached values owned by several modules and several stores.

## Tags and dependency invalidation

Tags describe **what cached data depends on**.

Example:

```text
durable / user:1
  tags: user:1, users

hot / user:1:status
  tags: user:1, user:1:status
```

Changing the user may invalidate `user:1` and therefore all dependent entries. Changing only presence/status may invalidate only `user:1:status`.

Prefer hierarchical/semantic tags where useful:

```text
site:7
site:7:resources
site:7:resource:42
site:7:resource:42:widgets
user:15
user:15:permissions
```

Do not encode storage technology in tags.

### Cross-store correctness

A dependency can span several aliases and physical stores. Invalidation must therefore be able to reach every participating cache target without giving one module unrestricted access to other modules' stores.

Use a generic application/runtime-owned invalidation/coherence mechanism (coordinator/policy/registry) that tracks relevant cache targets and fans dependency invalidation out to them.

Do not solve cross-store invalidation by:
- exposing the global application cache manager directly to modules;
- making module code iterate Redis/filesystem/memory stores;
- hardcoding other modules' aliases;
- duplicating invalidation logic in each controller/handler.

Keep mutation-triggered invalidation close to the persistence/domain cache policy so every write path preserves coherence.

## TTL and tags

Use TTL and tags together when data has semantic dependencies:

- **tags** are the primary correctness mechanism;
- **TTL** is the safety bound and natural expiry mechanism.

TTL alone is acceptable for data whose staleness is inherently time-based and does not require mutation-triggered invalidation.

Never rely on an extremely long TTL as a substitute for correct dependency invalidation.

## Read-through / Remember

Avoid repeating manual `Get -> decode -> load -> encode -> Set` logic throughout modules.

Prefer a reusable typed read-through helper such as:

```go
value, err := cache.Remember(
    ctx,
    store,
    key,
    options,
    loader,
)
```

The exact API should fit Go's type system and the existing package style. A generic function `Remember[T]` is acceptable when it reduces duplication without hiding important behavior.

Expected semantics:

1. read cache;
2. on valid hit, decode and return;
3. on miss/corrupt entry, call loader;
4. if loader succeeds, write the result with TTL/tags;
5. return the loaded value;
6. do not cache failed loader results unless negative caching is explicitly designed.

Preserve explicit serialization/versioning behavior and focused tests.

## Repository cache vs result cache

Use cache at the narrowest useful level, but do not restrict caching to repositories.

### Repository cache

Appropriate for reusable data reads such as:

```text
resource by ID/path
site by domain
user by ID
permissions
```

### Result cache

Appropriate for an expensive assembled result, analogous to component-result caching rather than HTML caching:

```text
resource API view
menu
resource tree
widget collection
SEO result
admin navigation
```

GO CMS is API-first. Cache assembled data/results, not server-rendered HTML unless a future transport explicitly requires it.

A result entry should carry every dependency tag required to make the assembled result stale correctly.

## Failure policy and observability

Cache is an optimization, not the source of truth.

For ordinary cache read/write failures, prefer fail-open behavior where the domain operation can safely continue from the authoritative source. However, failures must not disappear silently.

Record/log/measure at least:
- hit;
- miss;
- read/decode error;
- write error;
- invalidation error;
- invalidation count/target where useful.

Do not turn a temporary Redis/filesystem outage into a domain outage unless the specific cache use case is explicitly correctness-critical.

Do not swallow errors silently just because the request continues.

## Filesystem cache maintenance

Token/version-based tag invalidation may make entries logically stale without physically deleting every file immediately.

Design maintenance separately from hot-path invalidation. When needed, provide capabilities such as:

```text
cache prune
cache clear
```

Filesystem cache should be pruneable for expired/orphaned/stale entries without requiring synchronous scans during domain writes.

Redis TTL can perform natural physical cleanup; do not assume every store has equivalent maintenance behavior.

## Existing GO CMS behavior to preserve

- `cache.Store` is the low-level contract.
- `cache.Manager` owns application physical stores.
- module-scoped managers expose only explicit aliases/bindings.
- Redis and filesystem stores already support TTL and token-based tag invalidation.
- cache coherence around repository writes is a correctness concern, not optional cleanup.

Extend these mechanisms rather than creating a parallel `ManagedCache`/`TaggedCache` hierarchy.

## Implementation workflow

For cache changes:

1. inspect `github.com/vernal96/go-cms-kernel/cache` contracts/managers in the
   kernel checkout first;
2. inspect the affected physical connector(s) only if store semantics change;
3. inspect module bindings/runtime assembly;
4. inspect one actual cached read and every mutation path that can stale it;
5. define dependency tags before adding invalidation code;
6. add focused behavioral tests;
7. only then broaden to integration/repository-wide validation.

When changing cache semantics, explicitly answer:

```text
What is the store?
What is the alias?
What is the key?
What are the tags?
What mutation invalidates them?
Which aliases/stores must receive that invalidation?
What is the TTL?
What happens when cache infrastructure fails?
```

## Required tests

Prefer behavior-driven cache tests such as:

```text
miss -> authoritative load -> cached hit
cached read -> write -> next read observes fresh value
same dependency cached in two aliases/stores -> one mutation invalidates both
site A invalidation does not incorrectly invalidate unrelated site B data
TTL expiry produces miss
corrupt payload/tag token produces safe miss/recovery
cache backend failure does not silently corrupt domain behavior
```

For cross-store changes, include at least one test proving invalidation crosses the intended alias/store boundary.

## Anti-patterns

Do not:

```text
Store("redis") from module code
Store("filesystem") from module code
one alias per entity by default
TTL-only coherence for mutable domain data
module-local tag namespacing that blocks legitimate cross-module dependencies
global mutable site cache singleton
controller-only invalidation
silent cache errors with no observability
full filesystem scans on every invalidation
```

Prefer explicit policy, deterministic keys, semantic dependencies, site/module isolation where appropriate, and cross-store invalidation where correctness requires it.
