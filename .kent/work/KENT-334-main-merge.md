# KENT-334 main integration — 2026-07-28

PR: `#654`

The User requested merge. `origin/main` at `35cf25ea3` was merged into
`KENT-334`, then `origin/main` advanced through PR `#653` and was integrated
again at `53aa3ea09`. The second normal merge is resolved and staged. Do not
abort, reset, rebase, or restore the deleted Workflow Run/Placement scheduler.

## Locked conflict decisions

- Preserve KENT-334 Task-owned Current Nodes, controller gates, Authority
  `WorkflowExecutionLease`, queued/running/finalizing phases, and complete
  deletion of Workflow Run, Placement, transition history, and Task Cancel.
- The upstream `PrepareWorkflowRun`/legacy scheduler changes target the deleted
  model. Do not restore those files or APIs.
- KENT-334 already gates durable admission and prevents execution until the
  controller swaps its gate to a live scope and releases the Authority lease.
- Port the independent upstream Session-store admission deadlock fix:
  `WithSessionStore` releases its callback count under the admission gate and
  attempts resource retirement only after releasing that gate.
- Preserve upstream runtime test sharding/admission-lock infrastructure.
- Preserve upstream label-exclusion behavior while adapting it to Current Node
  read models and the replacement test organization.

## Local resolution completed

- Restored KENT-334 versions of:
  - `server/sessionruntime/{authority,authority_execution,authority_resource,authority_test,script,task_targets}.go`
  - `server/workflowexecution/contracts.go`
  - `server/workflowrunner/{starter,script_runner,session_recovery_test}.go`
  - `server/workflowstore/store.go`
- Re-deleted legacy files modified upstream:
  - `server/workflowexecution/service.go`
  - `server/workflowexecution/service_test.go`
  - `server/workflowrunner/runtime_client_factory_test.go`
  - `server/workflowrunner/scheduler_execution_test_aliases_test.go`
  - `server/workflowrunner/scheduler_service_test.go`
  - `server/workflowrunner/starter_test.go`
  - `server/workflowstore/runs.go`
- Kept upstream `server/sessionruntime/authority_store.go` and adapted
  `agentResource.withStoreUnderAdmission` / `releaseCallbackCount`.

## Completed delegated slices

- Runtime/test-sharder conflict slice:
  session `34cf0ece-1b6a-4971-8b17-1bbf5a5565c5`.
  A continuation removed every reintroduced legacy Workflow Run/Placement API
  and restored Current Node test contracts. Focused Runtime tests passed.
- Metadata/label/read-model conflict slice:
  session `325fead1-a424-4c77-97bc-3460c20d3e33`.
  It preserved label exclusion semantics, regenerated SQLite queries, ported
  coverage into KENT-334 replacement tests, and kept deleted legacy suites
  deleted. Metadata, Workflow View, Workflow Service, query-generator tests,
  and a scoped build passed.

Both agents completed without commits. Review their diffs before integrated
verification.

## Remaining integration gates

1. Add or port a Current Node regression for the upstream active-transcript
   continuation deadlock fixed by `WithSessionStore`. **Completed:**
   `TestCurrentNodeContinuationWithActiveTranscriptSubscriberDoesNotBlockLaterAutomaticScript`
   keeps a real transcript subscription active across a `continue_session`
   successor, then proves an unrelated automatic Script successor starts after
   the continued scope releases automatic capacity. Reverting the production
   lock-order fix made the test fail and time out with
   `WithSessionStore -> releaseCallback -> closeRetiringResource` waiting on
   the already-held Session admission gate; restoring the fix passes.
2. Confirm no conflict markers and no legacy Workflow Run/Placement/Task
   Cancel production or test APIs were restored.
3. Run focused Session Runtime, Workflow Execution, Workflow Runner, Metadata,
   Workflow View, Workflow Service, Runtime, CLI, and Desktop tests.
