# KENT-334 server/API cutover

## Review round 3 continuation — July 25, 2026

- [x] Remove stale Task Cancel/Run help text and stale sqlitegen `GetTaskRun`
      diagnostics tests.
- [x] Rename remaining workflow-specific runtime `WorkflowRun` seams to
      Current-Node execution configuration without changing generic runtime
      Run terminology.
- [x] Make workflow prompt source identity branch-aware for parallel Current
      Nodes and cover compaction/reopen assignment behavior.
- [x] Bring the broad runtime/runtimeview/session/projectview/client-ui suite
      below the required 180-second cap.
- [x] Regenerate, re-run focused/full verification, update the ledger, and
      commit the third review-fix round.

## Review round 4 continuation — July 26, 2026

- [x] Replace remaining Current-Node runtime error and completion-contract
      terminology that still says “workflow run” or “run-start snapshot”.
- [x] Replace the negative identifier/path-exclusion architecture guard with
      positive current-node control-contract and legacy-decoder fixtures.
- [x] Restore pure completion-mode/schema/decoder coverage while deleting only
      obsolete Run-controller tests.
- [x] Reproduce and fix the exact broad-suite 180-second timeout.

Evidence: the review reproduced an exact broad-suite cap failure after a fresh
round-3 local pass. A subsequent focused runtime run measured
`core/server/runtime` at 153.115 seconds; the named worktree-reminder test
itself completed in 0.55 seconds, so it is only where the cumulative cap was
reached, not the root cause. Profile the aggregate runtime test cost and
remove the shared per-test delay/IO source rather than weakening the cap.

The runtime fixture was performing a second, no-op `EnsureDurable` metadata
mutation immediately after `session.Create`, which already returns a durable
observed Store. Removing that duplicate persistence operation retains the
durable production contract while removing its repeated test-fixture I/O.
After the change, the required exact command passed with the normal harness
settings in 94.251 seconds; a constrained two-package-parallelism diagnostic
run also passed in 116.198 seconds. No cap or test selection was changed.

## Review round 5 continuation — July 26, 2026

- [x] Reject and remove the unrelated production append-recovery rewrite from
      KENT-334.
- [x] Batch the large active-segment transcript fixture through the production
      atomic append contract instead of one durable transaction per entry.
- [ ] Re-run both mandatory aggregate commands below the unchanged cap during
      final verification.

The production append-recovery rewrite was not established as necessary: a
later clean review passed both mandatory aggregates without it. The takeover
therefore removed that unrelated durability-protocol change and retained only
the batched 650-entry active-segment fixture, which preserves the same product
assertion with fewer fixture transactions.

The claimed lack of human authorization for the inherited `AGENTS.md` Frozen
Rust edit conflicts with the recorded direct human decision supplied to this
agent on July 25, 2026. Preserve the explicit human-approved decision unless
the human revokes it directly.

Decision: the inherited `AGENTS.md` Frozen Rust diff is an explicitly
human-approved edit as of July 25, 2026. Preserve it unchanged; do not revert
or otherwise alter `AGENTS.md` without a new explicit human authorization.

## Review round 2 completion — July 24, 2026

- [x] Make the hard cutover migration transactional and reject irreversible
      down migration without changing the Goose version.
- [x] Keep pending Approval frozen target edges graph-edit protected.
- [x] Use one canonical user-interrupt reason across migration, status, and
      durable Attention suppression.
- [x] Restore serial forward executable Task-wide Move and its Current-Node
      coverage.
- [x] Make automatic-admission reservations visible to Interrupt and
      quiescence.
- [x] Remove workflow Run identity from current runtime/session state and
      transcript writes while preserving read-only legacy decode.
- [x] Route graph edit/save and Task/Workflow/Project deletion through the
      Workflow Execution mutation permit with commit-time snapshot revalidation.
- [x] Re-run focused server verification, metadata regeneration/schema dump,
      server build, and the demolition ledger.

### Current continuation notes

