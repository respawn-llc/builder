# KENT-484 implementation handoff

## Completed plan slices

- `shared/jsoncontract` dependency boundary and tests.
- One-time repository inventory in `.kent/work/KENT-484-json-inventory.md`.
- Eight-tool `StaticToolContracts`, Registry ownership, runtimewire composition,
  schema-free global metadata, request filtering, and complete-node placeholder
  removal.
- Canonical tool ingress before dispatch, Edit/Ask structural parser deletion,
  and Ask Question batch/error bookkeeping.
- Typed reviewer schema preparation and response validation.
- Workflow Completion Contract migration, dynamic request/dispatch reuse,
  Script completion validation, and `complete_node.json` deletion.
- LLM transport hard cutover from raw schema bytes to prepared
  `jsoncontract.Function` / `jsoncontract.Structured` values. OpenAI schema
  parsing, fallback object schemas, recursive normalization, and independent
  structured-output strictness were deleted.
- Workflow graph and Task Complete CLI boundaries. Both prepare contracts at
  command composition, validate through `jsoncontract`, then typed/domain
  decode. Manual presence/type/forbidden-field parsers were deleted.
- Workflow prior-values and metadata migration 00060. The Workflow Store owns
  one prepared prior-values contract used by every Current Node read path.
  Migration 00060 structurally normalizes empty-object input bindings to
  arrays before callback decoding and persists Workflow Edge bindings
  canonically. The stateless decoder now accepts typed non-nil slices only.
- Onboarding Finalize requests, Update Status responses, and Session execution
  request/response unions now use typed source schemas and prepared contracts.
  Gateway composition owns the two request contracts; Remote composition owns
  the two response contracts. Structural `UnmarshalJSON` implementations and
  generic member-presence maps were deleted.
- The final cross-platform `go/packages`/AST JSON-contract architecture guard
  now rejects JSON token walkers outside the exact KENT-530 function, generic
  decoded-object inspection outside the approved dependency/presentation and
  exact KENT-535 seams, raw schema maps/keyword mutation, provider schema
  processing, and production schema JSON files. Positive and negative fixtures
  cover every category. Temporary inventory bookkeeping and all remaining
  first-party tool schema files were removed.
- The deletion/integration sweep is complete: all changed Go files were
  formatted, `go mod tidy` completed, in-scope mechanism searches are clean,
  the nine schema deletions total exactly 194 lines, app policy/frozen Rust
  paths remain untouched, exact KENT-530/KENT-535 exclusions remain, and
  `git diff --check` passes.

The first twelve Planning checkboxes through the deletion/integration sweep
are marked complete in `.kent/plans/KENT-484.md`.

## Current slice: complete

All Planning checkboxes are complete.

## Review remediation

- [x] Preserve canonical Edit alias input in persisted assistant tool-call presentation.
- [x] Delete the obsolete duplicate Session execution field decoder.
- [x] Remove the two small-package classifications by deepening `shared/jsoncontract`
  and merging the tool Registry fixture into an existing test-harness package.
- [x] Run focused tests, commit the complete review-fix round, and return to review.

## QA remediation

- [x] Remove the concrete-tool import cycle from the shared Registry fixture.
- [x] Inspect every consumer and equivalent concrete-tool package test.
- [x] Run focused package and architecture tests, commit, and return to QA.

## Verification notes

- Relevant static-tool, ingress, reviewer, Workflow completion, runtime,
  workflowrunner, LLM transport, prompt-cache, CLI graph, Task Complete, and
  `shared/jsoncontract` tests passed during implementation.
- Full `go test ./server/llm`, `go test ./server/tools`, and
  `go test ./cli/kent` passed after their respective slices.
- Full `go test ./server/workflowstore ./server/workflowview ./server/metadata`
  passed after the persistence/migration slice.
- Full `go test ./shared/serverapi ./shared/client ./server/transport` plus
  focused startup/core and full onboarding/sessionview/serverstatus packages
  passed after the typed-union slice.
- Cross-platform production guard and all positive/negative guard fixtures pass
  in `go test ./server/core -run
  'TestJSONContractArchitectureGuard|TestProductionJSONContractArchitecture'`.
- `git diff --check` passed after the latest slice.
- Final `./scripts/test.sh server` was attempted twice before this handoff and
  surfaced KENT-484 integration failures. A third broad verification after
  remediation found additional stale runtime test inputs; focused regressions
  now pass. The server suite remains caveated only by five unrelated Workflow
  Goal lifecycle failures that reproduce unchanged on baseline commit
  `badbb5884`. The operator confirmed current main commit `304cc3790` already
  fixes that unrelated issue with full CI green and explicitly directed this
  ticket not to duplicate or expand that work.
- `./scripts/build.sh --output ./bin/kent` passed.
- Additional focused verification passed for `shared/jsoncontract`,
  `shared/serverjsoncontract`, `shared/serverapi`, `shared/client`,
  `server/transport`, `server/tools`, `server/runtimewire`, the architecture
  guards, canonical runtime ingress regressions, and stale result-group test
  inputs.