4. Run the exact non-Rust matrix. The upstream sharder may make the ordinary
   `./scripts/test.sh server desktop` command fit the cap; do not use
   `--no-wall-clock-cap`.
5. Rebuild server/desktop, run CI checks, docs test/build/smoke, formatting,
   and frozen-Rust checks.
6. Because this integration touches lifecycle/read-model surfaces, continue
   the same original review sessions until all are GREEN:
   - architecture/correctness `45134d2a-e206-4f36-8d05-e5474c13b591`
   - concurrency/lifecycle `d243af7f-058e-4e8b-978d-b30d7f08ffa9`
   - migration/API/read models `5b45b4dc-1714-42a1-b238-1b54bb1e1570`
7. Commit the main integration, push `KENT-334`, wait for required PR checks,
   then merge PR `#654` using an allowed repository method.

## Integration review checkpoint

- Runtime/test-sharder slice review: **GREEN**. No restored legacy Workflow
  Run, Placement, history, or Task Cancel APIs; Current Node runtime contracts
  and the upstream sharder/admission lock remain intact.
- Metadata/label/read-model slice review: **GREEN**. Generated SQL argument
  ordering and exclusion semantics are consistent across Current Node board
  counts/cards and Task List reads; CLI/Desktop/API plumbing preserves the
  shared included/excluded Label-expression contract.
- Focused Session Runtime, Workflow Execution, Workflow Runner, and Workflow
  Store suites pass together after the regression and production fix.

## Final merge verification

- The regression's first version exposed a SQLite setup race under repetition.
  It now waits for the continued successor's blocked model turn before
  creating unrelated workflow data and passes ten consecutive runs.
- `TestGoalAuthorityLiveSetUsesRuntimeCommand` now counts typed Goal feedback
  and Goal status events rather than unrelated Runtime-open events and passes
  twenty consecutive runs.
- Exact gate:
  `go clean -testcache && ./scripts/test.sh server desktop` — **PASS** in
  134.724 seconds under the ordinary cap.
- `./scripts/build.sh server desktop --output ./bin/kent` — PASS.
- `./scripts/ci-check.sh deps` — PASS.
- `./scripts/ci-check.sh frontend-lint` — PASS.
- `./scripts/ci-check.sh vet` — PASS.
- `pnpm --dir docs test`, `build`, and `smoke:built` — PASS.
- Conflict, marker, architecture, formatting, diff, and frozen-Rust checks are
  clean.
- All three original reviewer sessions are **GREEN** against the latest staged
  worktree.
- KENT-owned handwritten production delta against merged `origin/main`:
  `+12233 / -14053`, net `-1820`.

## Second main advancement — 2026-07-28

The verified first integration was committed and pushed as
`bed985bd9 chore: merge main into KENT-334` with parents `a86ae8963` and
`35cf25ea3`. PR checks passed, but `main` then advanced to `53aa3ea09` through
PR `#653` (`KENT-309`, Narrow metadata transaction scope).

A second `git merge --no-ff origin/main` is now in progress. Do not abort,
reset, or rebase it. The merge began with conflicts in:

- `server/metadata/store.go`
- `server/metadata/store_test.go`
- `server/projectview/service.go`
- `server/projectview/service_test.go`
- `server/workflowstore/graph_edit_policy.go`
- `server/workflowstore/graph_save.go`
- `server/workflowsvc/service.go`

Resolution invariant: preserve KENT-334 Task-owned Current Nodes and deleted
Workflow Run/Placement/history/Task Cancel mechanisms while integrating
KENT-309's narrowed transaction scopes, prepared-state invalidation,
authoritative commit-time revalidation, and keyed mutation lanes. Do not
restore legacy Run blocker counts or Run-based graph impact.

After resolution, rerun focused metadata/project view/workflow store/workflow
service/workflow view/worktree/client suites, the exact clean-cache
server/Desktop gate, build/CI/docs/static checks, and all three original
reviewer sessions. Then commit and push this second merge, wait for the new PR
checks, and merge PR `#654`.

