# Entity lifecycle hooks

`entityhooks` owns registration, immutable dispatch scopes and delivery tracking.
Entity packages own keys, operation names, draft validation and event DTOs. The
kernel does not import the entities using this package.

## Site-owned entities

A contributor declares its dependency and registers during `Module.Build`:

```go
func (Module) Dependencies() []kernel.ModuleCode {
    return []kernel.ModuleCode{"core"}
}

func register(ctx kernel.ModuleContext) error {
    return entityhooks.RegisterBefore(ctx.EntityHooks(), resource.BeforeCreate,
        "default_title", func(ctx context.Context, change *resource.Change) error {
            change.Data.Title = strings.TrimSpace(change.Data.Title)
            return nil
        })
}
```

Use `RegisterAfter` with `resource.AfterCreate`, `AfterUpdate` or `AfterDelete`
for asynchronous reactions. Handler codes are stable identities, unique within
the contributor and scope. The owning module declares produced event names with
`EntityHookEventNames() []string`, including profiles that currently have no site.

Site registries are constructed independently and sealed after all modules have
built. Dependencies determine module order; registration order determines order
within a module. Missing dependencies, duplicate codes, wrong scopes, conflicting
payload types and late registration are errors.

A service owned by a module may retain `ctx.EntityHooks().Dispatcher()` for typed
dispatch. It has no registration capability. See the compilable catalog and
policy modules in `backend/examples/extensions/entity_hooks.go`.

## Application-owned entities

Users are global. Register user hooks through an application dependency provided
in `app.Definition.ModuleApplications`:

```go
func (Application) ModuleCode() kernel.ModuleCode { return "accounts_policy" }
func (Application) Dependencies() []kernel.ModuleCode { return []kernel.ModuleCode{"core"} }
func (Application) RegisterEntityHooks(ctx context.Context, r entityhooks.Registrar) error {
    return entityhooks.RegisterBefore(r, user.BeforeCreate, "default_name",
        func(ctx context.Context, change *user.Change) error {
            change.Data.Name = strings.TrimSpace(change.Data.Name)
            return nil
        })
}
```

Application contributors are registered once in dependency order. Building or
rebuilding site runtimes never repeats global registrations.

## Mutation contract

Core services create an operation-scoped context, pin the required immutable
registries through commit, and propagate it through repository decorators. This
context contains operation metadata and typed domain preparation policy, **not a
SQL transaction or service locator**. Adapters invoke the owner's preparation
function with the locked before image and candidate. Never retain this context
in a runtime, callback registration or background worker.

Before handlers run sequentially and can edit their draft or return a veto. The
domain validates the final draft and preserves identity, audit, authorization and
concurrency invariants. Read-only mutation metadata cannot be changed. Ordinary
editing cannot change widget identity or fields belonging to another operation.
Derived path/order changes of descendants and siblings can be vetoed; their
fields are controlled by the originating tree operation. Aggregate SQL drafts
remain private to the transaction; any veto rolls the entire operation back.

Before handlers should only prepare/validate the supplied draft and read their
explicit dependencies. Do not perform external effects or recursive business
writes from a before handler: they cannot participate in the owner's transaction.
Use an after handler for those reactions.

Resources cover tree and LibraryItem creation/editing, field values, widgets,
movement, transfer, trash, restore, revision restore and permanent deletion.
Trash is an update. Only permanent deletion produces `resource.deleted`.
Confirmed filesystem Media cascades also produce resource/user updates with
operation `media_cascade`. Their before-hooks can veto the whole cascade before
physical storage is touched; derived field/avatar clearing cannot be rewritten.
Resource versions and configured revisions, user audit metadata, outbox events
and recipients are persisted in the same deletion transaction.
User operations include profile/preferences/avatar/password, blocking and groups.
There is no user deletion operation. Login bookkeeping, password rehash during
authentication and raw seed/migration SQL are maintenance, not business hooks.

Event payloads contain explicit snapshots of the committed operation. User
password drafts exist only in before-create/change-password callbacks. Events
never include passwords or password hashes. A transfer records recipients in
both site scopes; later dispatch does not discover new recipients retroactively.

## Delivery and lifecycle

The adapter commits entity state, history, recipient rows and the outbox message
in the **same physical transaction**. PostgreSQL support uses module-owned
`entity_hook_calls` and `outbox_messages` tables. Each physical source has a stable
name and implements `entityhooks.Source`; expose it through `Provider`.

The application consumer uses the existing EventBus. A failed handler is retried;
completed recipients are skipped. Each attempt has a one-minute context and a
two-minute database lease. Handlers must honor cancellation. Use
`Delivery.IdempotencyKey()` when producing effects: a crash after the effect but
before recording completion can cause duplicate execution. Delivery is
**at-least-once**, with no global completion-order guarantee.

Pending recipients survive restart and block removal/profile deactivation of
their site. Startup fails if a saved recipient has no matching configured
handler. Failed transition preparation releases temporary drains and leaves the
old runtime active. Same-profile rebuilds may preserve pending work only when
all recipient identities remain available. These gates are process-local, like
the existing SiteRuntime publication mechanism.

Workers start after validation and stop before databases/EventBus close. Failures
are logged with source, event, module, handler and scope. Completed calls are kept
for seven days and cleaned hourly, at most ten batches of 500 rows per source.
Pending calls are never cleaned; an event with any pending recipient retains its
completed receipts as well.