- Runtime/session Current-Node port now compiles across `server/runtime`,
  `server/session`, `server/runtimeview`, and `shared/clientui` without a
  workflow `RunID` compatibility type. Current workflow prompt source
  identity is the direct Task/Current-Node pair, not an Exact Scope ID, so
  new transcript writes do not smuggle process-local scope identity back into
  persisted workflow state. Focused runtime/client-ui checks pass. The last
  full runtime/client-ui run exposed two stale compaction assertions that
  expected Scope ID prompt identities; those tests were ported to direct
  Task/Current-Node identity. The full
  `./scripts/test.sh ./server/runtime ./server/runtimeview ./server/session
  ./shared/clientui -count=1` suite now passes.
- Added Current-Node Manual Move coverage in
  `server/workflowstore/manual_moves_test.go`: forward serial Agent and
  Script moves, missing/backward/parallel executable rejection, and
  pending-Approval supersession. Added service coverage for
  execution-target selection/retry, durable quiescence revalidation, and
  target Current-Node admission. Focused workflowstore/workflowsvc Manual
  Move checks pass.
- Fine-grained graph Node/Group/Transition/Edge mutations and Graph Save now
  share `runWorkflowGraphMutation`, which holds the existing permit and
  revalidates every Workflow Task immediately before its durable mutation.
  Workflow and Project deletion use the same permit and revalidation. Tests
  cover controller-owned queued/reserved/retirement-held/gated/live work,
  plus Graph Save, Workflow Delete, and Project Delete waiting for a
  concurrent permit holder.
- The server executable build now passes with
  `./scripts/build.sh --output ./bin/kent server`.
- Metadata query render, `sqlc generate`/sqlite annotation via
  `go generate ./server/metadata/sqlitegen`, and
  `./scripts/dump-metadata-schema.sh` pass. The effective schema retains
  Current Node and pending Approval relations while containing none of the
  removed Run/Placement/Transition tables or Task cancellation columns.
- Final focused package verification passes for metadata, workflow store,
  Workflow Execution, runner, service, project view, workflow view,
  workflow attention, session runtime, server API, and API contract. The
  runtime/session/client-ui package suite also passes in full.
- The current Go remote client and CLI Task Cancel references were removed as
  part of the server-executable cutover. Rust remains frozen and `AGENTS.md`
  is unchanged.

Review-round research:

- The migration fault is real: `00065` opts out of Goose transactions and its
  down migration is a successful no-op. Replace it with a transactional
  migration that does not rely on `PRAGMA foreign_keys = OFF` inside the
  transaction, and make the down path return a deliberate SQL error so Goose
  cannot falsely lower the version.
- The frozen pending-Approval edge fault is real. Both
  `CountTaskEdgeReferences` and `CountAllTaskEdgeReferences` currently count
  only `task_current_nodes.entered_by_edge_id`; they also must count
  `task_pending_approval_branches.target_snapshot_json` at
  `$.entered_by_edge_id`.
- The interrupt reason has two spellings. Migration and
  `workflowattention` use `user_interrupt`; controller/status/store
  suppression use `user_interrupted`. Establish the domain-owned canonical
  value and update every producer/consumer and migration fixture.
- `ManualMoveTask` currently accepts only Start/Terminal targets. The
  replacement must resolve exactly one serial Current Node and exactly one
  current forward edge for executable Agent/Script targets, preserve target
  materialized Context/Session facts through the latest definition, and
  produce a ready target Current Node for controller admission. Parallel
  executable, missing-edge, backwards/history-derived, and live/Question
  cases remain rejected.
- `automaticReservations` currently stores only an opaque reference key. Make
  it retain the exact Current Node reference, include it in quiescence, and
  drain it under Task-wide Interrupt after a live-scope authorization; the
  pending automatic goroutine must see its reservation removed before it can
  admit.
- Runtime/session Workflow Run removal is materially broader than the prior
  server-view slice. Current `server/runtime` terminal, compaction,
  `workflow_run_id` event serialization, `server/runtimeview`, and
  `shared/clientui` fields republish an Exact Scope ID as a workflow Run ID.
  Keep exact scope identity private/process-local; current writes and DTOs
  must move to Task/Workflow/optional Current Node facts while old event
  decode remains read-only.
