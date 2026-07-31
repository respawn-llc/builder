## Recon

- Product authority is in the dependency sections of `docs/dev/specs/workflow-orchestration.md`, `docs/dev/specs/desktop-gui.md`, and `docs/dev/specs/terminology.md`. The adjacent locked behavior includes canonical Task status/done projection, Terminal Node satisfaction, manual movement, Task Comment awareness, server-authoritative Desktop state, and no client-derived lifecycle truth.
- The ticket comments supersede earlier design notes. The current decisions recorded there include same-Project cross-Workflow relationships, no relationship UUID or reverse copy, no direct reciprocal pair, no transitive traversal, 50 direct edges per direction with complete unpaginated reads, aggregate-only start warnings, `View deps` targeting the blocked Task, ordinary New Task form navigation with a typed pending relationship parameter, and KENT-355/KENT-356 boundaries.

### Persistence and generated query seams

- Metadata migrations live in `server/metadata/migrations/`; the current head is migration `00060_task_current_state_cutover`. Generated models are in `server/metadata/sqlitegen/models.go` and generated query adapters in `server/metadata/sqlitegen/queries.sql.go`.
- Authoring inputs are `server/metadata/querysrc/queries.sql.tmpl` and `server/metadata/queries.sql`; the query generator is under `server/metadata/querygen`/`server/metadata/sqlitegen`. Existing query patterns include explicit transaction-scoped `Queries.WithTx`, `ON CONFLICT ... DO NOTHING` idempotency, project-row write locks such as `AcquireTaskLabelWriteLock`, and indexed board queries such as `ListBoardNodeTasks` in `server/metadata/querysrc/queries.sql.tmpl`.
- Core task/project/workflow persistence is represented by `task_records`, `projects`, `workflows`, and project/workflow link tables. Existing delete paths are `Store.DeleteTask` in `server/workflowstore/tasks.go`, `Store.DeleteWorkflow` in `server/workflowstore/workflow_delete.go`, and `Store.DeleteProject` in `server/workflowstore/project_delete.go`; all already use explicit transactions and generated delete queries. Task delete currently removes comments and task-owned runtime rows before the task row. Workflow/project deletion bulk-removes task rows through generated queries.
- `server/core/raw_sql_guard_test.go` and metadata query-generation tests enforce the repository rule that production SQL is generated/query-source based, not hand-written in Go.

### Task store, status, and deletion

- `server/workflowstore/tasks.go` owns `CreateTask`, `UpdateTask`, task execution scope, and `DeleteTask`; task creation and editing already resolve Project/Workflow/source-workspace identity and use a transaction.
- `server/workflowview/task_projector.go` owns the shared task projection. `TaskProjector.ProjectTaskFacts` combines persisted status facts with `currentNodesContainTerminal`; `Done` is therefore already a server-side projection and includes Terminal Nodes. `server/workflowview/task_detail.go`, `task_list.go`, and `board.go` consume this projector.
- Board cards are projected in `server/workflowview/board.go`; `ListNodeCards` uses the generated `ListBoardNodeTasks` query and paged cursor input. `shared/serverapi/workflow.go` defines `WorkflowBoardTaskCard`, task list items, and task detail contracts. This is the existing board aggregate/card path where dependency counts must be supplied without edge-row loading per card.
- Task deletion has existing quiescence/attention cleanup and confirmation behavior. `server/workflowstore/project_delete.go` stages artifacts, rechecks blockers after acquiring a write lock, then deletes project tasks in one transaction. `server/workflowstore/workflow_delete.go` rechecks impact/version/counts in a transaction and deletes workflow tasks in dependency order. These are the relevant atomic cleanup boundaries.

### Server API and service routing

- `shared/serverapi/workflow.go` contains the public workflow RPC request/response types and validation. Existing task lifecycle types are `WorkflowTaskStartRequest/Response`, `WorkflowTaskMoveRequest/Response`, `WorkflowTaskDetail`, `WorkflowTaskSummary`, and task comment contracts.
- `shared/apicontract/service_contracts.go` exposes the server-owned `WorkflowService`; `shared/apicontract/rpc_routes.go` registers each workflow RPC with route metadata. `server/transport/gateway_unary_handlers.go` maps protocol methods to `WorkflowService` methods. New task relationship operations will need to follow all three seams plus protocol method constants/handshake compatibility.
- `server/workflowsvc/service.go` is the service boundary for task create/update/start/move/delete, task detail/list/board projections, and comments. Comment methods around the existing task service are the closest reusable request-to-store-to-projection pattern.
- `server/workflowview/task_detail.go` assembles detail facts from store/query/runtime sources, while `TaskProjector` centralizes the final task summary/status/action projection. `server/workflowview/activity.go` and the comment methods provide the existing bounded Task Comment read model.
- `server/workflowsvc/service_test.go` contains service-level lifecycle/comment tests, including paginated comment reads and task start/move behavior. `server/workflowstore/manual_moves_test.go` and `server/workflowstore/current_node_completion_test.go` cover manual movement, terminal state, and task deletion at the store boundary.

### Start and Manual Move continuation

- CLI start and move entry points are `cli/kent/task_lifecycle_command.go` (`taskStartSubcommand` and `taskMoveSubcommand`). Both currently parse execution-target options, call remote lifecycle methods through `runWorkflowMutationWithSetupProgress`, handle selection-required outcomes, and render lifecycle results.
- Server lifecycle request handling is in `server/workflowsvc/service.go`; durable movement preparation/application is in `server/workflowstore/manual_moves.go` and runtime coordination is in `server/workflowexecution/current_node_lifecycle.go`. Execution-target continuation is represented by `WorkflowExecutionTargetActionOutcome` and selection-required fields in `shared/serverapi`.
- Existing lifecycle contracts do not yet contain a dependency acknowledgement/bypass field or dependency-blocked outcome. `WorkflowTaskStartRequest.Validate` and `WorkflowTaskMoveRequest.Validate` are the request validation seams.

### CLI command and output seams

- `cli/kent/task_command.go` dispatches the canonical `task` subcommands. Existing aliases use the same handler (`comment`/`comments`, `label` paths).
- `cli/kent/task_show_command.go` defines the JSON wrapper (`taskShowOutput`), resolves task references, and renders human detail through `writeTaskDetailWithLabelNames`. `cli/kent/task_output.go` and `task_list_projection.go` contain shared output/projection helpers.
- `cli/kent/task_comments_command.go` is the closest subcommand implementation for a nested task operation, including project-scoped task resolution, JSON handling, and human output. `cli/kent/help/task.txt` is the public command help source and currently documents start/move/show/comment/label but no dependency group.
- Remote CLI capabilities are exposed through `workflowCommandRemote` and the shared client implementations under `shared/client`; API service method additions must reach the remote client and test transports before CLI handlers can call them.

### Workflow agent awareness

- Task Comment awareness is the existing model: `server/workflowrunner/starter.go` exposes `CountTaskComments`, `server/workflowrunner/inspection.go` wires it into runtime configuration, and `server/runtime/meta_context_runtime.go` refreshes the count when workflow instructions are selected for task delivery or compaction.
- `server/runtime/meta_context.go` carries `WorkflowTaskCommentCount` into the workflow developer-instruction builder. `server/runtime/workflow_prompt_timing_test.go`, `compaction_workflow_replacement_test.go`, and `history_replacement_recovery_test.go` cover timing, compaction reinjection, and failure behavior.
- `server/runtime/meta_context_runtime.go` uses typed steering (`steerMetaContextIfChanged`) to replace/refresh runtime context at the runtime boundary. The existing mechanism is the relevant seam for adding dependency awareness while preserving the no-history-rewrite rule.

### Desktop API and surfaces

