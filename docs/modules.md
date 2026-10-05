# Module declarations

A profile lists immutable module declarations implementing `kernel.Module`:

```go
Modules: []kernel.Module{
    core.New(core.Config{
        Caches: []cache.Binding{
            {Alias: core.HotCacheAlias, Code: "shared"},
        },
    }),
    admin.New(),
    seo.New(seo.Config{MaxTemplateLength: 2000}),
}
```

`core.New`, `mail.New`, `forms.New`, and `seo.New` accept their own `Config` types.
`admin.New()` and `search.New()` accept no arguments. Only Core exposes `Caches`;
Mail and Forms expose `Filesystems` for their `spool` aliases. Constructors copy
mutable configuration and perform no I/O. Database adapters and application
services are selected by the consuming application.

Go rejects a mismatched configuration type, an unknown field, and wrong argument
types or counts. Missing struct fields have zero values; value constraints,
required bindings, database interfaces and application dependencies are checked
before serving HTTP. Zero values retain each module's documented defaults;
Mail and Forms still require explicit delivery/public limits.

Profile compilation validates every module, even when there are no sites using
the profile. It checks bindings and calls `Validate` after the definition
registry has been assembled. Errors include the profile and module. The starter
propagates boot failure, closes opened resources, and exits before opening the
HTTP listener or starting workers. External I/O and site-specific data can still
fail during runtime; these cannot be checked by the Go compiler.

## Writing a module

A module implements:

```go
type Module interface {
    Code() kernel.ModuleCode
    Validate(context.Context, kernel.ModuleValidationContext) error
    Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error)
}
```

The constructor signature belongs to the module: see
[`counter.New(limit int)`](../examples/counter/counter.go). The example needs
neither caches nor filesystems. Keep declarations immutable and create fresh
site state in `Build`. Validate all site-independent requirements in `Validate`;
do not construct a fake site or a runtime for declaration validation.

`ModuleDatabaseFrom[T]` and `ModuleApplicationFrom[T]` accept both contexts. The
validation context limits database selection to the owning module, exposes
resolved cache/filesystem aliases, and provides the profile's definition
registry. `Disk(code)` checks an application disk explicitly named by the
module's configuration, such as Mail's upload destination.

Optional capabilities are `RegistryProvider` (`Registry() (ModuleRegistry,
error)`), `CacheBindingsProvider` (`CacheBindings() []cache.Binding`) and
`FilesystemBindingsProvider` (`FilesystemBindings() []filesystem.Binding`).
Return fresh mutable data from these methods. Registry declarations are derived
from the module's own typed configuration. Generic kernel code never interprets
module-specific configuration fields.

This API is included in v0.4.0. Upgrade the kernel dependency and module
declarations together; `ProfileModule` and untyped module configuration have
been removed.
