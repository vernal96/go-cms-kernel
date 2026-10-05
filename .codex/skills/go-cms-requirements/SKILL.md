---
name: go-cms-requirements
description: Use before implementing a non-trivial or materially ambiguous GO CMS feature when product behavior, architecture, API contracts, persistence, authorization, lifecycle, compatibility, performance, or UX decisions remain unresolved after inspecting the repository. Especially use for requests like "design", "think through", "add a new type/system", or other feature work with multiple valid interpretations. Do not use for small mechanical changes, focused bug fixes, or tasks whose relevant decisions are already specified.
---

# GO CMS Requirements

Follow root `AGENTS.md`. This is a workflow skill for discovering and resolving material requirements before implementation. It may be combined with the smallest relevant domain skill.

## Core rule

Do not interview the user before inspecting the current implementation.

The repository is the source of truth for existing architecture, naming, contracts and behavior. Ask only about decisions that cannot be reliably inferred from current code, tests, configuration or project instructions.

The goal is not to maximize the number of questions. The goal is to eliminate decisions that would otherwise force the implementation to guess.

## Discovery before questions

Inspect the narrowest relevant path first:

1. current domain/service/contract being extended;
2. direct persistence/API/UI consumers affected by the change;
3. one nearby established implementation pattern;
4. focused tests that encode current behavior;
5. relevant domain skill when the task clearly matches one.

Expand only when an actual dependency requires it. Do not scan the whole repository merely to prepare an interview.

Before asking anything, classify each uncertainty as one of:

```text
repository-answerable -> inspect and resolve yourself
safe implementation detail -> choose consistently with existing patterns
material product/architecture decision -> ask the user
```

## What is material enough to ask

Ask when different answers would materially change one or more of:

- user-visible/domain behavior;
- public API contract or compatibility;
- persistence/schema/lifecycle semantics;
- authorization or ownership rules;
- runtime/module boundaries;
- extension points or future extensibility;
- destructive operations/data migration;
- performance characteristics at expected scale;
- admin/public UX behavior that backend metadata cannot determine alone.

Do not ask merely because several implementation techniques are possible if one clearly follows existing project patterns.

## Interview format

Ask focused questions in coherent groups. Prefer one round of at most 5-7 material questions.

For each question where useful:

- state the decision briefly;
- present 2-4 concrete options when the choice space is clear;
- recommend one option when there is a clear project-aligned default;
- explain only the trade-off needed for the user to decide.

Avoid open-ended "how do you want this implemented?" questions when the repository already narrows the options.

Good example:

```text
Should deleting a Library permanently delete all LibraryItems?
A. Yes, in the same domain operation (recommended: preserves ownership invariant)
B. Block deletion while items exist
C. Detach/move items elsewhere
```

Bad example:

```text
What database schema should I use?
```

If the user already specified the behavior, do not ask them to reconfirm it.

## Multi-round rule

After the first answers:

1. update the mental requirements model;
2. re-check whether any material ambiguity remains;
3. ask a second, shorter round only if the previous answers introduced or exposed a real unresolved decision;
4. do not continue interviewing for low-risk implementation details.

Two rounds should be exceptional, not automatic.

## Decision summary before implementation

Before editing code after clarification, summarize concisely:

```text
Goal
Resolved behavior
Architecture/ownership choices
Important constraints
Explicit assumptions, if any
```

Do not turn this into a long design document unless the user asked for one.

If a material decision is still unresolved, do not silently choose it and implement.

## Interaction with domain skills

This skill governs **whether/what to ask**. Domain skills govern **how the resolved feature should fit the project**.

Examples:

```text
new resource type with unclear lifecycle
  -> go-cms-requirements + go-cms-resources

new site permission model
  -> go-cms-requirements + go-cms-authorization

new CRUD contract with unclear semantics
  -> go-cms-requirements + go-cms-api

cache implementation with fully specified semantics
  -> go-cms-cache only
```

Do not load unrelated skills just to make the interview broader.

## Stop conditions

Proceed to implementation when:

- current code has been inspected enough to understand the affected contract;
- all material user decisions are resolved;
- remaining choices are safe implementation details governed by existing patterns/instructions;
- the intended behavior can be summarized without guessing.

Skip this skill entirely when those conditions are already true at task start.
