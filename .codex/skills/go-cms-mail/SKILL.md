---
name: go-cms-mail
description: Use for GO CMS mail templates, reusable templating integration, asynchronous mail jobs, SMTP/null/log transports, previews/manual sending, delivery attempts/history, persistent and transient attachments, permissions, site variables and site-scoped mail administration.
---

# GO CMS Mail

Follow root `AGENTS.md`, `go-cms-templating`, `go-cms-events-jobs`, `go-cms-authorization` and `go-cms-api` where applicable.

## Module boundary

Mail is an optional feature module, not core infrastructure and not an SMTP wrapper embedded in `kernel/app`.

Mail owns:

- mail template CRUD and validation;
- Mail-specific variable schema/resolution;
- preview/render orchestration;
- immutable rendered messages queued for delivery;
- Mail send job handler;
- transport contracts/manager;
- delivery attempts/status history;
- persistent/transient attachment orchestration;
- mail permissions and admin UI/API contributions.

Mail does not own the generic template language. Use `kernel/templating`.

Generic `kernel/app` must not import `modules/mail`, store `*mail.Management`, expose `MailManagement()` or gain one feature-specific field/method per optional module. Optional management HTTP is wired through a generic contribution/composition boundary described by `go-cms-api`.

## Site scope

MailTemplate belongs to exactly one Site and never exists globally. Keep `Template.SiteID` and relational ownership through `mail.templates.site_id -> core.sites(id)`. Template Code is unique only within its Site (`UNIQUE(site_id, code)`), so different Sites may independently use the same Code.

Use the existing SiteRuntime/module wiring and site-scoped authorization patterns. Never create a mutable global active site/mail instance.

Template lookup is always site/runtime scoped, semantically `TemplateByCode(ctx, siteID, code)`. Never introduce a global code lookup. Manual APIs do not accept authoritative template `site_id`, and cross-site template IDs are rejected.

All `site.*` values used to validate/render a template come from the current SiteRuntime that owns `Template.SiteID`. Never accept a second arbitrary rendering Site ID. Future Forms integration resolves Mail from the same Form SiteRuntime and calls that site-scoped service by template Code.

## Runtime transition lifecycle

Mail participates in generic SiteRuntime deactivation for every profile-code change and actual Site deletion.

Required ordering:

```text
enter Mail draining
  -> reject new QueueManual and QueueByCode
  -> prevent workers from starting a new Message claim
  -> query this Site for queued/sending/retryable Messages
  -> if active: abort drain and block transition
  -> if inactive: purge only this Site's transient spool prefix
  -> allow Site mutation/publication
```

- Hold the lifecycle read gate through the queue persistence mutation and through the worker claim mutation. Checking a flag and releasing it before the mutation leaves a transition race.
- Already-running SMTP I/O is not force-cancelled and never runs inside a database transaction. Its Message is `sending`, so the active-message query blocks the transition until it becomes terminal.
- A failed participant/preparer or Site repository mutation aborts draining and restores queue/claim availability on the old runtime.
- Same-profile Site settings updates do not trigger Mail deactivation or wait for the queue to empty.
- Any profile change is blocked while active Mail exists, even when both profiles include Mail.
- Removing Mail from a profile keeps that Site's templates, messages and delivery attempts. Re-adding Mail exposes them again under the new current runtime.
- Deleting the actual Site may cascade-delete its Site-owned Mail business data, but only after the active check and spool purge succeed.
- Transient spool cleanup is irreversible on abort only after the active check proved the objects unreferenced; persistent core Files are never purged by this lifecycle.
- A late scoped Mail job whose Site no longer exists or whose current profile intentionally lacks Mail is generically obsolete and must be ACKed without endless retry. Malformed envelopes, ambiguous handlers and invalid current registries remain errors.
- Mail draining shares the current process-local SiteRuntime limitation; it is not a distributed lock across multiple application processes.

## Template identity

Mail templates need persistence identity and stable semantic code:

```text
ID   -> persistence/admin identity
Code -> integration identity, e.g. feedback_notification
Name -> human label
```

Other modules reference stable template codes rather than DB IDs.

Programmatic integrations use the current site-scoped Mail service. Expose only permission-checked, non-secret template metadata needed to validate configuration (Code, Name, Enabled and variable definitions), then queue through `QueueByCode`; never hand another module a repository, arbitrary SiteID, cross-site template ID, rendered secrets or transport internals. Transient attachment streams are copied into Mail's own spool during queueing, so the caller retains no Mail spool ownership.