- Graph mutation and project/workflow deletion serialization are also real.
  `workflowsvc` direct graph mutations and Save bypass
  `workflowexecution.RunMutation`; Workflow Delete has the permit but no
  controller quiescence revalidation; Project delete only uses its own lock
  and Session Authority. Route all destructive/mutating graph and
  Task/Workflow/Project operations through the shared controller permit and
  validate its one snapshot before the durable commit. Add deterministic
  intent/gate/live-scope races.
- The latest user instruction rejects deferrals. The stale
  `shared/client/remote.go` and CLI Task Cancel paths must now be removed to
  restore `./scripts/build.sh server`; do not run manual QA because the task
  explicitly assigns review/QA to separate agents. Do not edit frozen Rust or
  `AGENTS.md`: the Task’s current authority explicitly freezes Rust and the
  repository instruction forbids changing `AGENTS.md`.

- [x] Fix script finalization so completion runs while its Exact Execution
      Scope remains registered; add a successful Script Current-Node
      end-to-end test.
- [x] Make automatic admission reserve capacity before dequeueing and wake
      admission whenever capacity releases.
- [x] Make Interrupt persist durable interruption after request cancellation
      through controller-owned cleanup.
- [x] Derive completion shell selection from the Current Node's outgoing
      definition edges and reject forced shell without `exec_command`.
- [x] Reject the invalid compliance instruction to remove the User-approved
      `AGENTS.md` Rust freeze. `AGENTS.md` remains unchanged from `4d9c0fc2c`.
- [x] Replace/delete stale server tests until the focused server suite compiles.
- [x] Complete the hard Run/Placement/Transition/cancellation schema, store,
      query, and generated-binding cutover.
- [x] Replace legacy attention notification contracts/finalizer with
      Approval/Current-Node/Question identities.
- [x] Recompute the production deletion gate and keep it net-negative.

- [x] Remove scheduler, automatic-registration, and fatal workflow-execution startup coupling.
- [x] Remove legacy Run-owned runner and script execution paths.
- [x] Remove the server Task Cancel route, service contract, and shared request DTO.
- [x] Replace Run/Placement status, board, detail, attention, and activity projections with Current Node/controller state.
- [x] Delete Run/Placement store/query/schema/history paths and regenerate metadata.
- [x] Replace deleted Run tests with Current Node integration coverage; run focused server verification.
- [x] Audit the completed server/API checkpoint and build all server packages.

Checkpoint verification on July 24, 2026:

- `go build ./server/...` passed.
- `./scripts/test.sh ./server/workflowsvc ./server/workflowexecution ./shared/serverapi -run 'Test(ServiceTaskStart|AnswerWorkflowTaskQuestion|CurrentNodeController)' -count=1` passed.
- `git diff --check` passed.
- `./scripts/test.sh ./server/metadata ./server/workflowstore
  ./server/workflowexecution ./server/workflowrunner ./server/workflowsvc
  ./server/workflowview ./server/workflowattention ./server/sessionruntime
  ./shared/serverapi -count=1` passed within the harness cap.

Historical in-progress notes:

- `server/workflowrunner.Starter.newWorkflowProviderClient` now passes a
  provider-capabilities override only when one is actually configured or
  locked; a zero-value override was previously sent to runtime client
  factories.
- `server/workflowview/task_projector.go` and `task_status.go` have started
  the direct Current Node projection: task facts now accept Current Nodes plus
  live Authority executions and no longer decode Run IDs. This is deliberately
  mid-cutover and does not currently compile because `board.go`, `activity.go`,
  and `attention.go` still call the deleted Placement/Run/Transition
  projection helpers. Continue by replacing those consumers, then remove the
  old public DTOs and generated queries together; do not reintroduce the
  helpers as compatibility paths.