- Desktop API DTOs and Zod adapters are in `apps/desktop/src/api/models.ts` and `apps/desktop/src/api/schemas/workflowBoard.ts`/`common.ts`; transport methods are split across `client.ts`, `clientTaskLifecycle.ts`, and `clientWorkflowBoard.ts`, with `apiService.ts` defining the client-facing service shape.
- `apps/desktop/src/features/task-detail/useTaskDetailData.ts` owns Task Detail queries, comments, live project-event invalidation, and lifecycle mutations. `TaskDetailContent.tsx` and `TaskDetailList.tsx` compose the detail surface. The current data model has no dependency fields.
- `apps/desktop/src/features/board` owns board data/cards and board refresh/query behavior. `BoardCardInstance.ts` identifies rendered cards; `BoardCard.tsx`/nearby board components should be located by the future implementer for card footer/chip composition.
- `apps/desktop/src/shared/task-mutations/useTaskMutations.ts` centralizes Task Detail mutation refreshes and invalidates task, attention, comments, activity, boards, cards, and task lists. `useTaskDetailLiveRefresh` already filters project events through `workflowProjectEventAffectsTask`.
- Sidebar routing/destination seams are under `apps/desktop/src/app/sidebar.tsx`, `sidebarDestinations.tsx`, `apps/desktop/src/app-facade`, and `features/task-detail`. Existing New Task UI is `apps/desktop/src/features/tasks/NewTaskDialog.tsx`; existing task-detail focus routing is represented by `taskDetailInitialFocus` and attention notification focus types.
- Desktop uses a server-authoritative remote API and preserves unsent drafts without queueing mutations, as documented in `docs/dev/specs/desktop-gui.md`; disconnected mutation behavior and live refresh are already explicit in the current hooks.

### Protocol, documentation, and versioning

- `shared/protocol/version.json` currently contains protocol version `73`; `shared/protocol/protocol.go` embeds it and `shared/client/remote_rpc.go` sends it during handshake.
- Public CLI help is `cli/kent/help/task.txt`; public product documentation is under `docs/src/content/docs`. The repository `kent-tasks` skill is outside this worktree at `/Users/nek/.kent/.generated/skills/kent-tasks/SKILL.md` and is the documented CLI task-management guidance source.

## Design

### Design authority and task baseline

- Use the locked Task Dependency sections of the Workflow orchestration, Desktop GUI, and terminology specifications, plus the two KENT-210 design references, as the product authority.
- The KENT-210 worktree is updated to committed `fix/workflow-runtime-recovery` revision `43839bedf`, which contains the locked specification and reference commits. The main source workspace and its unrelated uncommitted work remain untouched.
- Do not add the KENT-210 scroll timing, accessibility wording, or failed-removal presentation details to the product specifications; they are intentionally plan-level implementation guidance.

### Relationship behavior

- A Task Dependency is one directed relationship from a Blocker Task to a Blocked Task. The ordered Task pair is its identity; there is no relationship identity or stored reverse copy.
- Both Tasks must belong to the same Project. They may belong to different linked Workflows or source workspaces.
- A Task may have at most 50 direct Blocker Tasks and may directly block at most 50 Tasks. Both complete direct directions are returned without pagination.
- Self-dependencies and direct reciprocal relationships are rejected. Directed cycles of three or more Tasks are allowed. Kent never traverses transitive dependencies.
- Dependencies are advisory planning metadata. They do not pause, move, resume, interrupt, or otherwise mutate work already underway.
- Add and remove are idempotent and available in every Task state. A real relationship change updates both affected Tasks; an idempotent no-op updates neither.
- A dependency is satisfied only when its Blocker Task has authoritative `done` status. Every Terminal Node satisfies it, including one reached by Manual Move. Reopening the Blocker makes it unsatisfied again without changing the Blocked Task's update time.
- Task, Workflow, and Project deletion atomically remove touching relationships. Each surviving related Task receives one update-time change. Existing Task Delete confirmation remains unchanged.
- Relationship validation and mutation are atomic, including both 50-relationship limits under concurrent additions. Failures identify the violated rule and change nothing.

### Relationship views and ordering

- Task Detail exposes complete `Blocked by` and `Blocks` directions, related Task status, Blocker satisfaction, and direct aggregate counts from one authoritative projection.
- Both directions put Tasks that are not done first, then order each group by Task Short ID.
- Board cards use exact aggregate progress and never load relationship rows per card.

### CLI

- The canonical command group is `kent task dep`; `deps`, `dependency`, and `dependencies` are accepted but hidden from help and documentation.
- Add and remove use named `--blocker` and `--blocked` Task selectors. List inspects both directions by default and accepts `--direction blocks|blocked-by`.
- Plain add and remove output is exactly `done`. JSON mutation output contains only the typed outcome and both Task identities.
- Human dependency lists and Task show use the locked `Blocks <N> tasks:` then `Blocked by:` shape, omit empty directions, and preserve the locked ordering. A Task with no relationships produces no human dependency section.
- Dependency-list JSON returns complete direction envelopes. Task-show JSON exposes aggregate counts only and never embeds related Tasks.
- Task Start and executable Manual Move accept `--ignore-dependencies`. Without it, an otherwise valid operation with unsatisfied dependencies returns the aggregate-only confirmation-required outcome and exits nonzero; the CLI never prompts interactively.

### Desktop board and Task Detail

- A board card with direct Blocker Tasks shows one reusable dependency-progress chip before Labels: circular progress plus `satisfied blockers / total blockers`, primary while incomplete and success when complete. No direct Blocker Tasks means no chip.
- The chip's accessible name uses `Dependencies: N of M complete.` Exact localized wording for the other icon-only dependency actions is not a KENT-210 product contract.
- Task Detail places one flat Dependencies area after the description and metadata. Its header reuses the exact board chip and omits it when there are no direct Blocker Tasks.
- `Blocked by` appears before `Blocks`, separated by one divider. Empty subsections remain visible with their circular Add controls.
- Each related-Task row shows the locked status icon, Task Short ID, and one-line end-ellipsized title, with no Workflow name.
- Selecting a related Task replaces the current Task Detail in the same presentation. KENT-210 adds no sidebar-local Back or Forward history.
- Remove uses the uncircled red `X`, sends immediately without confirmation, and removes the row immediately. If removal fails, Desktop reloads Dependencies from the server and shows a non-persistent error without a one-click Retry.
- Opening Task Detail from a dependency-progress chip or `View deps` reliably scrolls Dependencies into view after asynchronously loaded data is ready. KENT-210 adds no special keyboard-focus movement.

### Related-Task creation

- `Blocked by` Add replaces Task Detail with the ordinary New Task form for a new Blocker Task. `Blocks` Add does the same for a new Blocked Task.
- The form silently carries the originating Task and fixed relationship direction. It shows no dependency field, relationship explanation, picker, or other visible variant.
- The new Task uses the originating Task's Project and Workflow, defaults to its source workspace, and keeps the ordinary workspace selector available.
- Submit atomically creates the Backlog Task and relationship. Failure or cancellation creates neither. Successful submission keeps the ordinary New Task behavior.
- Desktop does not provide an existing-Task dependency picker in KENT-210.
- At a 50-Task direction limit, its Add control remains unavailable with an accessible explanation; Kent rechecks the limit on submission.

### Start-ahead acknowledgement

- Task Start and human Manual Move to executable work check current direct unsatisfied dependencies only after the action is otherwise valid and before Execution Target selection or any other continuation.
- Desktop uses the exact locked `Start task ahead of dependencies?` dialog, aggregate count body, corner Close, outline `View deps`, and primary `Start`. It never lists Blocker Tasks in the warning.
- `View deps` abandons the operation and opens the Blocked Task's own Dependencies area. `Start` acknowledges only that initiating operation and carries through later continuation dialogs.
- Dismissing a later continuation leaves the Task unchanged and discards the acknowledgement. A later independent operation checks dependencies again.
- Concurrent dependency changes do not require a second acknowledgement within the same initiating operation. Resume, Approval, and automatic transitions never request dependency acknowledgement.

