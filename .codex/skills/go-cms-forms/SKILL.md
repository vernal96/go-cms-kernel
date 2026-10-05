---
name: go-cms-forms
description: Use for GO CMS Forms module architecture and implementation: site-owned forms, form fields/elements/layout trees, public submission, result/value persistence, statuses, extensible triggers/actions/executions, CAPTCHA/consent/upload fields, transient submission files, Mail integration, permissions, admin UI/API and runtime lifecycle.
---

# GO CMS Forms

Follow root `AGENTS.md`. Load `go-cms-api`, `go-cms-authorization`, `go-cms-events-jobs`, `go-cms-runtime-integrity`, `go-cms-mail` or `go-cms-admin-ui` only when the current task materially crosses those scopes.

## Module boundary

Forms is an optional feature module and owns the complete form-builder domain. Generic kernel/App must not gain Forms-specific fields, services or conditionals.

Forms owns:

- Form CRUD and site ownership;
- form field instances and Forms-specific field metadata;
- visual elements;
- ordered layout tree/containers;
- result/submission persistence;
- typed result values and upload metadata;
- form-owned business statuses;
- triggers/actions/action executions;
- Forms-specific field types such as CAPTCHA, consent and transient upload;
- public form schema/submission HTTP;
- Forms management HTTP, permissions and admin navigation/UI;
- Forms-owned temporary upload spool and cleanup;
- Forms action jobs/retries and runtime transition participation.

Forms does not own generic field types/validation mechanics, Mail delivery, generic Jobs/Outbox/EventBus, generic files, or generic runtime lifecycle mechanics.

## Site ownership

Every Form belongs to exactly one Site. Form Code is stable only within the Site:

```text
UNIQUE(site_id, code)
```

Different Sites may independently use the same Form Code.

All CRUD, public resolution, results, statuses and actions are site scoped. Never introduce a global FormByCode lookup without Site/SiteRuntime scope.

A Form belongs to Site, not Profile. Removing Forms from a Site profile keeps Forms business data. Re-adding Forms exposes the same forms/results again. Actual Site deletion may cascade-delete Site-owned Forms business data after runtime transition safety checks succeed.

## Core form model

Keep separate domain entities instead of one opaque JSON form document:

```text
Form
Field
Element
LayoutNode
Status
Result
ResultValue
Action
ActionExecution
```

Use relational identity, constraints and repository/service boundaries for lifecycle-sensitive entities. JSON is acceptable only for type-specific configuration where typed columns would not improve domain integrity.

## Form

A Form contains at least:

```text
ID
SiteID
Code
Name
Description
Enabled
CreatedAt/UpdatedAt
CreatedBy/UpdatedBy where current audit conventions apply
```

Code is integration/public identity; ID is persistence/admin identity.

Creating a Form atomically creates the required initial structure:

- one Forms consent field;
- one Forms CAPTCHA field;
- one submit-button element;
- layout nodes placing them in a valid order;
- one default business status `new` / `Новый`.

The backend, not only the frontend, preserves mandatory structural invariants.

## Form fields

Form fields reuse the existing `core/field` registry and `field.Definition` semantics for type, label, required, rules, options, editor and simple `VisibleWhen` metadata.

Do not invent duplicate email/phone/select/etc validation systems in Forms.

A FormField also owns Forms-specific result presentation metadata that must NOT be added to generic `field.Definition`:

```text
ResultLabel    // short table/detail label; fallback to Label when empty
ShowInResults // whether the default result table exposes the column
ResultPosition
```

Example:

```text
Label:       "Укажите ваш Email для обратной связи"
ResultLabel: "Email"
```

The form label is public presentation; ResultLabel is admin-result presentation.

Field Code is unique inside a Form and remains the stable semantic key used for submitted values, Mail action mappings and result snapshots.

The final runtime field registry is the source of available builder types. Do not replace it with a Forms-owned closed list. Persist type-specific options as JSON at the Forms HTTP/PostgreSQL boundary, reconstruct built-in typed options where required, and preserve contributed-module option objects for their own field compiler. A non-transient field is accepted only when its compiled value implements valid `field.StorageValueType` semantics; never stringify an unsupported value to force persistence.

## Forms-specific field types

Forms registers Forms-owned field types through the existing module field registry.

At minimum support:

```text
forms.captcha
forms.consent
forms.upload
```