- July 24 continuation: `ListWorkflowTaskActivityRows` now has only the
  approved durable `comment` and `session_started` sources, and
  `activity.go` projects only those typed payloads. The public server DTO
  demolition has begun: Run/Placement/Transition Activity DTOs, `RunIDs`,
  `RunCount`, `CanCancel`, and canceled status fields were removed from
  `shared/serverapi/workflow.go`. This intentionally makes `workflowview`
  non-compiling until Board, Task Detail, Task List, and Attention are
  converted as one contract slice.
- Important query-source repair: before regeneration, the generated bindings
  in the inherited dirty worktree contained Current Node admission/binding
  queries that were missing from
  `server/metadata/querysrc/queries.sql.tmpl`. Regenerating exposed that
  source-of-truth violation. The template now owns:
  `entered_by_edge_id` current-node row/insert fields, batch
  `ListTaskCurrentNodesByTasks`, Current Node admit/resume/interrupt/recovery
  queries, and exact Session-to-Current-Node binding queries. Regeneration was
  run with `go run ./server/metadata/querygen render ...`, `sqlc generate`,
  and `go generate ./server/metadata/sqlitegen`; `go build
  ./server/workflowstore` passes. Do not restore generated-only SQL or
  hand-write production SQL.
- July 24 read-model continuation:
  - Board now batch-loads decoded Task Current Nodes via
    `DefinitionProjection.CurrentNodesByTask`, pairs them with one Authority
    snapshot, and has no Placement/Run action facts. Its count/card-list query
    source reads `task_current_nodes` directly with no canceled-terminal
    synthesis.
  - Task Detail now exposes Current Nodes, direct retained Session count,
    Authority-derived live Session IDs, and live script Current Node/path
    values resolved from the latest definition. Actions come from the shared
    Current-Node `TaskProjector`.
  - Task List takes one bounded Authority snapshot per operation and passes a
    typed JSON live-state input into generated SQL. Status filtering/sorting is
    overlaid before cursor application; Run count/IDs and canceled synthesis
    are gone from this query/token path.
  - Attention now merges durable `task_pending_approvals` and interrupted
    `task_current_nodes` with volatile prompt-registry Questions from Authority
    Exact Execution Scopes. The new generated
    `ListWorkflowDurableAttentionCandidates` query owns the durable SQL.
  - `CurrentWorkflowTaskExecutionSnapshots` was added to
    `sessionruntime.Authority` for those server read models.
  - `00064_current_node_workflow_status.up.sql` no longer synthesizes canceled
    state or `run_ids_json`; it still must be superseded by the final hard
    cutover migration.

Latest local verification:

- Query render, `sqlc generate`, and `go generate ./server/metadata/sqlitegen`
  passed.
- `go build ./server/workflowview`, `go build ./server/core`, and
  `go build ./server/workflowstore` passed.
- `git diff --check` passed.
- Do not claim broad tests: retained Run-oriented tests have stale
  constructors/fixtures and are not yet replaced.

Review-fix status:

- User instruction July 24: do not delegate and do not ask further questions;
  continue locally until the whole KENT-334 goal is complete. The active
  `kent goal` records that complete goal and its server-only constraints.
- Valid review fixes now in the working tree:
  - Script process retirement removes the workflow liveness index before its
    finalizer, but retains the exact scope until completion commits. A
    CurrentNodeController test proves a successful Script completion starts
    its successor.
  - Automatic admission owns an in-memory reservation before removing an
    intent from the queue and wakes whenever capacity is released. Controller
    coverage proves a concurrency-one queue admits only one item and retries
    the next item after retirement.
  - After an Interrupt has marked scopes stopping, durable interruption uses
    a controller-owned 30-second background cleanup context rather than the
    caller context. Coverage holds the store write through caller expiry and
    proves the durable interruption is still committed.
  - `CurrentNodeStartContext` derives
    `HasContinueSessionOutgoingEdge` from the latest definition; starter and
    persisted inspection consume that typed fact. Forced `shell_command`
    selection now requires `exec_command`.