### Workflow agent awareness

- New Workflow instructions mirror Task Comment awareness. When direct unsatisfied dependencies exist, they state only the current count and direct the agent to `kent task show <current Task Short ID>`.
- Instructions never embed related Task bodies or relationship lists and never rewrite earlier model-visible history when relationships or satisfaction change.

### Scope boundary

- KENT-355 owns removal of the 50-relationship cap and relationship pagination.
- KENT-356 owns sidebar-local Task navigation history, Back/state restoration, a visible dependency field, and existing-Task search.
- KENT-210 does not restore Task Cancel behavior or absorb either follow-up.

## Architecture

### Architecture work checklist

- [x] Define the persistence, transaction, and deletion boundaries.
- [x] Define server-owned domain, projection, and API contracts.
- [x] Define Start/Manual Move continuation and Workflow-instruction integration.
- [x] Define CLI integration and output ownership.
- [x] Define Desktop data flow, navigation, mutation, and rendering composition.
- [x] Define protocol/documentation impact and cross-layer failure propagation.

### Ownership and source of truth

- `server/workflowstore` owns every Task Dependency write. CLI and Desktop call the same Workflow service methods and never derive, mirror, or persist relationship state.
- `server/workflowview.TaskDependencies` owns every reusable relationship read, satisfaction derivation, canonical related-Task status, ordering, and aggregate count. Its full projection and focused direct-unsatisfied-count method share one internal typed direct-dependency facts loader and satisfaction reducer. CLI, Desktop, lifecycle preflight, and Workflow instruction awareness consume that owner through narrow interfaces rather than querying or deriving dependency state in `workflowstore`.
- `server/workflowview.Board` owns the page-bounded board-card aggregate as the one optimized projection-specific query; it uses the same durable `Done` satisfaction fact and is covered against the canonical `TaskDependencies` count. Clients receive ready-to-render projections.
- Satisfaction is not stored. Every read joins the directed relationship with the existing canonical `workflow_task_status_records.is_done` fact; live `running`, `queued`, and `waiting_question` presentation continues to overlay through the existing runtime authority.
- Dependency changes remain outside Workflow Execution state. They do not acquire live execution ownership, create a scheduler gate, or add durable acknowledgement state.

### Domain and persistence model

- Add migration `00062_task_dependencies.up.sql` after the current `00061` migration.
- Add one `task_dependencies` table with only:
  - `blocker_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE`
  - `blocked_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE`
  - primary key `(blocker_task_id, blocked_task_id)`.
- Add the reverse lookup index `(blocked_task_id, blocker_task_id)`. The primary key serves `Blocks`; the reverse index serves `Blocked by`, unsatisfied counts, cleanup, and reciprocal checks.
- Do not add an ID, reverse row, satisfaction column, timestamps, soft-delete state, transitive closure, or relationship history.
- `server/workflow` owns one pure typed Task Dependency policy: the direct-edge limit, attach evaluator over canonical pair/project/existence/count facts, and Add-availability projection. `server/workflowstore` is the only mutation owner: after the Project-row write lock it loads those facts and calls the policy; Add and atomic Task creation never reimplement a rule. `server/workflowview` calls only the same policy's Add-availability projection. The migration adds no trigger or check that reimplements a business rule.
- SQLite owns only structural relationship integrity: the ordered-pair primary key and Task foreign keys. Because every production mutation routes through the locked store orchestrator and one domain policy, final-slot concurrency remains atomic and expected failures are typed before insert. Any later primary-key/foreign-key failure is an invariant/storage failure with pair context, never classified by parsing SQLite error text.
- Keep all authored SQL in the migration and generated query sources. Regenerate `server/metadata/queries.sql` and `server/metadata/sqlitegen`; no production Go code contains SQL.

### Mutation transaction boundary

- Add one dependency transaction implementation in `server/workflowstore/task_dependencies.go`; Add and atomic Task-create attachment share one locked fact-loading/orchestration path into `workflow.TaskDependencyPolicy`, while remove and deletion cleanup share its pair resolution and generated query helpers.
- A mutation first performs a no-op write against the Blocker Task's Project row, then re-reads both Tasks inside the same transaction. This follows the existing Project-label write-lock pattern and makes count/check/insert or delete/touch one serialized SQLite write operation.
- After the locked store path loads canonical facts, it calls the domain policy once. The policy's evaluation order is:
  1. Resolve both canonical Tasks and verify same Project and non-self pair.
  2. Return `already_present` before limit checks when the exact pair exists.
  3. Reject the reverse pair.
  4. Count the Blocker's outgoing and Blocked Task's incoming relationships.
  The store then:
  5. Insert the pair.
  6. Touch both Tasks once with one operation timestamp.
  7. Commit and return the typed pair metadata and `added`.
- Remove flow resolves and validates both Tasks, deletes the exact pair, and touches both Tasks only when a row was removed. An absent row commits unchanged and returns `already_absent`.
- Store errors are typed by rule and carry both Task identities plus the relevant limit or missing Task identities. Service mapping converts them to one structured RPC error; callers never inspect error strings.
- Relationship mutation does not use the Workflow Execution mutation permit. The Project-row write lock and SQLite transaction own relationship concurrency, so advisory metadata remains independent of live execution while still serializing with Task/Workflow/Project deletion writes.

### Atomic related-Task creation

- Extend the existing Task-create domain request with an optional typed relationship intent: existing related Task ID plus the new Task's role (`blocker` or `blocked`).
- Extend `createTaskWithQueries` rather than adding a second Task creation path. It creates the Task, applies labels, validates and inserts the dependency through the same transaction helper, and commits once.
- The Project sequence allocation already writes the Project row, so the create transaction is serialized with relationship mutations. Constructible relationship failures—cross-Project scope and the existing related Task's applicable incoming or outgoing full slot—roll back the Task row, Task sequence allocation, labels, and dependency together.
- The new Task ID is generated inside `CreateTask`, so a reverse edge to that not-yet-existing ID cannot be pre-seeded through the public create boundary. Do not add caller-supplied IDs or a test-only seam to manufacture reciprocal-create failure; reciprocal validation remains covered through the shared relationship helper and public Add path.
- The ordinary CLI Task-create command sends no relationship intent. Desktop's hidden New Task navigation parameter is the only KENT-210 caller that supplies it.

### Deletion cleanup and surviving Task timestamps

- Task Delete acquires the same Project-row dependency write lock before its relationship reads. It explicitly lists distinct direct neighbors, deletes touching relationships, and batch-touches surviving neighbors once before deleting the Task in the existing Task Delete transaction. `ON DELETE CASCADE` remains the final integrity guarantee.
- Workflow Delete begins its commit transaction with one generated no-op Project update covering Projects that contain the Workflow's Tasks, then rechecks impact. It computes the distinct surviving Tasks related to Tasks owned by the deleted Workflow, batch-touches them once, deletes touching relationships, then continues the existing bulk Task/Workflow deletion transaction.
- Project Delete needs no surviving-Task touch because same-Project scope makes every related Task part of the deleted Project. Foreign-key cascade removes the rows inside the existing Project Delete transaction.
- Cleanup uses generated set-based queries. It does not call per-Task delete paths or issue one read/write per relationship.

### Server-owned relationship projection

- Add a reusable `workflowview.TaskDependencies` read model and wire one instance into `workflowsvc.ReadModels`.
- The read model has a full projection method and a focused direct-unsatisfied-blocker count method. Both call one private typed facts loader and shared satisfaction reducer; the focused method loads only the bounded `blocked-by` facts and canonical durable statuses, not unrelated Task Detail/worktree facts.
- The full projection loads both direct directions and compact related Task metadata with one generated relationship query. It then:
  - bulk-loads canonical durable statuses through the existing Task status projector;
  - overlays live execution state from one Project runtime snapshot;
  - derives satisfaction from the durable `Done` fact;
  - sorts each bounded direction unfinished-first, then Task Short ID;
  - returns complete directions and aggregate counts.