Use actual repository naming conventions for TypeCode; keep codes stable and namespaced if the registry requires uniqueness.

### CAPTCHA

CAPTCHA is a transient validation field. Its submitted token/challenge response is validated by a Forms CAPTCHA verifier/provider and is NOT stored as ResultValue.

Keep CAPTCHA provider integration behind a Forms-owned contract/application dependency; do not put vendor knowledge in generic field/kernel code. A development/test verifier may exist, but production configuration must not silently pretend CAPTCHA verification succeeded when a real verifier is required.

### Consent

Consent is a persisted boolean/value field. Do not create policy text/version snapshots. Store the accepted value like another result value; Result creation time is sufficient for the submission timestamp.

### Upload

`forms.upload` means a file uploaded with the public submission. It is NOT `core.file` and must not create a permanent `core.files` row.

Validate configured MIME/size constraints, filename safety and request limits. Persist result metadata such as filename/MIME/size/checksum, not permanent bytes.

## Field validation and conditional visibility

Compile ordinary form fields through the existing `field.Schema`/field registry.

Forms owns submission-specific semantics on top:

- CAPTCHA verification;
- transient upload parsing/validation;
- consent semantics;
- effective requiredness for fields hidden by `VisibleWhen`.

Generic `field.Schema` does not currently interpret visibility. Do not globally change its semantics merely for Forms. Before final validation, Forms determines the effective active field set from the supported simple `VisibleWhen` contract. A hidden conditional field is not required and client-supplied values for inactive fields must be handled deterministically (prefer ignore/reject according to the established Forms API contract; never allow them to bypass visible-field validation).

Do not turn `VisibleWhen` into a general expression language.

## Elements

Elements are non-value visual entities, separate from fields.

Initial supported element types should include at least:

```text
text
heading
image
button/submit
```

Use a Forms-owned element type registry/config validation boundary so additional element types can be added without changing the Form aggregate schema.

A submit button is an Element, not a field and not a ResultValue.

## Layout tree

Represent form structure as an ordered tree of LayoutNodes, not duplicated nested JSON.

Conceptually:

```text
LayoutNode
  ID
  FormID
  ParentID nullable
  Kind: field | element | container
  FieldID nullable
  ElementID nullable
  ContainerType nullable
  Position
  Config
```

A node references exactly one compatible payload for its Kind.

Only container nodes may have children.

Initial container types should include at least:

```text
group
slide
```

The model must support future columns/sections/etc without creating a separate Quiz entity. A quiz is a Form whose layout uses slide containers.

Enforce:

- same-Form ownership of all references;
- no cycles;
- no self-parent;
- parent must be a container;
- deterministic sibling ordering;
- moving/reordering nodes transactionally;
- deleting a field/element also removes or rejects its layout references according to one explicit service rule;
- root/container structural validity.

## Required initial layout invariants

A valid Form has the required initial consent, CAPTCHA and submit button represented in the layout. Preserve the product invariant agreed for this project. Do not rely only on frontend controls to keep mandatory nodes present.

Exactly one active submit-button element should be present in the renderable layout unless a later requirement explicitly changes this rule.

## Public form schema

Forms exposes a public site-scoped read contract by Form Code for enabled Forms only.

Return only data required by a public renderer:

- stable Form code/name/description;
- fields and safe public options/rules/editor metadata;
- elements;
- ordered layout tree;
- no database/audit/internal action/status/admin configuration;
- no secrets, CAPTCHA server keys or spool details.

Public endpoints are contributed through the existing profile HTTP mechanism and therefore resolve against the current SiteRuntime/domain. Do not add Forms-special routing to the application root.

## Public submission

Public submit resolves an enabled Form by Site + Code and accepts ordinary values plus multipart upload parts where applicable.

Submission pipeline:

```text
resolve current site-owned Form
-> apply request/body/file limits
-> parse values/uploads
-> evaluate conditional visibility
-> validate field schema
-> verify CAPTCHA
-> validate consent and Forms-owned field semantics
-> stage transient uploads in private Forms spool
-> create Result + typed ResultValues + pending ActionExecutions + durable action Jobs/Outbox atomically where possible
-> return a minimal success response
```

Never trust client-supplied FormID/SiteID/status/action configuration.

Do not expose internal numeric IDs in the public response unless there is a concrete public use case. Prefer an opaque/public result reference if a reference must be returned.

Validation errors are safe, structured and keyed by Form field Code.