- Focused checks that pass in this worktree:
  `./scripts/test.sh ./server/workflowexecution ./server/workflowrunner
  -count=1`; focused workflowstore migration/context tests; focused
  workflowsvc construction; focused shared/serverapi task-detail validation;
  and `go build ./server/core ./server/workflowrunner ./server/workflowstore
  ./server/workflowview ./server/workflowsvc ./cmd/dumpmodelrequest`.
- At this historical checkpoint, the complete focused suite remained red. The explicitly deferred
  `shared/client/remote.go` still imports the removed cancel request through
  runtime/core/startup; do not restore that DTO. Most importantly, production
  Run/Placement/Transition/Cancel schema/store/query and attention-finalizer
  authority remains and must be removed before any completion claim.

July 24 continued hard-cutover work (uncommitted):

- Legacy Run-owned store source files have been deleted locally:
  `runs.go`, `run_records.go`, `transitions.go`, `joins.go`,
  `executable_runs.go`, `context_sources.go`, `protocol_violations.go`, and
  `run_snapshot.go`. `tasks.go` and `manual_moves.go` are now Current-Node
  implementations. `go build ./server/workflowstore` passed immediately after
  regeneration. The old production data types have been removed from
  `workflowstore.Store` contracts. Do not restore them for stale tests.
- Attention notification code is mid-replacement: `clientui` and
  `serverapi` now name Approval UUID and interrupted Current Node focus rather
  than Task Transition/Run identity; `workflowattention.Finalizer` was
  rewritten around `ApprovalID` and `CurrentNodeReference`; the core provider
  and service use the new contracts. `go build ./server/workflowsvc
  ./server/core ./server/workflowattention ./shared/serverapi
  ./server/workflowview` passed before the next query/public-count slice.
- Important follow-up: replace stale `workflowattention` and
  `shared/serverapi` notification tests with Current-Node tests; the old tests
  compile against removed contracts and must not be kept.
- Next coupled task: replace graph-edit/delete impacts and their query source
  from Placement/Run counts to Current-Node/Pending-Approval facts, then
  remove the corresponding old query blocks and regenerate. The attempted
  broad public-count patch was not applied; files are still in the buildable
  state described above.

- `Starter.StartCurrentNode` binds an agent Session through
  `BindSessionToCurrentNode`, the one transaction that writes direct Task
  ownership, the exact Current Node's `session_id`, and the retained
  Session/Node association. It does not call `AttachRunSession`.
- `ResolveCurrentSessionStartContext` now resolves direct Session Task
  ownership, the Session-bound Current Node, its direct association, and the
  latest Workflow definition. It contains no Run lookup or frozen Run snapshot
  fallback.
- `dumpmodelrequest` treats `ErrSessionNotCurrentWorkflowNode` as the
  ordinary retained-Session path while retaining Task-origin policy lookup.
- Start's applied response validates `current_nodes`; the removed
  Transition/Placement/Run payload requirement is absent.
- `./scripts/build.sh --output ./bin/kent server` remains blocked by the
  intentionally untouched Go remote client still referring to the removed
  `WorkflowTaskCancelRequest`. Server package builds succeed; do not restore
  that server contract merely to make the later client compile.

July 24 hard-cutover continuation (uncommitted):

- Added forward `00065_remove_workflow_run_history`. It drops legacy
  Run/Placement/Transition tables, views, and runtime triggers; drops Task
  cancellation columns; recreates `task_records`,
  `workflow_task_status_records`, and Current-Node/direct-Session triggers.
  `./scripts/dump-metadata-schema.sh` passes and its effective schema contains
  none of the dropped relations, legacy views, or cancellation columns.
- Removed every legacy Run/Placement/Transition/Cancel query from
  `server/metadata/querysrc/queries.sql.tmpl`, regenerated
  `queries.sql`, sqlc bindings, and sqlitegen. Graph edit/delete impact now
  uses Current Nodes and pending Approvals, with no Run/Placement counters.
  Pending Approval snapshots block destructive node removal but do not block
  a mutable edge configuration change: those snapshots are self-sufficient
  and completion must retain their frozen configuration.
