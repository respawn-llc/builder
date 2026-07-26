# KENT-334 requirement/evidence matrix

Updated: 2026-07-26. Current Task body and
`.kent/plans/KENT-334-takeover-audit.md` are authoritative. Manual QA and
deployment are excluded. Rust is frozen.

Status key: `[x]` means evidence was reproduced in this worktree. `[ ]` means
implementation or final evidence is still incomplete.

## Current Task-body completion checklist

| Status | Requirement | Owning specification | Production evidence | Reproduced test/evidence |
| --- | --- | --- | --- | --- |
| [x] | Migrate retained sequential, parallel, interrupted, Question, Approval, Join, Session, and canceled-Task state. | `workflow-orchestration.md` migration clauses | `server/metadata/migrations/00060_task_current_state_cutover.up.sql`, `migration_functions.go` | `./scripts/test.sh ./server/metadata/... -count=1` |
| [x] | Delete Run/Placement/history persistence in one hard cutover. | Persistence and hard-cutover clauses | Migration 60 only; migrations 61–65 deleted; generated bindings regenerated | Effective schema dump plus metadata suite |
| [x] | Make Workflow Execution the sole execution authority and remove scheduler/reconciliation/automatic-registration. | Workflow Execution and Exact Execution Scopes | `server/workflowexecution.CurrentNodeController`; obsolete startup coupling removed | Controller, Authority, startup, composition, shutdown-order, and positive repository guard suites pass |
| [x] | Pass focused lifecycle, interrupt, Question, read-model, migration, deletion/edit serialization tests. | Lifecycle, Questions/Approvals, serialized mutation clauses | Current Node store/service/controller paths | Focused server package matrices and full server target pass |
| [x] | Remove Task Cancel across server, Go client, CLI, and Desktop. | Task Delete / Task Interrupt clauses | Active contracts, routes, commands, events, and Desktop controls removed | Go structural guards, client/CLI suites, and 160 Desktop tests pass |
| [x] | Cut Go remote client, CLI, and Desktop to Current Node/Session contracts. | Contracts and clients clauses | `shared/client`, `cli/kent`, `apps/desktop` | Go client/CLI suites and Desktop lint/typecheck/tests pass |
| [x] | Preserve Activity as server-paginated Comments plus retained Session creation. | Activity clause | `server/workflowview/activity.go`, Desktop typed union | Ordering, equal-time, cursor pagination, and typed-variant coverage pass |
| [x] | Update Project Home canonical activity and make Current Node mutations touch Task activity. | Project Home clause | Task mutations call `touchTaskUpdatedAt` | Canonical ordering/pagination and Current Node mutation-to-order integration tests pass |
| [x] | Add structural architecture guards without exclusions or allowlists. | Guardrails clause | Go AST/composition, effective-schema/query, typed Session metadata, and parsed embedded-Script guards | Focused guard matrix and full server target pass |
| [x] | Keep handwritten non-test production code net-negative against `3c7d45a62`. | Task delivery boundary | Final ledger: 206 files, +10,908 / -13,520 | Net `-2,612`; production-patch SHA-256 `ef10dd89a4d1d9f99a5495068f105f72397124236bdc324879ec7a4f800743f2` |
| [x] | Bump active Go/Desktop protocol once and update active fixtures. | Contracts and clients clause | `shared/protocol/version.json` = 72 | Previous-generation rejection, current handshake round trip, Desktop transport tests |
| [x] | Run final scoped non-Rust tests, CI checks, and builds. | Delivery boundary | `scripts/test.sh` defaults to the proven stable maximum of eight Go packages | Server/Desktop tests and builds, dependency policy, frontend lint, vet, formatting, docs build/smoke all pass |

## Original ordered-plan reconciliation

### Current-state model and persistence