## Public anti-abuse and limits

Forms is a public mutation surface. Apply explicit bounded policies from the first version:

- maximum request/body size;
- maximum upload size/count;
- allowed MIME constraints;
- request/read timeout according to server conventions;
- configurable rate limiting at an appropriate site/form/client scope;
- CAPTCHA verification;
- safe errors without internals.

A honeypot may be an additional anti-bot feature but is not a substitute for server-side limits/CAPTCHA when CAPTCHA is configured.

Do not embed a specific third-party CAPTCHA vendor into generic kernel code.

## Results

A Result is the accepted request/submission and belongs to Site + Form.

Persist at least:

```text
ID
SiteID
FormID
FormCode snapshot
FormName snapshot
StatusID
UserID nullable when authenticated
UserAgent metadata
client/network metadata only according to configured retention/privacy policy
CreatedAt
UpdatedAt
```

User identity is backend-derived from the authenticated request actor when present; never trust user ID posted by the browser.

Result business status is separate from action execution status.

## Result values

Persist one logical ResultValue per submitted persisted field value, using adapter-neutral typed storage semantics rather than stringifying everything.

Keep enough field snapshots for historical readability after Form edits:

```text
FieldID where useful
FieldCode
FieldLabel
ResultLabel
FieldType
Value / typed storage metadata
```

Do not snapshot consent policy text/version.

CAPTCHA token is never a ResultValue.

Upload ResultValue stores only safe metadata/reference to the logical submission upload, not permanent bytes or an internal public spool key.

Historical Results must remain understandable after field labels/codes/config are edited or fields are removed.

## Result table metadata

`ResultLabel`, `ShowInResults` and `ResultPosition` belong to FormField configuration.

Default result-list columns use enabled `ShowInResults` fields ordered by ResultPosition and fall back from ResultLabel to Label. Do not automatically render an unbounded number of columns without UI controls; backend still returns bounded result pages and safe summary projections.

Result detail may show all persisted values independent of ShowInResults.

## Statuses

Statuses belong to a Form, not globally.

Status contains at least:

```text
ID
FormID
Code
Name
Color/presentation metadata
Position
IsDefault
```

A Form has exactly one default status. New Results receive it atomically.

Status is business workflow state such as `new`, `in_progress`, `processed`, `spam`. Do not use Result.Status to represent technical Mail/CRM action delivery outcomes.

Admin may CRUD/reorder statuses subject to invariants. Prevent deleting the current/default status in a way that leaves existing Results invalid; use an explicit replacement/archive rule if deletion is supported.

## Triggers, Actions and ActionExecutions

Separate:

```text
Trigger         = what happened
Action          = configured reaction
ActionExecution = state of that action for one Result
```

Initial trigger support:

```text
submitted
status_changed
```

A status_changed action may optionally constrain from/to status codes.

An Action belongs to a Form and stores:

```text
ID
FormID
Code/name
Enabled
Trigger
ActionType
validated type-specific Config
Position
```

An ActionExecution belongs to Result + Action and stores at least:

```text
ID
ResultID
ActionID
ActionType snapshot
status: pending | running | retryable | succeeded | failed
attempt count
timestamps
safe error
optional external reference
```

ActionExecution technical status never overwrites Result business status.

## Extensible action registry

Forms owns an ActionType registry. Do not add Forms action types to generic kernel registries.

An action type contract should conceptually provide:

```go
Code()
Metadata() // human/admin metadata
ValidateConfig(...)
Execute(ctx, ActionContext, config) (Result, error)
```

Use concrete typed structs/interfaces where practical; avoid unrestricted `any` service locators.

Forms includes the built-in Mail action for v1 and therefore explicitly depends on Core + Mail in profiles using the complete Forms module.

The Mail action references a MailTemplate by stable `template_code`, never by DB ID, and resolves the site-scoped Mail service from the same SiteRuntime/module dependency.

Other modules may contribute Forms action types by explicitly depending on Forms and registering an ActionType through a narrow Forms registration interface during SiteRuntime/module build. Registration is site-runtime scoped, duplicate action codes are errors, and runtime use must observe a stable/sealed registry after build. Do not make Forms import or enumerate concrete contributor modules.

Contributor modules register during their `Build`; Forms seals the per-site registry from `RuntimeBuildFinalizer` only after every profile module has built. Late registration is an error. Persisted actions whose contributor later disappears stay stored and are exposed to admin metadata as unavailable rather than breaking runtime construction.