Do NOT introduce MailTemplate version history, optimistic template versions, `expected_template_version`, preview fingerprints or similar concurrency machinery unless explicitly requested later.

A Preview is informational. Send always reloads the current authoritative template/site data and renders the current state again. If the template or site changed between Preview and Send, sending the newer current state is acceptable product behavior.

Do not trust rendered recipients/body posted back from frontend; Send re-renders from template + typed raw values.

## Template variables

Reuse the existing core `field.Definition`/field type system instead of inventing a second dynamic-form schema.

Mail-owned input variables use:

```text
data.<key>
```

A variable may be required or optional. Preserve the template author's `Required` setting.

Required:

- missing/empty according to field semantics -> Preview/Send validation error;
- do not silently render empty.

Optional:

- missing/empty -> empty scalar output or absent semantic reference;
- Preview returns a structured warning;
- omission alone does not block Send.

Validate/normalize non-empty values through current field types/rules.

Do not globally change core field semantics for Mail.

## Site variables

Mail also receives backend-authoritative site variables. Admins do not redeclare them as `data.*`.

Use established SEO-compatible names.

Template-safe built-ins include at least:

```text
site.id
site.profile_code
site.domain
site.locale
site.is_public
```

Every field declared by the active site's `Profile.Params` is available as:

```text
site.field.<key>
```

Values come from the current prepared SiteRuntime/Site snapshot. `Site.Settings` contains validated profile parameter values.

Clients never submit authoritative `site.*` values.

Do not expose audit/persistence/security internals such as CreatedBy, UpdatedBy, FileReferences or repository details merely because they exist on `site.Site`.

The template metadata/editor API should return available Site variables for the selected site/profile with useful metadata such as variable name, label and semantic field type. Frontend groups them separately from `data.*` variables.

Keep typed site values typed. File/media fields are not stringified to numeric IDs.

SEO and Mail should reuse a small core/site-owned variable catalog/value source where practical, while `kernel/templating` remains independent from Site domain types.

## Template fields

Support at least:

- From display name/address;
- To;
- CC;
- BCC;
- Reply-To;
- Subject;
- content mode `text` or `html`;
- active text/HTML body;
- static/variable attachments;
- declared `data.*` variables;
- enabled/disabled state;
- stable code/name.

String-capable fields may use placeholders.

Recipients are structured address entries, not an opaque comma-separated string.

Do not introduce default From fallback. Rendered From must be present and valid or Preview/Send fails validation. Sender allowlist/policy remains authoritative.

## Rendering and preview

Render all Mail fields on backend.

Use context-aware generic templating:

- text body -> PlainText;
- HTML body -> HTML with dynamic values escaped by default;
- Subject/address/display-name/Reply-To -> Header context with CR/LF/control injection rejection.

Validate final addresses after rendering. Empty optional recipient entries may be skipped; at least one final recipient must remain.

Preview uses the same authoritative rendering pipeline as Send, but it is not a transaction/concurrency lock. Send reloads current template/site dependencies and renders again.

HTML preview in admin uses a sandboxed iframe/equivalent isolation, never unrestricted Mail HTML inserted into the CMS application's DOM.

## Immutable queued Message

Do not enqueue `template_id + variables` and render inside worker.

Send flow:

```text
current template + current site + typed input
        -> validate/render/resolve
        -> immutable rendered Message snapshot
        -> persist status=queued
        -> durable mail.send(message_id)
```

After Message is queued, later template/site edits never alter that Message.

Persist enough historical identity/origin data such as template ID/code/name, rendered fields and requester/origin metadata. Do not store a template version because Mail templates are deliberately not versioned.

## Jobs and outbox

Mail delivery is asynchronous and uses existing Jobs/Outbox.

Job payload stays small, normally only `message_id` plus existing envelope metadata.

Persist queued Message and durable Outbox state atomically.

Worker:

1. loads immutable Message;
2. safely claims it / ignores local terminal duplicates;
3. starts delivery attempt;
4. opens already-authorized attachments;
5. sends through logical transport alias;
6. records result;
7. applies retry/terminal policy;
8. remains safe under at-least-once job delivery.

Do not hold DB transactions across SMTP I/O.

SMTP cannot guarantee cross-system exactly-once. Preserve stable RFC Message-ID and local claim/lease/idempotency semantics.

## Retry and terminal failure

Retry is explicit and bounded.

Configure positive `MAIL_SEND_MAX_ATTEMPTS` or repository-equivalent setting.

Classify failures:

- network/timeout/transient I/O -> retryable;
- SMTP 4xx -> retryable;
- clear SMTP 5xx -> terminal;
- permanently missing attachment -> terminal;
- unknown transport/configuration -> terminal;
- max attempts reached -> terminal.

Retryable below max attempts:

- persist failed attempt and retryable state;
- return appropriate error so existing job redelivery retries.

Terminal:

- persist terminal failed state;
- stop broker retries according to current runner semantics.

Do not make every `failed` Message claimable forever.

## Message vs DeliveryAttempt

Message keeps current business state, conceptually:

```text
queued
sending
accepted
failed (retryability/terminality explicit)
```

SMTP acceptance is not final mailbox delivery.

DeliveryAttempt stores technical history including attempt number, transport alias/driver, timestamps, safe response code/provider ID, safe error and retryable/terminal outcome.

Never persist credentials/tokens in history.

## Transport abstraction

Mail depends on a transport contract, not SMTP directly.

Use logical aliases (e.g. `default`) mapped by project configuration to SMTP/null/log adapters.

Credentials/host/TLS/auth live in env/config/secrets, never DB templates.

Support explicit timeouts and secure TLS behavior. Do not silently downgrade required TLS.

## Sender policy

Rendered From is checked against configured sender policy. Header values reject CR/LF/control injection.

No default From fallback is introduced.

## Attachment source classes

Mail must distinguish:

1. static template CMS file;
2. manual actor-supplied CMS file variable;
3. backend-authoritative Site file field;
4. trusted automatic persistent CMS file;
5. trusted automatic transient attachment/spool object.

Keep this distinction explicit because authorization and lifetime differ.

## Static template attachment authorization

Static attachment is trusted template configuration.

At template create/update time, validate selected CMS file using the editing actor's normal file authorization and field/attachment constraints.

Once saved, a send-only operator with Mail send permission but without generic `core.file.read` may send that template. They are using approved configuration, not choosing a new file.

Worker may open that persisted file through trusted delivery access after Message queueing.

## Manual file variable authorization

A file ID supplied in manual Preview/Send values is untrusted actor input.

Validate using the current authenticated actor's normal file permission plus declared field storage/MIME constraints.

Never use `security.System()` to authorize arbitrary manual file IDs.

A user with Mail send permission but no access to File #123 cannot exfiltrate it by posting ID 123.

## Site file fields

A compatible `site.field.<key>` file value is backend-authoritative Site configuration already validated through the Site/Profile boundary.

It may be used as an attachment source without requiring a send-only Mail operator to acquire unrelated file browsing permission.

Still enforce Mail existence/size/external-safety rules.

## Automatic persistent file variables

Trusted backend modules calling the internal automatic Mail API may intentionally supply CMS file references with trusted/system semantics. Preserve automatic origin metadata.

Do not expose this trusted path as a browser-controlled API shortcut.

## Temporary/transient attachments

Future Forms may provide uploaded files that must not become ordinary CMS Files.

A transient attachment:

- does NOT create a `core.files` row;
- has no normal CMS File ID;
- never appears in FileExplorer;
- is not permanent user-managed content.

Because Mail is asynchronous, bytes must survive the original request. Therefore use a Mail-owned private temporary spool/store.

Conceptually automatic Mail input may represent either:

```text
persistent CMS file reference
OR
transient attachment {filename, MIME, size, Body/io.Reader}
```

Do not force Forms to create permanent CMS Files.

Transient flow:

1. validate filename/MIME/size/Mail limits;
2. write stream to private Mail spool before queueing;
3. calculate immutable metadata/checksum;
4. persist only opaque spool reference + metadata on Message attachment snapshot;
5. persist Message + Outbox transaction;
6. if DB queue transaction fails, best-effort delete newly created spool object;
7. worker opens spool object through Mail-owned store abstraction;
8. preserve bytes across retryable delivery failures;
9. delete bytes after terminal accepted or terminal failed;
10. run bounded TTL cleanup for orphaned/abandoned spool objects.

Use `ModuleContext.Filesystems()` / logical module filesystem binding when it fits current infrastructure, so Mail depends on a logical private spool alias rather than local paths/S3.

Spool objects are infrastructure-only and never appear in FileExplorer/API as normal files.

Never put large attachment bodies into job payload or PostgreSQL JSON/bytea merely for convenience.

Internal spool keys/paths are never accepted from browser clients and never rendered into outbound messages.

## Attachment scalar interpolation

A file used as attachment differs from scalar interpolation.

Persistent/site file values may become an externally safe public URL only when Mail policy says that URL is suitable for recipients.