- Converted metadata project/workspace blockers and Project Home activity
  from legacy tables to Current Node/retained facts. Removed the now-unused
  workflowview legacy source projection.
- `go build ./server/... ./shared/serverapi ./shared/clientui`,
  `./scripts/test.sh ./server/workflowstore -count=1`,
  `./scripts/test.sh ./server/workflowattention ./shared/serverapi -count=1`,
  query generation, schema dump, and `git diff --check` pass.
- Before the metadata-test cleanup below, the full combined server command was red and exceeded the harness cap
  because old metadata migration/schema tests still assert deleted legacy
  tables/views and pre-00065 outcomes. This is stale test cleanup/replacement,
  not a production compile failure. Current examples include
  `server/metadata/db_test.go`, `store_test.go`, and
  `workflow_schema_test.go`. Delete or rewrite their legacy-only cases; do
  not retain compatibility schema or restore dropped relations merely to make
  them pass. The deferred Go remote client still prevents `runtime`, `core`,
  and `startup` test compilation by importing the removed Cancel request.
- The reproducible handwritten-production classifier now reports
  `added=8326 deleted=11715 net=-3389` against `3c7d45a62` with the plan's
  exclusions. Recompute after any material changes.

July 24 metadata-test continuation (uncommitted):

- `./scripts/test.sh ./server/metadata -count=1` is green. The metadata
  migration/schema tests now treat Run/Placement/Transition/Cancel relations
  as historical inputs only and assert that `00065` leaves no legacy relation
  or queryable `task_runs` table. Legacy executable fixtures now include their
  actual entering edge so the historical `00063` migration contract remains
  valid. Pure history-retention tests were deleted rather than preserving
  dropped tables/views. Project/workspace blocker and query-plan tests now
  seed Current Nodes and current status facts.
- `TestStalePredecessorFinalizationCannotRemoveResumedSuccessor` was an
  unbounded self-respawning test: its finalization callback identified both
  predecessor and successor solely by their equal Current Node reference.
  It now captures and checks the predecessor exact Scope ID. This expresses
  the intended stale-finalizer race and makes
  `./scripts/test.sh ./server/sessionruntime -count=1` green.
- The previous combined focused server command exceeded the harness cap while
  `sessionruntime` was stuck in that test. Rerun it after completing the
  still-missing workflowview Current Node boundary coverage.
- Remaining server checkpoint work: add replacement Current Node boundary
  tests for workflowview Board, Task Detail, Task List, Activity, and
  Attention; expand workflowattention notifications beyond approval-only;
  then rerun the focused server suite. The deferred Go remote client Cancel
  import remains intentionally outside this server checkpoint.

July 24 server/API checkpoint completion:

- `server/workflowview/current_node_boundary_test.go` proves Board Get/card
  listing, Task Detail, Task List, Comments-plus-retained-Session Activity,
  and durable Approval/interrupted-Current-Node Attention only through
  Current Node/direct Session authority. The Attention query now projects
  nullable Approval identity through a real left join, so interrupted rows
  cannot fail SQLite scanning.
- `server/workflowattention/current_node_notification_test.go` proves pending
  Approval focus and interrupted Current Node identity/focus, then proves
  resolution idempotence suppresses a later pending publication.
- Query render, `sqlc generate`, `go generate ./server/metadata/sqlitegen`,
  `./scripts/dump-metadata-schema.sh`, `go build ./server/... ./shared/serverapi
  ./shared/clientui`, the focused server checkpoint command above, and
  `git diff --check` pass.
- The effective dumped schema contains no `task_runs`,
  `task_node_placements`, `task_transitions`, cancellation columns, or legacy
  cancellation fields. Legacy relation names remain only in historical
  migration fixtures.
- The Go remote client and CLI still reference the removed Task Cancel
  request. They remain explicitly deferred lower-coupling client work; no
  server compatibility DTO was restored and no executable/manual QA claim is
  made for this server/API checkpoint.