- `workflowview.TaskDetail` composes this read model into every `WorkflowTaskDetail`, including an empty projection, so Desktop always has the two Add surfaces and CLI Task show can render without another request.
- Expose the same projection through a focused dependency-list RPC for `kent task dep list`; do not make that command load unrelated Task Detail facts such as worktree availability.
- The projection contract contains:
  - summary counts: blocker count, unsatisfied blocker count, directly blocked Task count;
  - direction records with typed `blocks` or `blocked-by`;
  - for Task Detail only, server-owned Add availability beside each direction as a required union: `available` with positive `remaining_capacity`, or `limit_reached`; `workflowview` derives it by calling the same `workflow.TaskDependencyPolicy` used by mutations, never by owning a second limit or comparison;
  - compact related Task ID, Short ID, title, Workflow ID, canonical status;
  - satisfaction and unsatisfied count only for `blocked-by`.
- The focused dependency-list RPC maps the same read-model facts into the exact locked CLI list envelope and omits Task Detail Add availability. `task dep list --json` and `task show --json` gain no unapproved fields.
- API validation enforces direction-specific optional fields, list/count consistency, and the typed Add-availability union. It does not redeclare the numeric cap: `available.remaining_capacity` or `limit_reached` is produced by the domain policy, complete arrays are explicit, and absence is not represented by empty-string or numeric sentinels.

### Board aggregate query

- Extend `ListBoardNodeTasks` in the generated query template. After the existing page CTE selects at most the requested card page, one grouped dependency CTE calculates blocker total and satisfied count only for those page Task IDs.
- Join the aggregate back into the same query result. `workflowview.Board` passes an optional dependency-progress value to each card; total zero becomes absence, never a `0/0` chip.
- `WorkflowBoardTaskCard` gains one optional typed progress object with `satisfied_count` and `total_count`. No card query loads relationship rows, and no per-card query is introduced.

### API, protocol, and events

- Add focused Workflow service methods and routes for dependency add, remove, and list. Extend the existing Task-create request for atomic related creation.
- Add a dedicated Start/Move outcome type with exactly three variants:
  - `applied`
  - `dependency_confirmation_required` with only `unsatisfied_dependency_count`
  - `selection_required`
- Keep Approval on its existing execution-target-only outcome type; dependency confirmation is impossible for Approval.
- Start and Move requests gain `proceed_despite_dependencies`. CLI `--ignore-dependencies` and Desktop's primary `Start` button map to this domain field.
- Add the structured Task Dependency RPC error and decoder, new method constants/routes/handlers, remote-client methods, and response validation across `shared/serverapi`, `shared/apicontract`, `server/transport`, and `shared/client`.
- Add project event action `dependencies_changed`. Add/remove and atomic related creation publish one Project-scoped Task event containing one affected Task as primary and the other in `related_ids`.
- Existing Task lifecycle/title events keep their current primary Task. Desktop adds typed dependency-impact predicates rather than broad foreign-Workflow invalidation:
  - a board preserves its existing same-Workflow refresh behavior; for a foreign-Workflow Task event it refreshes only for `dependencies_changed`, `deleted`, `moved`, `approved`, or `completed`, the actions that can change one of its direct edges or the blocker `Done` fact;
  - `approved` is always dependency-impacting because the event does not encode whether the approved transition entered a Terminal Node. A terminal Approval must update satisfaction; a nonterminal Approval performs the same bounded authoritative refetch and leaves progress unchanged;
  - an open Task Detail retains existing direct-current-Task invalidation; an event matching only a currently loaded related Task invalidates it only for `updated`, `deleted`, `started`, `interrupted`, `resumed`, `approved`, `moved`, `completed`, `question_waiting`, `question_cleared`, or `question_answered`, which can change the compact row title/status or remove its edge;
  - `dependencies_changed` invalidates Task Detail relationship data only when the open Task ID itself appears in the event's primary/related pair. A dependency change between one loaded related Task and a third Task does not refresh the open detail.
- Foreign-Workflow comments, labels, questions, interrupts, resumes, and ordinary updates do not refresh a board. Foreign-Workflow comments and labels do not refresh a Task Detail merely because their Task appears in its related rows.
- Workflow deletion keeps one existing Project-scoped `workflow/deleted` event per affected Project and emits no unbounded survivor Task events. Desktop treats that event as Project-wide invalidation regardless of the deleted Workflow ID: every open Task Detail plus every board summary/card-page query for that Project is invalidated, including boards for surviving Workflows whose cross-Workflow edges were removed.
- The current protocol version is `75`; increment it once for the incompatible RPC, event, and projection changes.

### Start and Manual Move continuation

- Deepen the existing initiating-action coordinator into an explicit non-mutating preflight followed by continuation. The preflight returns a typed, request-local preparation; it is not persisted and is not an acknowledgement or reservation.
- Server ordering is authoritative:
  1. Validate the wire request, setup-operation ID when applicable, explicit Execution Target selection shape, and all request fields.
  2. Under the existing Workflow Execution mutation permit, run the action preflight before reading dependencies:
     - Start checks controller-owned Task quiescence and the existing durable `ValidateTaskStart` rules.
     - Manual Move checks controller-owned Task quiescence and deepens `PrepareManualMove` to validate the current Task/source/path/target kind plus completion commentary/output shape and a dry-run of output/input materialization that does not require an Execution Target.
     - An executable action also loads and validates its Task Execution Target context: locked-versus-explicit compatibility, policy/selection compatibility, and required target infrastructure. It does not restore, resolve, or materialize a target.
  3. If the preflight says the action is executable and `proceed_despite_dependencies` is false, query the current direct unsatisfied count through `workflowview.TaskDependencies`.
  4. Return `dependency_confirmation_required` without Execution Target work or Task mutation when the count is nonzero.
  5. Otherwise continue the prepared action through the existing Execution Target selection/restore/resolve/materialize coordinator and apply path.
- Target-dependent revision/workspace availability, selected-root materialization, and Script validation against the selected Execution Root necessarily remain in continuation. The final Start/Move apply still rechecks live and durable state under the mutation permit/transaction because preflight creates no reservation; a concurrent state change can therefore fail the proceeded action without creating compatibility state.
- Any request/live/durable failure knowable in preflight returns before the dependency reader and before every Execution Target resolver/restorer/materializer call. Negative Start and Move tests lock that ordering.
- A proceeded request skips the dependency recheck for that initiating request. No relationship snapshot or acknowledgement token is created.
- Desktop reuses one setup-operation ID and one typed action through dependency confirmation and any later Execution Target selection. Dismissal clears the in-memory action, including proceed intent.
- CLI remains non-interactive. A confirmation-required response is rendered and exits nonzero; a rerun with `--ignore-dependencies` is a new explicit initiating request.

### CLI composition

- Add `cli/kent/task_dependency_command.go` with one `taskDependencySubcommand` handler. Route `dep`, `deps`, `dependency`, and `dependencies` to that handler; only `dep` appears in `cli/kent/help/task.txt` and public docs.
- Reuse `resolveWorkflowTaskID` for every selector. Add/remove resolve both selectors before calling the mutation RPC.
- Add one shared dependency-direction renderer used by `dep list` and human `task show`, preserving the exact heading/order/empty behavior from the spec.
- Extend `taskShowOutput` with only the optional aggregate dependency summary. The full directions remain outside Task-show JSON.
- Add `--ignore-dependencies` to Start and Move and add `--json` to Move so both lifecycle commands can return the typed confirmation outcome. Handle dependency confirmation before existing Execution Target selection handling.
- JSON output uses server projection types directly where they match the locked contract; human copy is owned only by the CLI.