Do not use mutable global action registries.

## Mail action

Mail action configuration includes at minimum:

```text
template_code
value mapping from Mail data variable -> Form field/result value
attachment mapping from compatible forms.upload fields
```

Validate configuration in admin against the current SiteRuntime Mail service/template metadata where a stable public validation API exists. At minimum validate structural mapping and fail clearly at execution if the referenced template/current runtime is invalid.

Execution calls the SAME SiteRuntime's site-scoped Mail service:

```text
Mail.QueueByCode(template_code, values, transient attachments, origin)
```

Set origin metadata such as:

```text
Source = forms
Event = submitted | status_changed
Reference = stable result reference/ID
```

Forms must not know SMTP, Mail spool keys, Mail worker internals or transport credentials.

Mail ActionExecution `succeeded` means the handler successfully handed the immutable message to Mail's durable queue. Actual SMTP delivery remains authoritative in Mail Message/DeliveryAttempt history; do not falsely label queued Mail as mailbox-delivered.

Store the queued Mail Message reference as safe execution external metadata when useful for admin linkage.

## Action jobs and outbox

Actions execute asynchronously.

When accepting a submission or committing a status change that fires actions, persist the Result/status mutation, ActionExecution rows and durable action-job Outbox messages in the same physical DB transaction.

Use one small scoped job per ActionExecution, normally containing only its execution ID plus standard job envelope metadata.

Worker flow:

```text
load execution/result/action snapshot/config
-> claim execution idempotently
-> execute registered handler
-> persist succeeded/retryable/failed
-> retry according to bounded policy
```

Treat broker delivery as at-least-once. Claim/state transitions must prevent duplicate side effects as far as the downstream integration permits.

Do not hold DB transactions across Mail queueing, HTTP calls or other external action I/O.

Define explicit retry classification. Unknown/missing action type for a still-current runtime is a configuration/domain error, not infinite retry.

Late scoped jobs for a deleted Site or a current profile intentionally lacking Forms use the existing generic obsolete-job semantics and must not retry forever.

## Forms transient upload spool

Because Actions are asynchronous, public upload bytes must outlive the request. Forms therefore owns a private temporary submission spool distinct from Mail spool and `core.File`.

Conceptual flow:

```text
multipart upload
-> Forms private spool
-> Result stores safe upload metadata
-> Forms action job opens fresh stream(s)
-> Mail action passes stream(s) to Mail.QueueByCode
-> Mail copies to its own spool before queuing Mail Message
-> Forms keeps its copy until every ActionExecution that may need it is terminal
-> Forms deletes result spool bytes
```

Requirements:

- site-scoped prefix/namespace;
- no FileExplorer visibility;
- no `core.files` row;
- no browser-accepted internal spool key;
- private logical filesystem binding via ModuleContext;
- immutable filename/MIME/size/checksum metadata;
- fresh opener/reader for each action execution;
- bounded TTL orphan cleanup;
- cleanup after all relevant executions become terminal;
- best-effort rollback cleanup when Result/Outbox persistence fails;
- no large binary bodies in PostgreSQL or job payloads.

## Runtime transition lifecycle

Forms participates in generic runtime transition for profile changes and Site deletion when pending asynchronous work/external spool exists.

Use a process-local draining gate analogous to Mail where appropriate:

```text
enter draining
-> reject new public submissions/status-triggered action creation
-> prevent worker from claiming new action execution
-> check pending/running/retryable executions for this Site
-> if active: abort drain and block profile change/site delete
-> if inactive: purge only this Site's Forms spool
-> allow transition
```

A failed Site mutation aborts draining and restores the old runtime.

Same-profile ordinary Site settings updates do not unnecessarily drain Forms.

Removing Forms from Profile keeps Forms business data/results/statuses/actions. Actual Site deletion may cascade-delete them only after active work is absent and Forms spool cleanup succeeds.

## Admin permissions

Register Forms permissions using current module/entity/action conventions. At minimum separate sensible entities such as:

```text
form
result
status/action configuration where current permission granularity justifies it
```

Backend site-scoped authorization is authoritative. Navigation visibility is only presentation.

Users who can edit Forms do not automatically gain unrelated Core File or Mail template permissions unless an operation genuinely needs them.

## Management API

