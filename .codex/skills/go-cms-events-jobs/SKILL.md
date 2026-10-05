---
name: go-cms-events-jobs
description: Use for GO CMS domain events, transactional outbox, background jobs/workers, event-bus publishing/consumption, retries, idempotency and asynchronous lifecycle. Use when a business mutation must reliably trigger work after commit or when introducing reusable background processing.
---

# GO CMS Events, Jobs and Transactional Outbox

Follow root `AGENTS.md`. This skill defines the semantic and reliability rules for asynchronous work.

## Keep the four concepts separate

- **Domain event** is an immutable past-tense fact that already happened, e.g. `resource.updated` or `form.submitted`.
- **Job** is an imperative unit of work to execute, e.g. `search.reindex_resource`, `mail.send` or `media.generate_preview`.
- **Transactional outbox** durably records a message in the same database transaction as the business mutation so the message cannot be lost between DB commit and broker publish.
- **EventBus** is transport. The existing `kernel/eventbus` `Bus` remains the low-level broker abstraction; do not turn it into the domain-event model or transaction manager.

Do not use the words event/job/outbox interchangeably in APIs or package names.

## Primary mutation stays synchronous

Do not turn normal CMS CRUD into background work merely because jobs exist.

For a resource update, the request should still synchronously:

```text
validate -> persist resource/fields/widgets/revision -> append outbox -> COMMIT -> return success
```

Only secondary work that may safely happen after the business commit belongs in asynchronous handlers/jobs, for example search indexing, mail, webhooks, media transformations or external integrations.

If failure of a step means the primary business state would be invalid, keep that step inside the synchronous transaction rather than hiding it in a job.

## Transactional outbox invariant

For a mutation that emits a reliable event:

```text
BEGIN
  mutate domain state
  persist any mandatory synchronous history/revision state
  INSERT outbox message
COMMIT
```

The domain mutation and outbox insertion must commit or roll back together.

Never implement the reliable path as:

```text
repository.Update() // commits
EventBus.Publish()  // may fail afterwards
```

Never publish to the external EventBus before the business transaction commits; the broker message could become visible even if the DB transaction later rolls back.

The repository/adapter that owns the physical DB transaction must be able to append the outbox row through that same transaction. Do not push SQL transaction orchestration into high-level domain services solely to support outbox.

## Persistence boundaries

- Generic kernel code may define reusable event/job/outbox message contracts and worker mechanics.
- Concrete outbox persistence belongs at the database adapter edge and must live in the same physical database/transaction domain as the business data it protects.
- Do not make core/domain packages depend on PostgreSQL, Kafka or RabbitMQ types.
- Do not assume every future module uses the same physical DB connection. The design must allow an outbox source to be associated with the module/database that can commit it atomically.
- Reuse nearby transaction patterns in module PostgreSQL adapters instead of introducing a reflection/service-locator transaction framework.

## Event envelope

Keep a small transport-independent semantic envelope. It should carry enough stable metadata for routing, observability and idempotency, typically:

- unique message/event ID;
- stable event name;
- occurred-at timestamp;
- payload/schema version where useful;
- site/resource/entity identifiers in the typed payload when they are domain data;
- correlation/causation IDs only if they are actually available and useful.

Payloads must be explicit versionable DTOs. Do not serialize arbitrary service/domain structs and treat that representation as a permanent integration contract.

Event names are stable, lower-case semantic facts such as `resource.updated`; avoid package-path names or Go type names as public broker contracts.

## Outbox records and publisher

Persist enough operational state to safely publish and retry, such as message identity, topic/name, body/headers, created/available time, attempt count and last failure/published state.

The outbox publisher is application-scoped background lifecycle, not site runtime state and not request state.

A safe publisher must:

1. claim a bounded batch;
2. allow multiple publisher instances without double-processing the same claim window (use the adapter's proper locking/lease mechanism, e.g. PostgreSQL `FOR UPDATE SKIP LOCKED` or an equivalent explicit lease);
3. publish through `eventbus.Bus`;
4. mark success only after broker publish succeeds;
5. persist retry state on failure and try again later;
6. stop cleanly on application shutdown.

Do not hold a database transaction open while waiting on slow broker I/O if a lease/claim design can avoid it safely.

Published outbox rows need an explicit cleanup/retention policy so the table does not grow forever. Keep cleanup operational and bounded; do not mix it with domain history/revision semantics.

## Delivery semantics and idempotency

Assume **at-least-once** delivery. Do not claim exactly-once behavior across PostgreSQL and Kafka/RabbitMQ.

A message can be published or consumed more than once due to crash/retry windows. Therefore:

- consumers/jobs that produce externally visible side effects must be idempotent or deduplicate by stable message/job ID;
- handlers must not rely on broker redelivery being impossible;
- a duplicate event must not corrupt current CMS state.

Use the existing connector acknowledgement/retry semantics rather than reimplementing broker internals in domain code.

## Events vs jobs

An event producer does not know which feature packages consume the event.

Good:

```text
core resource mutation -> resource.updated
search listener         -> decides to reindex
mail/form listener      -> decides whether mail work is needed
```

Bad:

```text
ResourceService.Update -> SearchService.Reindex -> AuditService.Write -> MailService.Send
```

A handler may translate an event into a job when the work is a concrete asynchronous command. Job names should be imperative and handlers should own the feature behavior they execute.

Do not create a job layer solely to wrap a handler that is already safe and naturally asynchronous; keep the first implementation small and prove the abstraction with real work.

## Worker lifecycle

- Workers are application-scoped and owned by `App`/application lifecycle composition.
- Start them only after required migrations/connectors/services are ready.
- Shutdown/cancel workers before closing the EventBus or the databases they use.
- Use application worker contexts derived from lifecycle context; never retain an HTTP request context for background work.
- Log worker start/stop, publish failures and exhausted/repeated retries with stable structured event fields.

## First integration rule

When introducing this subsystem, use one narrow existing domain event (normally a resource mutation) to prove the complete path:

```text
resource transaction
  -> resource revision/state
  -> outbox row
  -> outbox publisher
  -> existing EventBus
  -> focused consumer/handler test
```

Do not simultaneously implement Search, Mail, Forms, Audit, sitemap rebuilds and webhooks. They are future consumers of the same foundation.

## Testing invariants

Add focused tests for at least the invariants touched by the implementation:

- business mutation and outbox insertion are atomic;
- rollback leaves neither mutation nor outbox message committed;
- no external publish occurs before commit;
- publisher retries a failed publish without losing the row;
- success marks/cleans the message according to the chosen retention policy;
- concurrent publisher instances cannot both claim the same available row in one claim window;
- duplicate delivery is safe for the example consumer/idempotency boundary;
- worker shutdown cancels cleanly before dependencies close;
- event payload/name are stable and explicitly encoded.

Use focused unit/integration tests while iterating and broad backend `test/vet/build` once near completion because this subsystem is cross-cutting.

## Scope control

Do not add a workflow engine, scheduler/cron framework, distributed task graph, priority queues, admin monitoring UI, generic saga framework or broker-specific DLQ management unless explicitly requested.

The first goal is a small reliable foundation:

```text
Domain Events + Transactional Outbox + Publisher lifecycle + minimal Jobs abstraction + retry/idempotency rules
```