### Workflow instruction awareness

- Replace the narrowly named runtime Task-comment counter seam with one Task-awareness source returning comment count and direct unsatisfied dependency count together. The composition adapter obtains comments from the existing store seam and dependencies from the injected `workflowview.TaskDependencies` focused count method; `workflowstore.Store` does not implement or derive dependency awareness.
- Carry both counts through `workflowruntime.CurrentNodeExecutionConfig`, runtime meta-context build options, persisted inspection, and prompt arguments.
- Query both counts only when the existing workflow-prompt selector chooses a new instruction message: initial assignment, reassignment, or compaction reminder.
- Render only the count and `kent task show <current-short-id>` when unsatisfied dependencies exist. Zero emits no dependency paragraph.
- The existing typed meta-context steering remains unchanged. Relationship events never steer runtime context, so an already-visible workflow instruction is never rewritten.

### Desktop data and rendering

- Extend Desktop models, strict Zod schemas, API methods, and Task mutation inputs with the server dependency projection, board progress, mutation methods, atomic-create intent, and Start/Move outcome.
- Keep Desktop feature ownership isolated:
  - `ui` deepens the existing canonical `InteractiveChip`/`Chip` implementation rather than adding another chip base. A generic progress-chip composition renders circular progress content and semantics inside `InteractiveChip`, forwards its native button/size/focus/disabled behavior, and contains no Task Dependency API or business rule. If complete progress needs a success tone, extend `InteractiveChipTone` and its existing canonical tone maps/theme tokens once.
  - `shared/task-dependencies` may own only feature-neutral typed adapters and dependency-impact event predicates used by more than one feature. It exposes a narrow public entrypoint and contains no React feature composition, mutation hook, or client-side cardinality rule.
  - `features/board` owns its dependency-progress composition and card interaction.
  - `features/task-detail` owns its header composition, Dependencies area/rows, status presentation mapping, and relationship mutation hooks.
  - New Task owns its form adaptation, reached through the typed app-facade/sidebar destination. No feature imports another feature.
- Extend `KanbanCardVM` with optional dependency progress. Render the Board-owned composition of the `InteractiveChip`-backed UI-kit progress chip before the existing Label overflow row; its button stops card activation/drag and opens the Task at Dependencies. Task Detail composes that same UI-kit content wrapper independently through its own feature owner. No parallel rounded chrome, sizing table, focus ring, disabled state, or activation implementation is introduced.
- Add a static `dependencies` item to `TaskDetailList` after the body/properties item and before Inbox/tabs. Because dependencies are part of core Task Detail, the existing core loading/error state owns their initial failure.
- Add typed `TaskDetailInitialFocus { kind: "dependencies" }` and use the virtual list's keyed initial-scroll contract. Keep the item pinned until measured so asynchronous Task loading and virtualization cannot race the scroll; do not move keyboard focus.
- Task Dependency rows and Add/Remove controls receive callbacks from their Task Detail presentation owner. Selecting a row replaces the current Task ID; Add replaces the current destination with New Task. No stack, Back action, or restoration state is introduced.
- Extend `SidebarController` with a replace operation that swaps the current destination while preserving the original open/close lifecycle promise. This supports related Task/New Task replacement without creating history or leaving the board's selected-Task route orphaned.
- The New Task destination carries a typed pending relationship and initial source workspace. `NewTaskForm` accepts them as non-visible inputs and sends them in the existing Task-create call. Each Task Detail Add control consumes its direction's server-owned Add availability; `limit_reached` disables Add with an accessible explanation, while `available.remaining_capacity` is informational and never compared with a Desktop-owned limit.
- Remove uses a single optimistic cache update to remove the pair from the open Task's corresponding direction and aggregate counts. On failure, invalidate the Task query to reload server truth and show the chosen non-persistent error; do not queue or retry the mutation.
- Relationship success invalidates the two Task details, project boards/card pages, and Task lists that depend on updated timestamps. Existing cached content remains visible during background refresh.
- All dependency mutations remain disabled while disconnected. No Tauri/native API enters feature components.

### Desktop initiating-action controller

- Deepen the existing shared Execution Target continuation controller into one typed Task initiating-action controller rather than adding a second Board-only state machine.
- Its pending state is a discriminated union:
  - dependency confirmation with action and count;
  - Execution Target continuation with action, requirement, selection draft, and submission/error phase.
- Initial execution sends `proceed_despite_dependencies=false`. Primary `Start` reruns the same action with the field true. A subsequent `selection_required` state retains that true field; Close, `View deps`, or later Cancel discards the whole pending action.
- `View deps` clears the action and replaces/opens the blocked Task Detail with Dependencies focus.
- Board drag/start/move disabling continues to derive from this single controller's running or pending state.

### Failure propagation and scope controls

- Validation, cardinality, reciprocal, Project-scope, missing-Task, and create-attachment failures leave both Tasks and the relationship table unchanged and surface as typed errors.
- Read-model inconsistency—missing status, duplicate projected Task, or invalid count—and any structural database failure after the locked authoritative evaluator fail with Task-pair context. Clients do not hide or reconstruct missing data.
- Board and Task Detail never compute satisfaction from loaded rows. Optimistic Desktop state is presentation-only and is always reconciled from the next authoritative response.
- No work is added for unbounded relationship pagination, existing-Task Desktop search, sidebar history, Task Cancel, transitive traversal, or scheduler behavior.
- Implementation also updates public CLI documentation and the repository `kent-tasks` skill; product specs remain unchanged because Design explicitly kept the newly discussed interaction minutiae at plan level.

## Planning

### Delivery shape

- Observable outcome: users can create, inspect, and remove direct Task Dependencies from CLI and Desktop, see exact blocker progress, and explicitly start or executably move a Task ahead of current unsatisfied dependencies.
- Implement with vertical red-green-refactor slices. Each behavioral slice below begins with one failing public-boundary test, adds the smallest coherent production path that passes it, and refactors only while green; do not write the complete test matrix before the first working path.
- Keep the Architecture ownership boundaries fixed: `workflow.TaskDependencyPolicy` owns business invariants and Add availability, `workflowstore` exclusively orchestrates writes, `workflowview` owns relationship reads and satisfaction, and CLI/Desktop only consume typed server projections. Reuse the existing Task-create transaction, Task status projector, Project write-lock pattern, Execution Target continuation, Task Comment awareness lifecycle, sidebar controller, and query invalidation mechanisms instead of creating parallel paths.
- The approved lean scope already excludes transitive traversal, scheduling gates, persisted acknowledgements, relationship history/IDs, Desktop existing-Task search, sidebar history, relationship pagination, Task Cancel restoration, and Rust/TUI work. Do not reopen or compensate for those exclusions.
- Estimated diff: **medium confidence** — handwritten production changes in approximately **45–65 files / 3,800–5,800 LoC**; tests and generated artifacts in approximately **40–65 files / 4,500–7,500 LoC**. The main variance is generated SQLite bindings and the amount of existing Desktop continuation code that can be deepened without replacement.

### Workstream and delegation boundaries

- **Foundation — ordered and non-delegable:** complete slices 1–10 in order. They establish persistence, transaction semantics, deletion, atomic creation, projections, and the board aggregate that every client consumes.
- **Server contract — ordered after Foundation:** complete slices 11–14 to lock API, event, remote-client, and Start/Move contracts. Do not begin client contract work against provisional DTOs.
- **Parallel work after slice 14:** CLI slices 15–17, Workflow-agent awareness slice 18, and Desktop slices 19–26 are independent workstreams with disjoint primary ownership. They may be delegated to separate agents; within each workstream preserve the listed order.
- **Convergence:** slices 27–30 integrate documentation/versioning, perform structural audits, run end-to-end tests and browser QA, then build the final binary. Any contract change discovered during a downstream workstream returns to the owning earlier slice rather than introducing a client-side compatibility shim.