Private/admin-only URLs must not leak into outbound Mail.

Transient spool attachments have no scalar/public URL by default; never render internal spool paths/keys through `{{...}}`.

Generic templating never opens files or decides URL policy.

## File/media distinction

Persistent Mail attachments use Files. Transient automatic attachments use Mail spool. Neither requires adding a Media field to Mail.

A future Media field for semantic images such as SEO OG image remains separate.

## Manual requester snapshot

Manual Message history stores immutable requester ID and historical display name.

Resolve display name on backend from authenticated actor/current user service. Never trust requester name from frontend.

Automatic sends use explicit origin metadata.

## Limits

Validate configurable limits before queueing:

- max final recipient count;
- max outgoing Message/attachment size;
- max delivery attempts;
- transient attachment size within Mail policy.

Do not rely on HTTP request size as Mail size policy.

## Manual admin workflow

Admin UI includes:

- template list/CRUD;
- manual Send page;
- template selection;
- dynamic declared `data.*` fields with required markers;
- Site variable list/group;
- backend Preview;
- warnings for missing optional values;
- validation errors for missing required values;
- Send that re-renders current state and queues immutable Message;
- history list and detail/attempts.

Do not require Preview/Send version/fingerprint handshake. If current template/site values changed after Preview, Send uses current authoritative values.

For HTML use existing rich editor; text uses textarea.

Reuse current file picker/upload UI for persistent manual/static files; backend authorization remains authoritative.

## Template lifecycle

Prefer enabled/disabled for integration templates. Avoid casually deleting stable codes referenced by other modules. Historical Message snapshots remain independent of template existence.

A persisted Site-owned MailTemplate is reusable across Profile changes, but Enable, Preview and Send must validate it against the current Mail SiteRuntime, including the currently available transport aliases and Site variable catalog.

## Permissions

Use current module/entity/action conventions plus site-scoped checks.

Backend authorization is authoritative.

Important combined semantics:

- editing static attachment -> Mail template permission + normal file authorization;
- send-only operator using approved static/site file -> no extra generic file-read requirement solely for that configured attachment;
- manual actor-supplied file variable -> normal file authorization required;
- trusted automatic module send -> explicit trusted semantics;
- history -> Mail history permission + site access.

## History reads and retention

History lists are bounded and lightweight. Use summary projections; do not load full rendered bodies/attachment blobs for every row.

Full body/attachments load on detail only.

Support practical server-side filters such as status/template/date/recipient when implemented.

Retention is configurable, bounded and never deletes active queued/sending/retryable Messages. Temporary spool bytes are removed independently once no longer needed; Mail history may keep only metadata/checksum.

## Tests

High-value tests include:

- required data variable missing -> validation error;
- optional missing -> empty/absent + warning;
- site built-ins and `site.field.*` render correctly;
- caller cannot override backend `site.*` values;
- Mail/SEO share compatible Site placeholder names;
- site file field is not rendered as numeric ID;
- static attachment requires editor file permission at save;
- send-only actor can send approved static/site attachment without generic file-read;
- arbitrary manual file ID cannot bypass current actor permission;
- trusted automatic persistent file path works;
- transient automatic attachment queues without creating `core.File`;
- transient attachment survives original request and retryable SMTP failure;
- terminal accepted/failed cleans transient bytes;
- orphan TTL cleanup removes abandoned spool objects;
- internal spool keys never leak through API/rendering;
- DB queue failure best-effort cleans newly spooled attachment;
- immutable queued Message unaffected by later template/site edits;
- Preview may differ from later Send if authoritative template/site changed, and Send uses the current state without template-version conflict;
- queued Message + Outbox atomicity;
- stable RFC Message-ID;
- duplicate terminal job does not resend locally;
- SMTP transient retry, permanent failure and max-attempt behavior;
- requester ID/name backend snapshot;
- recipient/message-size limits;
- site/permission isolation;
- lightweight history list vs full detail;
- retention excludes active messages;
- frontend preview/send/history critical paths.

## Scope control

Do not add unless explicitly requested:

- MailTemplate revision/version history;
- optimistic template versioning;
- Preview fingerprints;
- newsletters/bulk campaigns/subscriber lists;
- SMS/Telegram/WhatsApp;
- conditional/loop template language;
- open/click tracking;
- provider bounce/delivery webhooks;
- inline CID images;
- generic notification workflow engine.

Build Mail as a clean consumer of generic templating + jobs/outbox so Forms and future channels can reuse generic infrastructure without depending on Mail internals.
