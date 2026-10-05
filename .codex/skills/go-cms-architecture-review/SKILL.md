---
name: go-cms-architecture-review
description: Use for reviewing GO CMS architecture, a cross-cutting refactor, commit/PR, or drift from project invariants. Not for normal implementation tasks.
---

# GO CMS Architecture Review

Follow root/backend instructions. Review evidence, not repository size.

## Scope efficiently

1. Start from the diff/changed files.
2. Read consumers of changed public contracts and the nearest tests.
3. Trace only the execution paths relevant to the change: boot/reload/request for runtime work, read/write/invalidate for cache work, registration/compile/dispatch for extension work.
4. Expand to unrelated packages only when the trace reaches them.
5. Use full architecture MCP only when a broad change cannot be evaluated from the diff and concrete dependency paths.

## Severity

- Critical: likely corruption/security/production failure or broken runtime publication.
- Major: breaks a core architecture invariant or expected extensibility.
- Medium: meaningful design debt likely to become costly as the subsystem grows.
- Minor: local quality issue without architectural impact.
- Acceptable trade-off: intentional deviation with contained cost.

For material findings give current behavior, impact, smallest coherent fix and the regression test that proves it.

## Checks

Prioritize:

- ownership: kernel vs module vs adapter/connector vs project/internal vs transport/frontend;
- dependency direction: generic/lower layers must not acquire project or optional-feature knowledge;
- scope: application/profile/site/module/request state is stored at the correct lifetime;
- runtime: no per-request SiteRuntime rebuild, mutable active-site singleton or partial publication;
- cache: every real mutation path preserves cached-read coherence and namespace correctness;
- `internal`: declarations do not repeat generic registration/lifecycle mechanics;
- HTTP: transport-specific state does not leak into core domain types;
- extension precedence: duplicates vs explicit deterministic overrides;
- module dependencies: explicit/deterministic, with `core` first and mandatory `admin` preserved.

Do not pad reviews with style comments while material architectural issues exist.

## Output

Start with a one-paragraph verdict. List findings in severity order with file/path evidence. End briefly with what should be preserved, recommended fix order and whether the code is safe to continue building on.