- [x] Introduce the task-current-state domain contract.
- [x] Add normalized metadata structures with aggregate-level constraints.
- [x] Move Task creation and Task Start onto Current Nodes.
- [x] Persist direct Session ownership and deterministic Node associations.
- [x] Replace Context Source history queries.
- [x] Implement sequential Current Node completion.
- [x] Materialize the current value environment.
- [x] Persist current pending Approval state.
- [x] Implement fan-out creation and branch progression.
- [x] Implement latest-definition Join arrival and aggregation.
- [x] Port Task-wide Move semantics, including approval-required executable Move.
- [x] Build the sequential/interrupted migration tracer.
- [x] Migrate materialized value environments before deleting history.
- [x] Complete parallel/Approval/Join migration and execute the hard schema cutover.
- [x] Cut legacy Session workflow metadata and persisted inspection.

Evidence: metadata/workflowstore focused suites, migration rollback fixtures,
manual Move store/service integration tests, and regenerated query bindings.

### Workflow Execution, runtime, lifecycle, and composition

- [x] Replace workflow-Session provenance, policy, and status consumers.
- [x] Adapt `sessionruntime.Authority` to Current Node scope refs and scoped immutable snapshots.
- [x] Replace the polling scheduler with Workflow Execution core state.
- [x] Enforce one executable-eligibility rule.
- [x] Implement admission and restart-marker flow.
- [x] Implement scope retirement, completion release, and Resume.
- [x] Make Task Interrupt drain every controller work state.
- [x] Prove or reject the need for controller mutation exclusions.
- [x] Cut production composition and lifecycle to Workflow Execution.
- [x] Serialize Task deletion through Workflow Execution.
- [x] Port agent execution preparation in the runner.
- [x] Port Script Node execution.
- [x] Remove workflow Run identity from workflow-specific runtime/transcript writes.
- [x] Port Question issuance, await, clear, and skipped batches to Exact Scopes.
- [x] Cut lifecycle server APIs and transport handlers to Current Node/Session contracts.
- [x] Cut server Question answering from Run identity.

Evidence: scoped Authority/controller suites; runner Agent/Script product
boundaries; lifecycle, Question, serialization-race, startup/composition, and
shutdown-order tests; the positive structural guard matrix; and the full
server target.

### Clients, read models, deletion/edit behavior, and documentation

- [x] Remove Task Cancel server and public-API behavior.
- [x] Cut the Go remote client to the locked server contracts.
- [x] Update CLI workflow controls and output.
- [x] Rebuild Task status, Board, Detail, and paginated List projections.
- [x] Rebuild attention and notification projections.
- [x] Rebuild Workflow graph edit impact and serialize Save.
- [x] Rebuild Workflow deletion impact and serialize deletion.
- [x] Rebuild Project Home activity from retained canonical facts.
- [x] Serialize Project deletion and replace old blocker counts.
- [x] Move Session workspace-retarget ownership checks to direct Task ownership.
- [x] Reduce Activity and Task Detail to approved Session behavior.
- [x] Cut remaining Desktop API contracts to the new protocol.
- [x] Update Workflow editor Context Sources to Session language.
- [x] Update Desktop behavior without adding a persistence-model UI.
- [x] Bump the Go/Desktop protocol once.
- [x] Cut the public workflow guide and embedded Script contract to Task-owned current state.
- [x] Delete obsolete mechanisms and add exact architecture guards.

Evidence: workflow view/project deletion and mutation matrices, Desktop
contract/feature tests, docs test/build/smoke, and structured prompt example
validation all pass.

### Final evidence

- [x] Run focused end-to-end workflow tests.
- [x] Run focused parallel and migration tests.
- [x] Perform final scoped non-Rust repository verification.
- [x] Refresh `./bin/kent`.
- [x] Recompute and record the final net-negative production ledger.

Final reproduced commands:

```text
KENT_TEST_GO_PACKAGE_PARALLELISM=8 ./scripts/test.sh server
./scripts/test.sh server
./scripts/test.sh server desktop
./scripts/build.sh server desktop --output ./bin/kent
./scripts/ci-check.sh deps
./scripts/ci-check.sh frontend-lint
./scripts/ci-check.sh vet
gofmt -l .
pnpm --dir docs test
pnpm --dir docs build
pnpm --dir docs smoke:built
```

Manual QA and deployment are excluded by the takeover goal.
