# KENT-334 server/API cutover

- [x] Remove scheduler, automatic-registration, and fatal workflow-execution startup coupling.
- [x] Remove legacy Run-owned runner and script execution paths.
- [x] Remove the server Task Cancel route, service contract, and shared request DTO.
- [ ] Replace Run/Placement status, board, detail, attention, and activity projections with Current Node/controller state.
- [ ] Delete Run/Placement store/query/schema/history paths and regenerate metadata.
- [ ] Replace deleted Run tests with Current Node integration coverage; run focused server verification.
- [ ] Audit the completed server/API checkpoint, build it, and commit only when coherent.

Checkpoint verification on July 24, 2026:

- `go build ./server/...` passed.
- `./scripts/test.sh ./server/workflowsvc ./server/workflowexecution ./shared/serverapi -run 'Test(ServiceTaskStart|AnswerWorkflowTaskQuestion|CurrentNodeController)' -count=1` passed.
- `git diff --check` passed.
- Full workflowstore/startup/runtime test-package compilation remains intentionally incomplete because stale Run tests have not all been deleted/replaced.

Current in-progress note:

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
