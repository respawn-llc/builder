# KENT-334 takeover continuation

Updated: 2026-07-26

## Durable authority

- Task body and `.kent/plans/KENT-334-takeover-audit.md` remain authoritative.
- Manual QA and deployment are excluded from this implementation handoff.
- Rust remains frozen.

## Integrated main

- Local `main` was merged into KENT-334 as `62a6e5577`.
- Conflicts kept the Current Node cutover and discarded reintroduced legacy
  scheduler/Run files.
- Main's Workflow name prompt field was ported to the Current Node
  `workflowruntime.TaskInstructions` path.
- Main added `server/workflowrunner/session_recovery_test.go`; it is still
  Run-shaped and currently fails package compilation. Port its retained
  Session recovery behavior to Current Nodes instead of deleting the behavior.

## Preserved takeover slices

- Go client/CLI changes are in the worktree. Focused
  `./scripts/test.sh ./shared/client/... ./cli/kent` passed before the main
  merge.
- Public workflow docs and embedded workflow-skill changes are in the
  worktree. `pnpm --dir docs test`, `build`, and `smoke:built` passed before
  the main merge.
- Desktop API and UI cutover is being completed by continued subagent session
  `c82f8c20-340e-4ac1-b019-f6d8734bba2c`; background shell session `3608`
  must be allowed to finish and then reviewed.
- The unrelated Session append-recovery protocol rewrite from the interrupted
  agent was intentionally not retained. The 650-record transcript fixture
  batching remains.
- Stash `stash@{0}` is the applied takeover stash retained by the failed
  `stash pop`; do not reapply it. Drop it only after confirming the worktree
  contains the intended client/docs/test changes.

## Migration checkpoint complete

The migration repair is now complete and verified:

- `server/metadata/migrations/00060_task_current_state_cutover.up.sql` is the
  sole Current Node hard-cutover migration. Former versions 61–65 were deleted.
  It runs transactionally and retains one deliberately failing Down path.
- The late-failure test begins at version 58, allows the independent version
  59 migration to commit, proves every version-60 replacement structure and
  destructive change rolls back, then proves Down cannot alter the completed
  version-60 schema.
- Structured SQLite functions now:
  - materialize current inputs from typed frozen bindings;
  - preserve all frozen prior-Node values;
  - fill required values branch-first then Task-wide;
  - sort requirements deterministically;
  - reject malformed/missing/conflicting values and any unresolved ordering
    tie with Task/Node/branch/value-key context.
- Reusable temporary graph/candidate staging feeds Current Nodes and frozen
  pending-Approval target snapshots without duplicated history queries.
- Pending-Approval target snapshots now retain current inputs, prior-Node
  values, exact entering edge identity, correct serial/fan-out/parallel branch
  identity, Session selection, and scheduling state.
- `entered_by_edge_id` intentionally has no database foreign key: migrated
  frozen Approval snapshots must preserve their entering edge identity after
  the mutable legacy graph was already deleted. Current graph mutation policy
  remains the authority for current writes.
- Parallel projection rejects several active fan-out batches, unresolved or
  duplicate branches, and competing/malformed Join arrivals with contextual
  diagnostics.
- Malformed `sessions.metadata_json` aborts with Session context instead of
  erasing unrelated metadata.
- Added value-carrying sequential, parallel, serial Approval, and parallel
  Approval fixtures plus malformed/missing/conflict/tie/branch/Join failures.
- Regenerated `server/metadata/sqlitegen` and dumped the effective schema to
  `/tmp/KENT-334-metadata-schema.sql`; the dump contains none of the removed
  Run/Placement/Transition-history or Task-cancellation schema.

Current verification:

```text
./scripts/test.sh ./server/metadata/... -count=1
PASS (75.925s)
```

## Other completed takeover slices

