---
name: spec-writing
description: Write, rewrite, review, or validate Kent product specifications under docs/dev/specs. Must use before any task that changes or evaluates those specifications.
---

## Authority
- `docs/dev/specs/` is authoritative for locked product and product-architecture decisions.
- Specs encode explicit user decisions. Do not invent, remove, weaken, or change a decision without prior user approval.
- Implementation can drift from a spec. Do not change a spec only to match code, tests, review comments, or implementation convenience.
- When the user explicitly changes product behavior or architecture, update the owning spec.
- An explicit superseding decision overrides the decision that it replaces.

## Content boundary
- Include user-visible behavior, operator-visible requirements, public compatibility contracts, domain ownership, and product invariants.
- Express product architecture through observable ownership, ordering, concurrency, failure, recovery, limits, and compatibility.
- Include an implementation detail only when that detail is itself a public contract.
- Exclude package and file ownership, code symbols, tests, storage schemas, serialization, request handlers, internal schedulers, transaction layout, and framework choices.
- Exclude implementation plans, worklogs, audits, migration or rollout history, code-drift notes, and temporary checklists.
- Track unimplemented product work in GitHub issues. Track implementation cleanup in `docs/dev/techdebt/techdebt.md`.

## Language
- Use common English and the domain terms in `docs/dev/specs/terminology.md`.
- Use one term for one meaning. Add a reusable Kent-specific term to `terminology.md` before using it across specs.
- Use short, direct sentences with an explicit subject and active voice.
- Put conditions before outcomes.
- State one requirement per sentence.
- Keep each prose paragraph or list item on one physical line. Do not hard-wrap prose to a column width or insert line breaks between sentences. Use line breaks only for Markdown structure such as separate paragraphs, list items, tables, block quotes, and code blocks.
- Use `must` for normative requirements.
- Write timelessly. Do not use changelog, rollout, or work-in-progress language.
- Preserve literal product copy exactly when the wording is part of the contract.

## Ownership
- Put each decision in one owning spec.
- Keep another spec's reference short and contextual. Do not duplicate its full contract.
- A page must explain its own subject without depending on implementation knowledge.
- Use the area index in `docs/dev/specs/README.md` to select the owning spec.

## Rewriting
1. Read `docs/dev/specs/terminology.md`, the owning spec, and adjacent specs that define shared behavior.
2. Inventory each existing product behavior before editing:
   - success and no-op behavior;
   - failure and recovery behavior;
   - ordering and concurrency;
   - defaults, limits, and time bounds;
   - retry, interruption, and idempotency;
   - public commands, configuration, output, copy, and compatibility.
3. For each implementation detail, identify the product behavior or invariant that it protects.
4. Reframe that behavior in domain language. Remove only the incidental mechanism.
5. If a detail can reasonably be either a public contract or an incidental mechanism, ask the user before removing it.
6. Compare the completed rewrite with the prior text. Restore any behavior, failure, ordering, limit, or compatibility rule that was lost.

## Review and verification
- Treat approved spec changes as authoritative during code review. Do not request a revert because the implementation differs.
- Validate implementation and QA results against the applicable specs.
- Check that each requirement is testable or observable at a product boundary.
- Check that the final text has no implementation topology, temporary history, duplicated decisions, or undefined code-internal terms.
- Check that rewrites preserve every success, failure, ordering, concurrency, limit, and compatibility contract.
- Run `git diff --check`.
- Documentation-only changes do not require product builds or tests unless another repository rule or the task requires them.
