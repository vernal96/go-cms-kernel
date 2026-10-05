---
name: go-cms-templating
description: Use for GO CMS reusable variable templating, interpolation, render contexts, variable schemas/resolvers and migration of feature-specific template engines such as SEO to shared kernel templating. Use when multiple modules need {{...}} placeholders without coupling to Mail/SEO/SMS.
---

# GO CMS Generic Templating

Follow root `AGENTS.md`. This skill defines a small reusable interpolation engine shared by feature modules.

## Ownership

Generic templating belongs in a neutral kernel package such as `kernel/templating`.

It must not import or know about:

- Mail;
- SEO;
- Forms;
- SMS/messengers;
- Resource/Site business types;
- filesystem/media storage;
- HTTP or frontend code.

Feature modules own their variable catalogs and resolvers. The templating package only compiles, validates and renders declared variables.

## Keep the language intentionally small

The first language is interpolation only:

```text
Hello, {{data.name}}
```

Do not add loops, conditionals, arbitrary functions, Go `text/template`/`html/template` execution, reflection-based property traversal, method calls or user-provided executable expressions.

Preserve the existing GO CMS variable naming style and compatibility with existing SEO placeholders where practical, e.g.:

```text
{{resource.title}}
{{resource.field.image}}
{{site.domain}}
{{site.field.company_name}}
{{data.email}}
```

Variable names are explicit, validated and allowlisted by the caller. Unknown variables are compile/validation errors.

## Compile then render

Prefer a compiled representation so malformed templates and unknown variables are rejected before repeated rendering.

The semantic API should support the equivalent of:

- compile source against an allowed variable set and limits;
- render a compiled template through an explicit resolver;
- return rendered output plus structured information about declared variables with no current value.

Do not silently reinterpret malformed delimiters.

## Missing values

A declared variable that has no current value renders as an empty value by default at the generic templating layer.

Rendering should still expose a structured missing-variable/warning list so previews and diagnostics can tell an administrator which values were empty.

Unknown variables are different from missing values:

- unknown/not declared -> validation error;
- declared but absent/empty at render time -> empty output + warning metadata.

Feature/domain layers may apply stricter semantics before calling the renderer. For example, Mail may mark a declared `data.*` field as required and reject a missing value before generic rendering. Do not put required/optional business policy into the generic templating engine.

## Values and conversion

The generic engine may support safe scalar conversion for common values such as:

- string;
- bool;
- signed/unsigned integers;
- floats;
- time values only through an explicit stable conversion policy if introduced.

Do not teach the generic engine how a `file.ID`, `media.ID`, resource field or site setting becomes a URL/string. Feature-specific resolvers own that conversion.

Avoid arbitrary `fmt.Sprint(any)` as the public conversion contract because it makes accidental structs/maps/slices render unpredictably.

## Render contexts

Rendering must distinguish output contexts when escaping/safety semantics differ. At minimum design for:

- plain text;
- HTML text insertion;
- message/header values.

For HTML interpolation, dynamic scalar values are HTML-escaped by default. Literal template markup remains literal.

For header-like output, reject CR/LF/control sequences that could enable header injection.

Do not create a single unescaped renderer and expect every caller to sanitize afterwards.

## Variable namespaces

Use explicit namespaces to prevent collisions and make ownership clear:

```text
site.*
resource.*
data.*
user.*
```

The templating package does not define which namespaces exist. Each caller supplies allowed names/resolution.

Mail-defined user inputs should normally live under `data.*`.

Profile-defined site fields use the established convention:

```text
site.field.<key>
```

Resource/template fields use the established convention:

```text
resource.field.<key>
```

Do not flatten dynamic profile/resource fields into `site.<key>` or `resource.<key>` because they may collide with stable built-in properties.

## Shared domain variable catalogs

When two or more features need the same domain namespace, do not duplicate hardcoded catalogs/resolution rules independently in every feature.

For example SEO and Mail both need site variables. A small core/site-owned helper may define the template-safe site variable catalog/value source, while remaining independent from feature packages.

A good boundary is:

```text
core/site
  knows Site + Profile.Params + typed Settings
  exposes template-safe variable metadata/raw values

Mail / SEO
  choose how typed values are converted for their context

kernel/templating
  knows none of Site/Mail/SEO
```

For site variables, keep stable built-ins separate from profile-defined fields. Typical template-safe built-ins include:

```text
site.id
site.profile_code
site.domain
site.locale
site.is_public
```

Profile parameters become:

```text
site.field.<Profile.Params key>
```

Do not expose audit/security/persistence internals such as created/updated actor IDs or file-reference bookkeeping merely because they exist on the Site struct.

Shared catalogs should preserve typed values. Do not pre-convert file/media profile fields to numeric strings; consuming features decide whether a typed file becomes a public URL, an attachment, or is invalid in the current context.

## Feature migration rule

When a feature already has a compatible local interpolation engine (currently SEO), move the reusable mechanics into `kernel/templating` and adapt the feature to use the shared package.

Do not leave two near-identical engines active after the migration merely to avoid touching existing tests.

Preserve SEO behavior unless the task explicitly changes its product semantics. Update SEO tests to prove compatibility of existing placeholders, missing-value warnings and limits.

When extracting shared site/resource variable catalogs from an existing feature, preserve established placeholder names such as `site.field.*` rather than creating Mail-specific aliases.

## Files/media are resolved outside templating

Generic templating must not open files or media.

A feature may resolve a file/media variable differently depending on context:

```text
Mail attachment -> actual file/spool reference/content
HTML body URL   -> appropriate accessible URL
SEO OG image    -> public/canonical media URL
```

Temporary Mail/Form spool objects are not template scalar values by default and their internal keys/paths must never leak through generic interpolation.

Keep those policies in the consuming feature/service.

## Limits

Keep explicit maximum template and rendered-output lengths. Callers may configure different limits for different fields/contexts.

Reject pathological input before allocating unbounded output.

## Testing invariants

Add focused tests for:

- literal-only templates;
- valid interpolation;
- unknown variable rejection;
- malformed/unclosed delimiters;
- declared missing value -> empty output + warning;
- scalar conversions;
- HTML escaping;
- header CR/LF rejection;
- source/result limits;
- deterministic rendering;
- migrated SEO output remains compatible;
- shared site variable catalog produces the same placeholder names for SEO and Mail where both expose them.

## Scope control

Do not turn this package into a CMS expression language or workflow engine.

The first goal is a small, safe, reusable primitive:

```text
Allowed variables + compile + explicit resolver + context-aware render + warnings
```