Use the generic SiteManagementContribution under a normalized Forms path, e.g. `/api/sites/{siteID}/forms/...` according to current routing conventions.

Provide bounded CRUD/search/pagination endpoints required by admin UI for:

- forms;
- fields;
- elements;
- layout/move/reorder;
- statuses;
- actions;
- results list/detail;
- result status change;
- action execution detail/retry when supported.

Prefer aggregate/batch mutation endpoints for layout reorder when many individual requests would create inconsistent intermediate trees.

Validate cross-site/cross-form IDs on backend; never infer safety from URLs alone.

## Admin UI

Forms contributes backend-driven site-scoped navigation such as:

```text
Формы
  Формы
  Результаты
```

Admin UI includes:

- form list/create/edit/enable-disable;
- form builder for Fields + Elements + Layout tree;
- drag/move/reorder using backend-safe mutations;
- field configuration using existing generic field editor components where possible;
- Forms-specific editors for CAPTCHA/consent/upload;
- ResultLabel/ShowInResults/ResultPosition controls;
- status configuration;
- action configuration;
- Mail action template selection/mapping;
- result list with configured result columns and server pagination/filters;
- result detail with all values and action execution state;
- result status change.

Do not make frontend authoritative for validation, layout invariants, action availability or permissions.

## Public/admin separation

Public form endpoints expose only enabled public form schema/submission behavior. Management APIs require normal authentication/site access/Forms permissions.

Never expose Action configs, internal status IDs, audit internals, spool keys, CAPTCHA secrets, Mail template internals or action safe-error diagnostics through public schema.

## Persistence and migrations

Forms owns its repository contract and module-specific adapters/migrations following the current Mail/Core patterns.

For PostgreSQL, use a dedicated `forms` schema and relational foreign keys/site ownership. Because the project is pre-production, prefer the clean current schema over compatibility shims.

Keep repository queries site scoped and bounded. Add indexes for actual lookup/list/claim patterns such as site+code, form children/order, results pagination/status/date, and pending action execution claims.

## Deletion semantics

Prefer enable/disable/archive for Forms referenced externally by stable Code. If hard delete exists, enforce safe cascades/cleanup and preserve no orphan layout/status/action rows.

Deleting a Form deletes its configuration and owned Results only when that is the explicit product operation; do not accidentally delete Result history when merely editing fields/layout/statuses.

Profile removal never deletes Forms business data.

## Testing priorities

High-value tests include:

- same Form code on two Sites; duplicate on one Site rejected;
- public lookup cannot cross Sites and disabled Form is not submittable;
- create Form atomically creates consent, CAPTCHA, submit button, layout and default status;
- field schema validation and structured field errors;
- hidden required `VisibleWhen` field does not incorrectly block submit;
- CAPTCHA token validated but never persisted;
- consent value persisted with no policy snapshot;
- forms.upload never creates core.File;
- upload MIME/size constraints and traversal-safe filenames;
- layout cycle/self-parent/cross-form references rejected;
- deterministic move/reorder and container-only children;
- ResultLabel fallback, ShowInResults and ResultPosition behavior;
- Result historical snapshots survive field edits/deletion;
- result business status independent from ActionExecution status;
- submitted/status_changed triggers create the correct executions exactly once per committed transition;
- action job/outbox atomic with Result or status mutation;
- duplicate job delivery does not repeat a locally completed execution;
- Mail action maps values/uploads and calls same-site QueueByCode;
- Mail action cannot resolve another Site's template;
- Forms spool survives async delay/retry and is deleted only after relevant actions become terminal;
- Site A spool/cleanup cannot affect Site B;
- profile change/Site delete blocked while Forms has pending/running/retryable work;
- abort restores submission/claim availability;
- profile removal preserves Forms data; Site deletion removes Site-owned data;
- custom module action registration works, duplicate action codes fail, registry is stable after runtime build;
- public request/body/rate limits behave safely;
- permissions and site access enforced on every management path.

## Scope discipline

Do not introduce unless explicitly requested:

- full workflow/BPM engine;
- arbitrary scripting/expression execution;
- Form version history/revisions;
- consent-policy version snapshots;
- permanent storage of public upload bytes as core.Files;
- CAPTCHA vendor logic in generic kernel;
- global mutable Forms/action registries;
- direct Forms knowledge of SMTP/CRM implementation details;
- distributed locks merely to replace current process-local SiteRuntime assumptions.