- Desktop Current Node/retained-Session cutover is complete, including the
  independent review follow-up:
  - Script current nodes cannot carry Sessions;
  - unknown/malformed attention events fail the contract;
  - Question/Approval/interrupted-current-node coherence is validated;
  - applied lifecycle responses require Current Nodes;
  - Activity accepts only Comment and Session-started variants.
  - `pnpm --dir apps/desktop lint`, `typecheck`, and `test` pass (159 tests).
- `server/workflowrunner/session_recovery_test.go` is ported to direct retained
  Session/Current Node behavior. `./scripts/test.sh ./server/workflowrunner`
  passes.
- `./scripts/test.sh ./shared/client/... ./cli/kent -count=1` passes.

## Active delegated work

- Runtime goal-status regression agent:
  - agent session `7bc6ac0f-353f-4c55-a927-9642147dcdd9`
  - background shell `1097`
  - write scope `server/runtimecontrol/**`
  - must be allowed to finish and reviewed before completion.

## Remaining takeover audit sequence

- Fix approval-required manual Move. Analysis at handoff:
  - `ApplyManualMove` materializes the concrete target before deleting state;
    when `edge.RequiresApproval`, it should delete superseded Approval state,
    freeze a new `workflow.PendingApproval`, apply any selected execution
    target lock, retain the source Current Node, touch the Task, and commit
    without inserting/starting the target.
  - extend `ManualMoveResult` with the pending Approval and retained source
    Current Node(s), or an equivalently truthful typed result.
  - `workflowsvc.moveWorkflowTask` must skip `Start` and return the retained
    source Current Node in the applied response until Approval.
  - add store and service integration coverage proving source retained,
    pending Approval frozen, no target/current execution start, then Approval
    replaces the source and starts the target.
- Review/integrate the active runtime goal-status agent.
- Repair all stale server/client/CLI tests and port the main Session recovery
  test (Session recovery itself is now ported).
- Restore Current Node product-boundary coverage and positive architecture
  guards.
- Finish Desktop and bump Go/Desktop protocol once, with old-version
  rejection (Desktop is finished; protocol remains).
- Replace global live-snapshot copies with scoped snapshot reads.
- Make server/Desktop tests, vet, builds, docs checks, scoped CI checks, and
  net-negative ledger pass.
- Remove the still-present
  `shared/serverapi.WorkflowTaskMoveRequest.AllowMissingEdge` field and prove
  active Go/Desktop contracts contain neither `allow_missing_edge` nor
  `auto_approve`.
- Correct stale round-5 claims in `.kent/plans/KENT-334.md` and
  `.kent/work/KENT-334-server-cutover.md`; they still describe the rejected
  append-recovery rewrite.

## Continuation checkpoint — 2026-07-26 16:00 CEST

The preceding active/runtime/manual-Move notes are historical. Current state:

### Newly completed

- Approval-required executable Manual Move:
  - `ManualMoveResult` exposes retained Current Nodes and pending Approval.
  - superseded task Approvals are deleted before the replacement is frozen;
  - selected execution-target state is locked before commit;
  - source remains current and target is not inserted/started;
  - Approval replaces the source and service starts the target.
  - Store and service integration tests pass.
- Runtime goal-status regression agent completed. The fixture now supplies
  the required live `ScopeID`; `./scripts/test.sh ./server/runtimecontrol
  -count=1` passes.
- Removed `WorkflowTaskMoveRequest.AllowMissingEdge`; Go reflection and
  Desktop transport tests prove no `allow_missing_edge` or `auto_approve`.
- Protocol was bumped once from 71 to 72. Current handshake round trip,
  previous-generation rejection, and Desktop transport tests pass.
- Repaired stale Current Node tests in startup, worktree, registry, transport,
  CLI app, and core:
  - startup now proves admitted Current Node recovery before service;
  - terminal worktree deletion uses Current Node completion;
  - attention fixtures use Current Node identity;
  - core construction fixture has valid automatic concurrency;
  - DTO boundary allowance names the Current Node interruption DTO.
