# KENT-334 Cutover Checklist

User direction: complete the mandatory server/API Current-Node cutover before
splitting lower-coupling client work. Task Cancel is in scope and must be
removed without a Current-Node compatibility lifecycle. Maintain a demolition
ledger and keep handwritten non-test production code net-negative against
integrated base `3c7d45a62`.

- [ ] Current Node scheduling: atomic eligibility, start, resume, task/session interrupt drain, forced completion, move, approval.
- [ ] Remove Task Cancel end to end; migrate legacy canceled Tasks once to
      terminal Current Nodes or approved workflow-neutral deletion.
- [ ] Remove scheduler, automatic-registration, Run runtime control, and Run-based runner paths from production composition.
- [ ] Finish Exact Scope question issuance, answer, clearing, and attention.
- [ ] Replace Run/Placement workflow status, board, task detail, activity, and attention read models with Current Node/retained Session projections.
- [ ] Lock the public Go server API and server transport contracts to Current Node/Session DTOs.
- [ ] Cut the Go remote client, CLI, and Desktop after the server API checkpoint; bump the active Go/Desktop protocol once after those shapes are locked.
- [ ] Remove Run identity from runtime/transcript/compaction/tool/script/session contracts and new writes.
- [ ] Ship one hard migration dropping Run/Placement/history tables, views, indexes, generated queries, and obsolete metadata.
- [ ] Delete Run/Placement domain/store/query/API/client/test code and compatibility paths.
- [ ] Regenerate query/protocol artifacts, update guards, then pass relevant builds, tests, migration tests, and manual QA.

Current evidence:

- Direct Session ownership and persisted Current-Node inspection are present.
- Start response now returns Current Nodes. Broad test-package compilation is
  not green yet; stale Run-based tests remain in lifecycle and read-model
  packages.
- The in-progress Complete API now targets live agent Sessions or one idle
  Current Node selected by Session/Task; it has no Run selector. It still needs
  focused tests and coordinated Go client/read-model cutover.
- 2026-07-24: the forced-idle selector is resolved inside the controller's
  shared mutation permit before quiescence and completion, so a concurrent
  admission cannot turn a selected idle Node live between selection and commit.
- 2026-07-24: `./scripts/test.sh ./server/workflowexecution -run
  '^TestCurrentNodeController' -count=1` passed after the controller completion
  change. This command was independently reproduced during plan validation.
- Added `00064_current_node_workflow_status` to derive task-detail/list status
  from Current Nodes and pending Approvals. The migration remains additive WIP
  until the one hard cutover migration replaces it.
- Began board-query migration: `ListBoardColumnTaskCounts` and `ListBoardNodeTasks` now select Current Nodes. `Board.placementsByTask`, task-list positions, worktree lifecycle counters, and every remaining Run/Placement projection still need conversion before deleting legacy tables.
- Generated SQLC output changed after query edits. Record the exact generation
  command and result before treating regeneration as verified.
- 2026-07-24 plan validation: `git diff --check` passed. Compile-only test
  loading across `workflowrunner`, `workflowsvc`, `sessionruntime`, `runtime`,
  and `workflowview` failed because their tests still construct removed
  Run-based contracts. `workflowexecution`, `workflowstore`, and `core`
  compiled under the same check. This is remaining cutover work, not a green
  package-build claim.
