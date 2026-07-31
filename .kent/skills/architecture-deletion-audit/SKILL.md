---
name: architecture-deletion-audit
description: Find wide code mechanisms with narrow or unproven product value and turn one coherent candidate into a plain-language deletion decision. Proactively use for architecture audits, simplification, redundant IDs or state, retry/recovery machinery, protocol fields, and code that may exist only because other code expects it.
---

## Goal

Produce one evidence-backed decision about whether Kent should retain a behavior and its supporting mechanism. The operator must be able to decide without reading code.

The audit agent finds candidates. It does not edit code, specifications, tests, or task records. The workflow may create a Main SWE task after operator approval.

## Authority

Read these before evaluating a candidate:

- `docs/dev/specs/terminology.md` for product language.
- The specifications that own the affected behavior.
- `AGENTS.md` files governing the scope.

Specifications and explicit operator decisions define required behavior. Existing code and tests are evidence of implementation, not proof that a behavior is required.

Use product terms from the terminology specification. Define any general architecture label when first used; do not present audit labels as Kent domain terms.

## Candidate size

Select one coherent behavior decision whose likely implementation changes roughly 1,000–3,000 lines.

- Split a larger candidate at a real product or ownership seam.
- Combine smaller findings only when one product decision and one source-of-truth change remove them together.
- Do not group unrelated cleanup to reach the size target.
- Estimate production, test, generated, and protocol changes separately.
- Treat the estimate as uncertain evidence, not a reason to pad or truncate the work.

## Audit method

### 1. Find wide mechanisms

Prefer mechanisms that cross several modules or clients:

- IDs, correlation fields, idempotency keys, and memo keys.
- State duplicated between client, server, persistence, and read models.
- Retry, replay, reconnect, cancellation, restoration, or deduplication machinery.
- Enums, pending states, tombstones, and transition coordinators.
- Contract fields copied through Go, Rust, and TypeScript without changing behavior.
- Interfaces with one adapter or pass-through modules.
- Compatibility paths without a specified migration or supported old behavior.

Static search produces candidates only. Do not call a repeated field unnecessary until its product behavior and ownership have been traced.

### 2. Trace one candidate end to end

Record:

- Where the value or state is created.
- Every module that copies, validates, stores, reads, or discards it.
- Every decision that depends on it.
- Whether it survives client reconnect, server restart, and process loss when those cases are relevant.
- Existing product state or entity identity that may already answer the same question.
- Tests that cover it and the observable behavior each test proves.

Classify tests that only preserve call order, field equality, memo entries, tombstones, or internal transitions as implementation evidence unless they also prove a specified user-visible outcome.

### 3. Apply the deletion questions

Answer each question concretely:

1. What can the operator observe because this mechanism exists?
2. Which specification requires that behavior?
3. What is the authoritative source of every fact involved?
4. What exact sequence behaves differently if the mechanism is deleted?
5. Can existing product state or an existing entity identity provide the retained behavior?
6. What code, contract surface, states, and tests disappear if the behavior is rejected?
7. What evidence argues that deletion would be wrong?

Reject vague benefits such as “safer retries,” “better reconciliation,” or “more robust.” Translate each claim into an exact sequence with a visible before-and-after result.

### 4. Recognize useful labels

These are audit labels, not Kent domain terms:

- **Pass-through data:** created and copied across modules but never drives a distinct decision.
- **Duplicate fact:** two owners represent the same product fact and must stay synchronized.
- **Mechanism-only state:** a state exists to coordinate an implementation technique rather than a distinct product behavior.
- **Validation-only duplicate:** two fields are required to contain the same value.
- **Protocol leakage:** internal coordination is exposed to clients that do not need to choose or display it.
- **Process-local guarantee:** a claimed recovery or deduplication guarantee depends on state lost at process restart.
- **Speculative seam:** an interface has one adapter and no demonstrated variation.

Labels help search and ranking. Evidence decides whether a candidate is valid.

## Decision card

Present exactly one candidate using this structure:

### Proposed decision

One sentence stating the behavior to retain or reject. Avoid implementation names unless no product term exists.

### What changes for the operator

- Today: one concrete sequence and visible outcome.
- Proposed: the same sequence and visible outcome after deletion.
- Unchanged: adjacent behavior that remains supported.

### Why this may be unnecessary

- The claimed benefit.
- The specification requiring it, or `No specification found`.
- The authoritative state that already exists.
- Why that state is sufficient or insufficient.

### Cost

- Modules and client contracts affected.
- Estimated changed lines, split into production, tests, generated code, and protocol artifacts.
- States, branches, fields, and tests expected to disappear or simplify.

### Contrary evidence

State the strongest reason to keep the behavior and what evidence would settle uncertainty. Never hide weak or conflicting evidence.

### Choice

Give two product choices:

1. Retain the behavior and mechanism.
2. Reject the behavior and create the deletion/refactor task.

Recommend one choice based on evidence and code removed, not aesthetic preference.

## Main SWE task contract

Prepare an implementation task only for choice 2. Its title must be under 40 characters. Its body must be standalone and include:

- The approved product behavior and non-goals.
- The existing authoritative state or identity to preserve.
- The mechanism and duplicated state to remove.
- Known affected clients and contracts.
- Required specification updates, if the operator's decision changes a specification.
- Concrete completion checklist items.
- Deterministic tests, builds, architecture guards, and manual QA where applicable.
- The estimated 1,000–3,000-line scope and evidence behind the estimate.
- Contrary evidence or unresolved implementation risks the implementer must verify.

Do not prescribe file-by-file edits when ownership should be rediscovered in the implementation worktree.

## Completion rules

- Do not recommend deletion when the user-visible consequence is unknown.
- Do not infer a requirement from code volume, comments, test names, or historical compatibility.
- Do not silently choose product behavior.
- Do not create an implementation task before the operator approves the deletion transition.
- End without a candidate when evidence is insufficient or no coherent candidate fits the requested scope.