- Replaced global live-snapshot cloning with scoped immutable reads:
  - Authority has one nested project/workflow/task/current-node source;
  - `WorkflowExecutionRef` carries immutable Project/Workflow scope facts;
  - controller resolves scope under the mutation permit;
  - Task List, Board, Detail, and task Attention use narrow snapshots;
  - only global Attention uses the explicitly global snapshot.
  - focused `sessionruntime`, `workflowexecution`, `workflowview`,
    `workflowstore`, and `workflowrunner` packages pass.
- Public docs and embedded workflow skill were reverified after reading the
  docs-writing skill:
  - `pnpm --dir docs test`
  - `pnpm --dir docs build`
  - `pnpm --dir docs smoke:built`
  all pass.
- Corrected round-5 records: the unrelated append-recovery production rewrite
  is rejected/absent; only the equivalent batched transcript fixture remains.
- Rebuilt `.kent/evidence/KENT-334-cutover-checklist.md` as the current
  Task-body/original-plan requirement matrix.
- Current handwritten non-test production ledger against `3c7d45a62`:
  `files=207 added=10148 deleted=11569 net=-1421` (final recomputation still
  required).

### Focused verification reproduced

```text
./scripts/test.sh ./server/workflowstore -count=1
./scripts/test.sh ./server/workflowsvc -count=1
./scripts/test.sh ./server/runtimecontrol -count=1
./scripts/test.sh ./server/sessionruntime ./server/workflowexecution \
  ./server/workflowview ./server/workflowstore ./server/workflowrunner -count=1
./scripts/test.sh ./server/startup -count=1
./scripts/test.sh ./server/worktree -count=1
./scripts/test.sh ./shared/serverapi ./shared/client/... ./cli/kent -count=1
pnpm --dir apps/desktop lint
pnpm --dir apps/desktop typecheck
pnpm --dir apps/desktop test
pnpm --dir docs test
pnpm --dir docs build
pnpm --dir docs smoke:built
```

The attempted mandatory runtime aggregates overlapped an in-progress scoped
snapshot edit and therefore failed package compilation; they must be rerun
from the completed checkpoint. One aggregate also reached the 180-second cap.
Do not cite those interrupted runs as product failures or passing evidence.

### Active delegated work

- Workflow Runner product-boundary restoration:
  - agent `8600bdfe-558b-4062-86f5-f1fe9915221a`
  - shell `2138`
  - write scope `server/workflowrunner/**`
  - in-progress `current_node_integration_test.go`
  - an observed interim run failed because the fixture execution-target source
    workspace did not match the Task source workspace; the agent was steered
    with the exact failure and must be allowed to finish.
- Architecture-guard design review:
  - agent `3371a46f-4f8d-436a-8fd3-cd41f07ec4ff`
  - shell `2139`
  - read-only plan reviewer
  - must be polled and used to implement the positive guard matrix.

### Immediate next work

1. Wait for/review the two active agents above.
2. Restore remaining product-boundary coverage:
   - Workflow View list/board cursor pagination, sorting, Detail state matrix,
     Activity ordering/pagination, and Attention Question/pagination/query
     scope;
   - graph Save/Delete, Task/Workflow/Project deletion races, execution-target
     behavior, production composition;
   - Project Home proof that Current Node mutations advance owning Task
     activity ordering.
3. Implement positive structural guards with no path exclusions, symbol or
   identifier allowlists, grandfather lists, suppressions, or text-copy
   assertions. Add structured embedded Script example validation.
4. Run `./scripts/test.sh server`; repair every remaining failure and runtime
   cap rather than bypassing it.
5. Rerun both mandatory runtime aggregates after all write agents finish.
6. Run final scoped non-Rust verification, update the evidence matrix and
   authoritative checklist checkmarks, recompute the ledger, rebuild
   `./bin/kent`, verify stash contents, then drop `stash@{0}`.
7. Audit every current Task-body/original-plan/takeover-audit requirement and
   run `kent goal complete`.

### Agent completion update — 2026-07-26 16:07 CEST