### Ordered implementation checklist

- [x] **1. Add the structural relationship schema and generated query surface** *(target 200–400 changed LoC).* RED: add metadata schema tests that open a migrated database and prove ordered-pair uniqueness, deletion cascade, and the reverse lookup index; structurally prove the migration contains no self/scope/reciprocal/cardinality trigger or check. GREEN: add migration `00062`, authored query-source operations for pair lookup/count/insert/delete and bounded direction reads, regenerate metadata SQL/bindings, and expose only typed adapters. **Done when:** focused metadata/query-generation tests pass, `./scripts/dump-metadata-schema.sh` shows only the pair primary key, Task foreign keys, and reverse index for relationship integrity, and `rg` finds no new production raw SQL or duplicated business-invariant trigger.

- [x] **2. Implement the single domain policy through the first Add tracer** *(target 300–500 changed LoC).* RED: through the public `workflowstore` Add boundary, prove one same-Project cross-Workflow pair is added and both Tasks receive one timestamp, while missing Tasks, self-link, cross-Project scope, and a direct reciprocal pair return typed errors without mutation; prove a three-Task cycle is accepted. GREEN: add typed Task Dependency facts/results and one pure evaluator in `server/workflow`, then add the Project-row write lock and fact-loading transaction orchestrator in `workflowstore`; insert only after the policy succeeds. **Done when:** focused domain/store tests pass, Add and the future create path call the exact same policy, and no migration trigger/check or second Go validator implements these rules.

- [x] **3. Complete idempotency, cardinality, and availability in the same policy** *(target 250–450 changed LoC).* Add one RED→GREEN case at a time for exact-pair idempotency before limits, Blocker outgoing capacity, Blocked Task incoming capacity, typed limit metadata, and the policy's `available(remaining_capacity)`/`limit_reached` projection. GREEN extends only the domain policy introduced in slice 2 and the store's generated fact/count queries; it does not add SQL enforcement, view-owned constants, or caller-side prechecks. **Done when:** the first 50 edges in each direction succeed, the 51st returns the typed limit error with no row/timestamp change, repeated Add returns `already_present` without touching either Task, and availability is produced by that same policy.

- [x] **4. Prove final-slot concurrency and invariant ownership** *(target 200–350 changed LoC).* RED: race two public Add calls for the same Project's final outgoing slot and repeat for the final incoming slot; add architecture guards that dependency insert/delete adapters are consumed only by the `workflowstore` mutation owner and that no second Task Dependency limit/evaluator exists outside `workflow.TaskDependencyPolicy`. GREEN: rely on the Project-row write lock and one transaction's fact-load/evaluate/insert order; surface unexpected primary-key/foreign-key failures as contextual invariant/storage errors without text parsing. **Done when:** exactly one writer succeeds in each race, the other receives the typed limit outcome, no partial timestamp change occurs, and searches/schema guards prove there is no trigger, duplicate constant, or parallel validator.

- [x] **5. Implement idempotent Remove through the same transaction owner** *(target 200–350 changed LoC).* RED: prove removing an existing pair returns `removed`, deletes exactly that pair, and touches both Tasks once; prove removing an absent pair returns `already_absent` and touches neither Task. GREEN: add the public Remove operation using the shared pair resolution/write lock and generated delete query. **Done when:** focused store tests pass for both outcomes and Add/Remove share validation and transaction helpers rather than duplicated flows.

- [x] **6. Add Task deletion relationship cleanup** *(target 250–450 changed LoC).* RED: create a Task with incoming and outgoing relationships, delete it through the public Task Delete operation, and prove all touching rows disappear while each distinct surviving neighbor is touched once. GREEN: acquire the same Project-row lock, list distinct neighbors, batch-delete edges, batch-touch survivors, and continue the existing Task deletion transaction. **Done when:** deletion tests pass for duplicate-neighbor directions, no dangling rows remain, and the existing Task Delete confirmation contract is unchanged.

- [x] **7. Add Workflow and Project deletion cleanup** *(target 300–500 changed LoC).* RED: add Workflow Delete coverage for cross-Workflow neighbors in every affected Project, distinct survivor timestamp updates, rollback on failure, and the affected Project identities needed by post-commit event publication; add Project Delete coverage proving cascade removes all edges without survivor updates. GREEN: add the generated set-based project lock/recheck/neighbor/delete/touch operations to the existing bulk deletion transactions. **Done when:** focused Workflow/Project deletion tests pass without calling per-Task deletion or issuing per-edge reads/writes, and Workflow Delete exposes bounded affected-Project facts without enumerating survivor events.

- [x] **8. Extend ordinary Task creation with an atomic relationship intent** *(target 300–500 changed LoC).* RED: through the existing Task-create public boundary, prove creating a new blocker and a new blocked Task each commits the Task, labels, sequence, and edge together; prove cross-Project scope rolls back all four; prove the new-blocker case rolls back when the existing Blocked Task's incoming slot is full and the new-blocked case rolls back when the existing Blocker Task's outgoing slot is full. Do not inject a new Task ID or attempt an impossible reciprocal-create fixture; reciprocal rejection remains covered in slice 2 at the shared policy/Add boundary. GREEN: add the optional typed related-Task intent and route it through `createTaskWithQueries` plus the exact domain policy and locked fact-loader from slices 2–3. **Done when:** focused constructible creation tests pass, ordinary callers omit the intent unchanged, and there is one Task creation path and one relationship-invariant implementation with no test-only ID seam.

- [x] **9. Build the authoritative Task Dependency projection, focused count, and Add availability** *(target 350–500 changed LoC).* RED: add projection tests for both complete directions, empty arrays, unfinished-first/Short-ID ordering, compact related Task facts, Done/manual-terminal satisfaction, reopen becoming unsatisfied, and a satisfaction change not touching the Blocked Task timestamp. Add boundary tests proving the full summary and focused unsatisfied-count method remain equal across terminal/reopen transitions, and each direction returns the domain policy's `available` or `limit_reached` result. GREEN: add reusable `workflowview.TaskDependencies`, one internal typed facts loader/shared satisfaction reducer, direction availability delegated to `workflow.TaskDependencyPolicy`, full bounded projection with bulk durable statuses and one Project live overlay, focused blocked-by count without unrelated Task Detail facts, and Task Detail composition. **Done when:** focused `workflowview` tests pass, list/detail/awareness-ready count use the same read owner, Add availability imports no view-local limit/comparison, and instrumentation proves no per-related-Task query or client-derived satisfaction is needed.

- [x] **10. Add exact board-card dependency aggregates in the page query** *(target 250–450 changed LoC).* RED: add board query/view tests spanning zero, partial, and complete blockers, cross-Workflow blockers, terminal/reopened blockers, and a page smaller than the full board; assert query count remains constant and only page Task IDs are aggregated. GREEN: extend the paged board query CTE and card projection with optional `{satisfied_count,total_count}`. **Done when:** focused board tests pass, zero blockers produce absence rather than `0/0`, and no relationship rows or per-card dependency query are loaded.

- [x] **11. Lock shared dependency API contracts and typed errors** *(target 350–500 changed LoC).* RED: add validation/round-trip tests for Add, Remove, focused List, atomic-create intent, Task Detail summary/directions with required typed Add availability, board progress, direction-specific optional fields, list/count consistency, and every typed dependency error; structurally prove focused List and Task-show JSON omit Add availability. GREEN: add separate route-shaped DTOs/mappers where the locked outputs differ, plus validators and the error decoder with null used only for genuine absence; validators check union structure and consistency without declaring another numeric cap. **Done when:** focused shared-contract tests reject malformed direction/count/availability combinations, CLI-facing envelopes retain their exact approved fields, and no contract exposes a relationship ID, stored satisfaction, history, transitive data, duplicate limit constant, or a client-owned cardinality decision.