## Second merge resolution checkpoint — 2026-07-28

All seven conflicts are resolved and staged. The integration keeps Task-owned
Current Nodes and the deleted Workflow Run/Placement/history/Task Cancel
architecture while porting KENT-309's transaction-scope work:

- Metadata Workspace Unlink prepares runtime guards and the Session-ID set
  outside the write transaction, then prefers authoritative commit blockers
  over typed preparation invalidation.
- Project View uses shared keyed Project mutation lanes and checks runtime
  activity both before and after blocking Session starts.
- Project Delete's KENT-334 reversible artifact lifecycle is preserved in
  `workflowstore`: recovery, validation, and staging now happen outside the
  write transaction; authoritative Current Node blockers win over Session-set
  invalidation; every failed commit restores staged artifacts; successful
  commits finalize outside the transaction.
- Workflow graph Save prepares an immutable structural descriptor, re-reads
  Current Node/Approval/Task references under the commit lock, and returns the
  exact committed definition and validation results. No Run or Placement
  impact query was restored.
- Script-aware definition evaluation lives in the existing
  `server/workflowscript` owner. It composes Workflow Definition validation
  with Script path validation without making `workflow` import
  `workflowscript`.
- Shared keyed mutation lanes replace the duplicated Project and Worktree lock
  implementations. Session-ID set equality is centralized in Metadata.

Focused verification passed:

- metadata and Project View;
- Metadata query generator and SQLite query-plan tests;
- Workflow Store, Workflow Service, Workflow View, Workflow Script, request
  memo, and Metadata Session-ID set validation;
- Session Runtime, Workflow Execution, Workflow Runner, Runtime, and Runtime
  Command;
- Workflow, Worktree, and shared client.

No unresolved paths or conflict markers remain. Production architecture scans
find no restored `PrepareWorkflowRun`, `SchedulerRuntimeStarter`,
`CountUnresolvedTaskRunsAtWorkflowNode`, Run/Placement impact fields, or Task
Cancel APIs.

## Second merge final verification — 2026-07-28

The three original reviewer continuations found and verified fixes for:

- graph Save returning preparation-time active Current Node and pending
  Approval counts after commit-time dynamic revalidation;
- Project Delete returning preflight blockers before recovering a stale
  present-Project Session artifact tombstone;
- Session-set equality accepting duplicates on one side.

The User rejected small-package exceptions. Script-aware Workflow Definition
evaluation now lives in the existing `server/workflowscript` owner, and
Session-ID set equality lives in Metadata. The small-package architecture
guard passes without new allowlist entries.

Verification:

- all three original reviewer sessions are **GREEN** against the latest staged
  merge;
- focused Metadata, Project View, Request Memo, Workflow Script, Workflow
  Store, Workflow Service, Workflow View, Worktree, shared client, and Runtime
  regressions pass;
- the ordinary clean-cache server/Desktop gate was assertion-clean but
  cap-incomplete, reaching the final tiny packages at 183.685 seconds;
- the User explicitly deferred the scheduler cap on 2026-07-28;
- `./scripts/test.sh server desktop --no-wall-clock-cap` passed in 162.036
  seconds, including 35 Desktop files / 173 tests;
- `./scripts/build.sh server desktop --output ./bin/kent` passed;
- dependency policy, frontend lint, Go vet, docs test/build/built-site smoke,
  formatting, diff, conflict-marker, architecture, and frozen-Rust checks all
  pass;
- KENT-owned handwritten production delta against `53aa3ea09` is 195 files,
  `+12343 / -14088`, net `-1745`, production-patch SHA-256
  `3e3d771102f3fe188469f8fe5d2280506b139edc22bc5cc5a1f0277842c42783`.

Remaining work is the normal second merge commit, push, PR checks, and merging
PR `#654`.