- Workflow Runner agent `8600bdfe-558b-4062-86f5-f1fe9915221a` completed.
  `server/workflowrunner/current_node_integration_test.go` now covers fresh
  Agent startup, latest role/completion contract, durable Comment count,
  disposable Session cleanup, continuation Session reuse,
  compact-and-continue planning, and Script stdin/completion. Full
  `./scripts/test.sh ./server/workflowrunner -count=1` passes. The suite emits
  expected query diagnostics for absent pending Approvals but remains green.
- Architecture plan reviewer `3371a46f-4f8d-436a-8fd3-cd41f07ec4ff`
  completed. Full output is preserved at
  `/tmp/KENT-334-architecture-guard-review.txt`.
  - Recommended smallest compliant matrix: one repository-wide typed Go
    analyzer, one effective-schema/generated-query structural analyzer, and
    one Goldmark/typed-JSON embedded Script example analyzer.
  - No path exclusions, symbol/identifier allowlists, grandfather lists,
    suppressions, or prose searches.
  - Required prerequisite: replace the SQLite Session metadata
    `map[string]any` projection in `server/metadata/store.go` near line 2150
    with one private typed workflow-neutral document so the new-write shape
    can be guarded structurally.
  - Suggested files:
    `server/core/current_node_authority_structure_test.go`,
    `server/metadata/current_node_persistence_structure_test.go`, and
    `prompts/workflow_script_contract_structure_test.go`.

There are no active delegated agents at this checkpoint.

## Continuation checkpoint — 2026-07-26 17:47 CEST

### Completed in this slice

- Removed the stale `server/workflowscript` small-package exception.
  `./scripts/test.sh ./server/core -count=1` passed.
- Added `TestServiceWorkflowTaskDeleteWaitsForConcurrentWorkflowMutation`.
  It proves Delete remains behind the shared mutation permit, leaves the Task
  readable while blocked, and removes it only after permit release.
- Closed remaining active Desktop Task-Cancel/Run drift:
  - Desktop rejects the removed `canceled` Task project-event action;
  - removed unused Task Cancel, canceled status, Runs/Transitions, and
    Run-interruption i18n keys;
  - current Question event fixtures use Session/Question identities.
- Renamed the active metadata board-query CTE from
  `effective_board_placements` to `effective_current_nodes`; rendered
  `queries.sql`, regenerated sqlitegen, and passed querygen/sqlitegen tests.
- Deleted unused `ContextSourceNoCompletedRunError` and its duplicate
  Context Source kind, and removed remaining current-path completed-Run
  wording from store/attention comments and workflow docs/prompt guidance.
- Extended the typed Current Node architecture guard to prove:
  - exactly one production Mutation Permit, Authority, Runner, controller,
    workflow service, and Project workflow-execution wiring call;
  - Runner/controller/services receive the same permit;
  - Runner/controller receive the same Authority;
  - the controller receives the sole Runner;
  - Authority retirement, Project service, Workflow service, and recovery
    reference the sole controller;
  - recovery precedes service wiring and Core composition.
- The composition audit found a real shutdown-order defect:
  normal Core close shut the Authority before the controller, although
  `CurrentNodeController.Close` needs the Authority to stop exact scopes.
  Reordered cleanup to controller → Runner → Authority → metadata and added
  `TestComposeBundlesClosesWorkflowExecutionBeforeAuthorityAndPersistence`.

### Reproduced verification

```text
./scripts/test.sh ./server/workflowsvc ./server/projectview \
  ./server/workflowview ./server/core ./server/metadata -count=1
PASS (131.283s)

./scripts/test.sh ./server/runtime ./server/runtimeview ./server/session \
  ./server/projectview ./shared/clientui -count=1
PASS (126.414s; runtime package 145.172s)

./scripts/test.sh ./server/runtime ./server/workflowruntime \
  ./server/workflowrunner ./server/metadata/sqlitegen -count=1
PASS (106.611s; runtime package 127.628s)

pnpm --dir apps/desktop test -- clientWorkflowSubscriptions.test.ts
PASS (30 files, 160 tests)

./scripts/test.sh ./server/workflowstore ./server/workflowattention -count=1
PASS

./scripts/test.sh ./server/metadata/querygen ./server/metadata/sqlitegen -count=1
PASS

pnpm --dir docs test
PASS
```