- [x] **12. Wire service methods, transport routes, mutations, and Project events** *(target 350–500 changed LoC).* RED: add service/handler tests proving Add/Remove/List use the store/view owners, idempotent no-ops emit no event, real mutations and atomic related creation emit one `dependencies_changed` Task event with the pair split across primary/related IDs, and typed failures cross transport unchanged. Add Workflow Delete coverage proving one existing `workflow/deleted` event is published per affected Project after cross-Workflow edge cleanup and no per-survivor Task event is emitted. GREEN: implement routes, handlers, service composition, read-model wiring, remote-client methods, and publication at commit success. **Done when:** focused service/transport/client tests pass, deletion events carry enough Project scope for client-wide invalidation, and CLI/Desktop use one remote contract without inspecting error strings.

- [x] **13. Add an ordered Start preflight before dependency confirmation** *(target 350–500 changed LoC).* RED: through the service/controller boundary, first prove malformed requests, a live/non-quiescent Task, and every durable `ValidateTaskStart` failure return before the dependency reader and before Execution Target context/restore/resolve/materialize probes. Then prove an otherwise valid Start with unsatisfied direct blockers returns only `dependency_confirmation_required(count)` without target work or mutation, while proceed and zero/satisfied blockers enter continuation. GREEN: introduce the three-variant Start/Move outcome and `proceed_despite_dependencies`, deepen the initiating-action coordinator into typed preflight/continuation, and add a controller-owned non-mutating Start preflight under the mutation permit. Preflight may validate target context compatibility but must not perform target work; selected revision/workspace resolution, materialization, Script validation against the selected root, and final race revalidation remain in continuation/apply. **Done when:** focused Start tests lock the negative call ordering, warning/proceed/selection-required paths, terminal/reopen count changes, and absence of any persisted preparation or acknowledgement.

- [x] **14. Add the same complete preflight to executable Manual Move** *(target 350–500 changed LoC).* RED: prove malformed output/commentary, missing/ambiguous source or path, invalid target kind, unavailable/non-quiescent Task, and dry-run input/output materialization failures return before the dependency reader and every Execution Target probe. Prove non-executable Moves never warn, valid executable Moves warn before target work, proceed carries into later selection, target/root-dependent failures remain typed after continuation, and Approval retains its existing outcome type. GREEN: deepen `PrepareManualMove` as the non-mutating durable/request preflight, combine it with controller quiescence and target-context compatibility under the mutation permit, then route its typed preparation through the shared continuation and existing transactional apply revalidation. **Done when:** focused Move/Approval tests lock negative call ordering and warning→proceed→selection/apply behavior with no persisted acknowledgement, dependency snapshot, or reservation.

- [x] **15. Add CLI dependency mutations and canonical aliases** *(target 250–450 changed LoC; CLI workstream).* RED: add command-boundary tests for `task dep add/remove`, both selector resolutions before RPC, `--project`, JSON pair/outcome structure, exact plain `done`, and idempotent outcomes; structurally prove `deps`, `dependency`, and `dependencies` reach the same handler while only `dep` is advertised. GREEN: add the single dependency subcommand group and mutation rendering. **Done when:** focused CLI tests pass in human/JSON modes and the aliases contain no duplicate handler or help entry.

- [x] **16. Add CLI dependency list and Task-show composition** *(target 300–500 changed LoC; depends on 15).* RED: add behavioral tests for unfiltered and direction-filtered list envelopes, server ordering, empty directions, human `Blocks` then `Blocked by` sections, omission when a Task has no dependencies, and Task-show JSON aggregate-only shape. GREEN: implement one shared human direction renderer used by list and show and consume server projection types for JSON. **Done when:** focused CLI tests pass and Task show never fetches or exposes full directions solely for its JSON summary.

- [x] **17. Integrate CLI Start/Move dependency outcomes** *(target 200–350 changed LoC; depends on 13–16).* RED: test `--ignore-dependencies` mapping, nonzero human and JSON confirmation-required exits, confirmation handling before Execution Target selection, and Move's new `--json` behavior. GREEN: adapt lifecycle request construction and typed outcome rendering without interactive prompting. **Done when:** focused CLI lifecycle tests pass for Start and executable Move, and rerunning with the flag is the only CLI proceed path.

- [x] **18. Extend Workflow instruction awareness through the canonical dependency reader** *(target 300–500 changed LoC; agent-awareness workstream).* RED: mirror existing Task Comment awareness tests for initial assignment, reassignment, and compaction reminder with zero/nonzero unsatisfied counts, prove a dependency change during an already-visible instruction neither steers runtime context nor rewrites persisted/model-visible history, and add a composition boundary test showing awareness count equals `workflowview.TaskDependencies` summary across manual-terminal, ordinary terminal, and reopen changes. GREEN: replace the comment-only seam with a typed Task-awareness source; compose existing comment counting with the injected `workflowview.TaskDependencies` focused count, carry both counts through execution config/meta-context/inspection/prompt arguments, and render only count plus `kent task show <short-id>` when nonzero. **Done when:** focused view/composition/runtime/workflowruntime/prompt tests pass, `workflowstore.Store` exposes no dependency-count derivation, and awareness queries occur only when the existing instruction selector chooses a new message.

- [x] **19. Cut Desktop API models and client calls to the locked protocol** *(target 350–500 changed LoC; Desktop workstream).* RED: update TypeScript schema/client tests for dependency projections including the typed Add-availability union, board progress, Add/Remove/List, atomic-create intent, Start/Move outcome, typed errors, related event IDs, and rejection of malformed/legacy payloads. GREEN: extend models, strict Zod schemas, API composition, mutation inputs, and query keys; schemas parse server capacity facts without exporting or comparing a Desktop literal `50`. **Done when:** focused Desktop API tests pass with no compatibility normalizer, client-owned cardinality rule, sentinel absence, or Tauri/native import in feature code.

- [x] **20. Deepen `InteractiveChip` with progress content and add Board-owned composition** *(target 250–450 changed LoC; depends on 19).* RED: through public `@/ui` and Board interfaces, test semantic incomplete/complete progress values, progress accessibility structure (`role`, current, and maximum), native activation, and click/drag isolation without asserting copy, class names, colors, styles, DOM placement, or icon appearance. GREEN: add a generic progress-content wrapper that composes `InteractiveChip` rather than rendering its own button/chrome, extend the canonical `InteractiveChipTone` maps with `success` only if required, extend `KanbanCardVM`, and compose the wrapper inside `features/board`; use shared/i18n semantic inputs for the accessible label and open Task Detail with Dependencies focus. **Done when:** focused UI/Board tests pass, the wrapper contains no dependency business rule, Board imports only permitted API/app-facade/UI/shared entrypoints, and structural review proves there is still one chip base, size map, focus/disabled behavior, and interaction implementation.

- [x] **21. Render Task Detail-owned Dependencies composition** *(target 350–500 changed LoC; depends on 19–20).* RED: add feature-boundary tests for the typed `blocked-by` then `blocks` section order, empty/populated semantic groups, related Task identity/title data, Add/Remove/row-selection callbacks, server-supplied satisfaction, and progress accessibility structure. Do not assert headings/copy, color/style/classes, truncation pixels, or status icon/glyph appearance. GREEN: keep the static virtual-list item and all Dependencies area/row/status/mutation composition in `features/task-detail`, using existing/generic `@/ui` rows/buttons and the same `InteractiveChip`-backed progress wrapper from slice 20; no Board or dependency feature import is allowed. **Done when:** focused Task Detail interaction/accessibility tests pass for loading/error/empty/populated states, rows never calculate status/satisfaction locally, and architecture lint confirms feature isolation and canonical chip reuse.

