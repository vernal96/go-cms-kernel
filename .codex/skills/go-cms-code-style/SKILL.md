---
name: go-cms-code-style
description: Use whenever writing, refactoring, or reviewing Go code in the GO CMS project layer. Defines naming, package/file organization, imports, shadowing, public API readability, test doubles, formatting, and project-specific style rules.
---

# GO CMS Go Code Style

Follow root `AGENTS.md`. Optimize for readable call sites, explicit ownership, predictable package structure, and idiomatic Go.

## Formatting

- Run `gofmt` on every changed Go file.
- Prefer `goimports` when import cleanup is needed and available.
- Do not hand-format against `gofmt`.
- Keep formatting-only edits out of unrelated patches.

## Naming

Choose names with package and receiver context in mind.

- Do not repeat package names, receiver types, parameter names, or return types in function/method names unless needed for disambiguation.
- Prefer noun-like names for accessors that return a value.
- Avoid `Get` for ordinary accessors when the shorter form is clear: prefer `ProfileCode()` over `GetProfileCode()`.
- Prefer verb-like names for operations that perform work: `Load`, `Resolve`, `Register`, `Build`, `Store`, `Delete`.
- Add a type suffix only when it distinguishes real variants, such as `ParseInt` and `ParseInt64`.
- Preserve conventional initialisms: `ID`, `URL`, `HTTP`, `API`, `SQL`, `JSON`.
- Receiver names should be short and stable; do not use `this` or `self`.

Example:

```go
// Bad.
package yamlconfig

func ParseYAMLConfig(input string) (*Config, error)

// Good.
package yamlconfig

func Parse(input string) (*Config, error)
```

## Package names

Package names must describe a capability.

Avoid generic dumping grounds such as `util`, `utils`, `helper`, `helpers`, `common`, and `misc`.

If code has no clear owner, resolve the ownership problem instead of creating a generic package.

Package names should be short, lowercase, and readable at the call site.

## Shadowing

Be especially careful with `:=` inside nested scopes.

- Do not accidentally shadow `ctx`, `err`, transactions, repositories, or state whose new value must survive the block.
- Do not shadow imported package names with locals when the package is still needed.
- Reusing a variable is acceptable when the old value is intentionally no longer needed.
- If two values represent different concepts, give them different names.

Bad:

```go
if shortDeadline {
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()
}

// ctx here is the original context.
```

Better:

```go
if shortDeadline {
    var cancel context.CancelFunc
    ctx, cancel = context.WithTimeout(ctx, timeout)
    defer cancel()
}
```

## Package and file size

Go does not require one type per file.

- Group tightly related declarations and behavior together.
- Split files when navigation becomes difficult, not merely because another type appeared.
- Avoid both multi-thousand-line catch-all files and forests of tiny one-declaration files.
- Split packages by coherent responsibility and ownership, not arbitrary size thresholds.
- Keep related types in one package when they collaborate closely and sharing unexported implementation improves the public API.

Use `doc.go` only when substantial package-level documentation justifies it.

## Imports

Default grouping:

1. standard library;
2. non-standard imports.

Use additional groups only when they improve clarity consistently.

- Avoid aliases unless required by a collision or they materially improve readability.
- Blank imports must be intentional and their side effect should be obvious.
- Do not rename an import merely to free the package's natural name for a local variable.

## Public API

Export only what consumers need.

Read the complete call site, including the package name. Prefer concise names such as:

```go
cache.Store("resources")
runtime.Build(ctx)
resources.Resolve(ctx, site, path)
```

Avoid facade methods that merely repeat another object's API without adding ownership, lifecycle, policy, validation, or composition.

Do not introduce an interface only for symmetry. Add one when there is a real behavioral boundary, multiple implementations, useful test substitution, or dependency inversion.

## Test doubles

- A dedicated test-helper package may use the production package name plus `test`, for example `cachetest`.
- Prefer behavioral names such as `AlwaysFails`, `AlwaysAllows`, or `RecordingBus` when multiple doubles exist.
- Use `Stub`, `Fake`, `Spy`, and `Mock` accurately.
- Keep one-off helpers local instead of creating reusable helper packages prematurely.

## GO CMS-specific rules

- Prefer explicit constructors, factories, interfaces, registries, and manual dependency injection.
- Do not create generic `Manager`, `Service`, or `Helper` types unless the name represents a stable responsibility; prefer a more specific domain name when possible.
- Pass `context.Context` as the first parameter to blocking, I/O, request, and lifecycle operations; do not store request contexts in long-lived structs.
- Keep site-scoped state site-scoped; never shorten code by introducing mutable global state.
- Keep concrete infrastructure names out of core/domain APIs when the abstraction is about purpose rather than technology.
- Return explicit errors; do not hide failures behind silent fallbacks.
- Prefer the nearest established project pattern unless it conflicts with an explicit architecture invariant.

## Review checklist

1. Does the call site read naturally without duplicated package/type words?
2. Could a `Get` prefix be removed without losing meaning?
3. Does every package have a clear capability name instead of `util/common/helper`?
4. Did nested `:=` accidentally shadow `ctx`, `err`, or required outer state?
5. Are files grouped by related behavior rather than one-type-per-file dogma?
6. Are imports simple and predictable?
7. Is every exported symbol needed by a consumer?
8. Does the code preserve GO CMS ownership and runtime boundaries?
9. Was `gofmt` run?

## References

- Habr translation: https://habr.com/ru/companies/skillfactory/articles/729924/
- Google Go Style Best Practices: https://google.github.io/styleguide/go/best-practices
- Google Go Style Guide: https://google.github.io/styleguide/go/guide

Project-specific architecture rules in `AGENTS.md` take precedence over generic style guidance.