### Environment incident and remaining timeout

- A stale orphan `perl -0pi` process from the old cutover had been CPU-bound
  for about 46 hours and still targeted
  `server/metadata/querysrc/queries.sql.tmpl`. The User explicitly authorized
  terminating it and its shell parent. The file's Git/SHA-256 hashes were
  unchanged across termination.
- An external Rust workspace build was observed after the restart. It ended
  without changing tracked Rust source. It is not KENT-334 verification and
  must not be rerun; Rust remains frozen.
- `./scripts/test.sh server` still reached the 180-second cap in
  `server/runtime` near the worktree-reminder tests. Individual required
  aggregates pass, so the remaining root is default `-p 18` whole-server
  package contention against the long runtime suite, not a failing assertion.
  Do not disable or raise the cap. Next, run the full server target with
  supported lower package-parallelism values (start at 8, then 4) to find the
  stable concurrency point, then make the test harness default robust and
  rerun the exact unqualified server target. Do not treat an environment
  override as final evidence.

### Immediate next work

1. Resolve the full `./scripts/test.sh server` cap at its root, then rerun it.
2. Run final non-Rust checks:
   `./scripts/test.sh server desktop`,
   `./scripts/build.sh server desktop --output ./bin/kent`,
   `./scripts/ci-check.sh deps`,
   `./scripts/ci-check.sh frontend-lint`,
   `./scripts/ci-check.sh vet`, and `gofmt -l .`.
3. Recompute the production ledger against `3c7d45a62`.
4. Confirm intended client/docs/test edits remain, then drop `stash@{0}`;
   never reapply it.
5. Update the evidence matrix and both plans only from reproduced evidence.
6. Audit Task body/comments/plans, then run `kent goal complete`.

No delegated agents are active.

## Final completion checkpoint — 2026-07-26 18:05 CEST

### Timeout root cause and harness repair

- Whole-server `-p 18` execution exceeded the fixed 180-second test-runtime
  cap through host/package oversubscription; no assertion failure was observed.
- `KENT_TEST_GO_PACKAGE_PARALLELISM=8 ./scripts/test.sh server` passed.
- `scripts/test.sh` now caps auto-detected Go package parallelism at the proven
  stable maximum of 8 while preserving explicit overrides and the 180-second
  cap.
- The exact plain `./scripts/test.sh server` target then passed.

### Final reproduced non-Rust verification

```text
./scripts/test.sh server desktop
PASS (Desktop: 30 files, 160 tests)

./scripts/build.sh server desktop --output ./bin/kent
PASS

./scripts/ci-check.sh deps
PASS

./scripts/ci-check.sh frontend-lint
PASS

./scripts/ci-check.sh vet
PASS

gofmt -l .
PASS (no output)

pnpm --dir docs build
pnpm --dir docs smoke:built
PASS
```

`./bin/kent` was refreshed at version 2.4.0. The final handwritten non-test
production ledger against `3c7d45a62` is 206 files, `+10908 / -13520`, net
`-2612`, with production-patch SHA-256
`ef10dd89a4d1d9f99a5495068f105f72397124236bdc324879ec7a4f800743f2`.

The already-applied takeover stash was confirmed present in current
client/CLI/docs/prompt/test edits and then dropped. Rejected Session
append-recovery production files are clean; the 650-record batched fixture
remains. Rust is unchanged and was not verified.

All current Task-body, original-plan, and takeover-audit implementation/check
items are reconciled in `.kent/evidence/KENT-334-cutover-checklist.md`.
Manual QA and deployment were not run because the takeover goal explicitly
excludes them. No delegated agents are active.