- [x] **22. Implement related-Task replacement and reliable Dependencies scrolling** *(target 300–500 changed LoC; depends on 21).* RED: add sidebar/virtual-list tests proving row selection replaces the current Task, chip/View-deps opens or replaces with typed Dependencies initial focus, asynchronous load still scrolls the keyed item after measurement, keyboard focus does not move, and closing resolves the original sidebar lifecycle promise once. GREEN: add the typed initial-focus state, keyed pin/scroll behavior, and sidebar destination replacement operation. **Done when:** focused navigation tests pass with no history stack, Back action, restoration state, or orphaned board selection.

- [x] **23. Implement optimistic Remove and typed dependency-impact invalidation** *(target 350–500 changed LoC; depends on 21–22).* RED: add mutation tests proving immediate removal from only the open Task direction/summary, disabled behavior while disconnected, authoritative refetch plus transient error on failure, and success invalidation of both Task details, project boards/card pages, and Task lists. Add table-driven subscription tests proving: same-Workflow board refresh behavior is preserved; foreign-Workflow `dependencies_changed`, `deleted`, `moved`, `approved`, and `completed` refresh the board; foreign comments, labels, questions, interrupts, resumes, and ordinary updates do not; direct-current-Task events keep refreshing Task Detail; related-row title/status/deletion actions refresh it; related comments/labels do not; and `dependencies_changed` involving a related Task plus a third Task does not refresh unless the open Task is in the event pair. Add two approval integration fixtures: terminal Approval refetches the foreign board and changes the blocker progress to satisfied, while nonterminal Approval performs the safe refetch and preserves the prior progress. Keep the Project-scoped `workflow/deleted` invalidation and the Workflow A deletion → surviving Workflow B refetch fixture. GREEN: implement named typed predicates shared by the board and Task Detail event policies plus the single optimistic cache patch; do not replace them with an all-Task-event branch or infer terminality client-side. **Done when:** focused mutation/subscription tests pass, terminal Approval cannot leave stale progress, nonterminal Approval preserves authoritative progress, unrelated foreign-Workflow activity causes no board/detail refetch, and refreshed server data always replaces optimistic presentation state.

- [x] **24. Route dependency Add through server-owned availability and the ordinary New Task form** *(target 300–500 changed LoC; depends on 19, 21–22).* RED: add navigation/form tests for each subsection's fixed new-Task role, origin Project/Workflow, default origin source workspace with selector still available, hidden typed relationship input, cancellation creating nothing, server failure creating nothing, `limit_reached` disabling Add with an accessibility-description relationship, `available` enabling it without comparing counts to a literal, and successful replacement/invalidation. GREEN: make Task Detail consume the direction's typed availability and extend the New Task destination/form inputs plus existing create request; the store still re-runs the authoritative domain policy on submit. **Done when:** focused Desktop creation tests plus server atomic-create tests pass, no TypeScript dependency-cap literal or client capacity calculation exists, and there is no second create/mutate sequence or cross-feature import.

- [x] **25. Deepen the initiating-action controller for dependency continuation** *(target 350–500 changed LoC; depends on 13–14 and 19).* RED: add pure controller tests for the discriminated dependency-confirmation and Execution Target states, stable setup-operation/action identity, proceed intent surviving `selection_required`, duplicate submission prevention, and Close/View-deps/Cancel clearing all pending state. GREEN: generalize the existing Execution Target controller instead of layering a second Board state machine. **Done when:** focused controller tests pass and board drag/start/move disabling derives from this single controller's running or pending state.

- [x] **26. Integrate the Desktop confirmation dialog and initiating surfaces** *(target 300–500 changed LoC; depends on 20–25).* RED: add integration tests for Start and executable Move showing the locked dialog before any target selector, primary Start proceeding, View deps clearing the action and replacing/opening the blocked Task at Dependencies, later target selection retaining proceed intent, and dismissals making no mutation. GREEN: connect board/detail actions, dialog, controller, and API outcomes using the initiating operation's exact action. **Done when:** focused Desktop integration tests pass for warning→proceed→selection/applied and warning→View deps/close paths without persisted acknowledgement.

- [x] **27. Update active protocol version, CLI documentation, and generated task skill** *(target 200–350 changed LoC).* After server, Go client, and Desktop contract shapes are green, bump `shared/protocol/version.json` exactly once from `75` to `76` and update active handshake fixtures. Invoke `docs-writing` before updating public CLI documentation; document only canonical `task dep`, list/mutation contracts, and `--ignore-dependencies`. Update the repository `kent-tasks` skill to the same behavior while leaving product specs unchanged. **Done when:** protocol tests, docs tests/build relevant to edited pages, and focused review pass; aliases remain undocumented and no deferred feature is described.

- [x] **28. Audit ownership, duplication, Desktop boundaries, tests, and locked scope.** Run structural searches and focused review proving all relationship business invariants and Add availability have one `workflow.TaskDependencyPolicy`, no schema trigger/check duplicate, and one locked `workflowstore` mutation orchestrator; all writes route through it; all satisfaction/ordering/counts come from `workflowview` or the paged board query; and all production SQL is migration/query-source generated. Prove Board and Task Detail import no feature, the progress wrapper composes canonical `InteractiveChip` and introduces no second chip base/sizing/focus/disabled/interaction implementation, shared dependency code contains no feature composition, TypeScript contains no dependency-cap literal/calculation, and tests contain no literal accessible copy, visual style/color/class, or icon-appearance assertions. Also verify the existing event-precision and deferred-scope exclusions. **Done when:** `git diff --check`, generated-query/schema/domain-policy guards, Desktop architecture lint, duplicate-symbol/code search including chip ownership, banned-test-assertion review, event-action matrix review, and the explicit scope checklist are clean.

- [x] **29. Run focused end-to-end dependency verification.** Exercise the real metadata/store/view/service/transport/client/CLI composition for add/remove/idempotency, scope/rules/cycles/limits/race, satisfaction/reopen, deletion cleanup, constructible atomic related-creation rollback cases, board aggregates, Start/Move negative-preflight ordering and warning/proceed/selection continuation, canonical awareness/projection count equality, and agent-awareness lifecycle. Exercise Desktop tests for chip/detail/navigation/create/remove/live refresh/disconnected/continuation behavior, including terminal and nonterminal foreign-Workflow Approval, the other positive dependency-impact actions, negative unrelated foreign-Workflow actions, and Workflow A deletion refreshing surviving Workflow B dependency progress. **Done when:** all focused commands run through `./scripts/test.sh ...` and the relevant Desktop package scripts pass repeatedly without timing flakes or fake-only production hooks.

- [x] **30. Hand off design review, browser QA, and repository-wide non-Rust verification.** The implementation agent does not perform manual browser QA; the separate QA agent owns the isolated server, `pnpm --dir apps/desktop dev:browser`, `qa-harness`, and `ui-design` verification. The implementation agent runs the available architecture checks, focused non-Rust suites, frontend lint/typecheck/tests/build, and server build; do not build or test frozen Rust/TUI. **Done when:** implementation verification is recorded and the explicit task scope assigns browser/end-to-end acceptance to the QA agent.

## Testing

## Review follow-up

- [x] Reconcile dependency-list projection with the authoritative workflow orchestration contract: omit empty directions for both unfiltered and filtered requests, and return `directions: []` when no relationships exist.
- [x] Add regression coverage for empty unfiltered and filtered dependency-list responses.
- [x] Re-run the previously reported transcript replacement test directly; it passes in isolation.
- [x] Confirm no new implementation or compliance findings remain in re-review.
- [x] Human-authorized scope recorded in KENT-210 comment `comment-6ded210e-8fdb-455e-ae37-fb8ce544e128`: browser visual QA and the separate end-to-end CLI/GUI acceptance pass belong to the separate QA agent, not this implementation task.
