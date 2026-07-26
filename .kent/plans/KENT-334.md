## Recon

- Domain definition and constraints are already documented in:
  - `docs/dev/specs/workflow-orchestration.md` (task-owned current nodes, pending approvals, fan-out/join expectations, interrupt/resume behavior).
  - `docs/dev/specs/terminology.md` (workflow vocabulary, transition branch identity, execution-context language).
- Persisted workflow state is currently implemented around old workflow run/placement entities:
  - `server/metadata/migrations/00005_workflow_orchestration.up.sql` and related down migration define `task_runs`, `task_node_placements`, `task_transitions`.
  - Normalization migrations extend this model:
    `00013_normalize_task_run_records.up.sql`,
    `00014_normalize_task_transition_edge_records.up.sql`,
    `00015_normalize_task_node_placement_records.up.sql`,
    `00016_add_task_node_placement_index_fields.up.sql`,
    `00024_remove_workflow_graph_denormalization.up.sql`.
  - Generated store assets still mirror that model:
    - `server/metadata/queries.sql`
    - `server/metadata/sqlitegen/models.go`
    - `server/metadata/sqlitegen/queries.sql.go`
    - `server/metadata/sqlitegen/sqlite_schema.sql` (legacy schema shape)
- Workflow store interfaces and stores are run/placement-first:
  - `server/workflowstore/store.go` (`RunRecord`, `PlacementRecord`, run selectors and completion helpers).
  - `server/workflowstore/runs.go`, `tasks.go`, `transitions.go`, `manual_moves.go`, `queries.go`.
  - Public store APIs in use: `ListRunnableRuns`, `GetRun`, `ClaimRun`, `InterruptRun`, `ResumeTaskRuns`, `CompleteRun`, `AttachRunSession`, `GetRunStartContext`, transition source/target run usage.
- Runtime execution path is currently run-aware:
  - `server/workflowrunner/starter.go` runtime start interface and flow is built on run records.
  - `server/workflowruntime/completion.go`, `server/runtime/engine.go`, `server/runtime/workflow_runtime.go`, `server/runtime/workflow_terminal_state.go`, `server/runtime/compaction_runtime_state.go`.
  - Tool/identity coupling:
    - `shared/runtimeids/runtime.go` (`run_id` in execution identity parsing),
    - `server/tools/execution_identity.go`,
    - `server/tools/definitions.go` (`RequiresWorkflowRun` command/tool flag exposure),
    - `server/runtimewire` adapters.
- Service/read-model boundaries still rely on run/placement:
  - `server/workflowsvc/service.go` source/target selectors, complete/move/approve payloads, resume and start responses.
  - `server/workflowview/{task_detail.go,task_projector.go,board.go,attention.go,activity.go,task_status.go,task_list_status_sort.go}` depend on placement/run facts and run_count.
  - `server/workflowattention` attention selectors and targets still reference run IDs and transition source runs.
- Session and history layers still carry workflow run fields:
  - `server/session/event_format_v1.go`, `server/session/event_history_v1.go` (`WorkflowRunID` in `HistoryReplacementRecord`),
  - `server/session/types.go`,
  - `server/session/migration_legacy.go` migration of historical workflow replacement records.
- Go API/contracts and protocol still expose removed concepts:
  - `shared/serverapi/workflow.go` and sibling workflow DTO files (run/placement DTOs, run_count, RunIDs).
  - `shared/clientui/attention_notification.go` and related client UI payloads.
  - `shared/runtimeids` runtime/session config contracts.
  - the Go remote client under `shared/client` and Desktop TypeScript API contracts.
  - `shared/protocol/version.json` owns the current protocol version; one Go/Desktop protocol bump is required on cutover.
- CLI and UX surfaces still present `run`-based selectors and renderers:
  - `cli/kent/task_complete_command.go` (including `--run` UX and run resolution),
  - `cli/kent/task_complete_command_test.go`, `task_list_projection.go`, `task_show_command.go`, `task_list_command.go`, `help/task.txt`.
  - Desktop/client references in:
    - `apps/desktop/src/api/models.ts`
    - `apps/desktop/src/api/schemas/*` (`workflow*`, attention/task schema)
    - `apps/desktop/src/features/*` task detail/board/notification/attention components and tests referencing `run_id`, `placement_id`, `run_ids`, `run_count`.
- Migration/restart behavior surfaces that need one-way cutover validation:
  - Startup/restore path touchpoints: `server/launch`, `server/startup`, `server/runtimewire`, `server/sessionruntime`.
  - Session continuity and interrupt/rebuild behavior may require updates where workflow execution is re-bound to live scope (`server/runtimeview`, compaction/reconnect paths).
- Existing tests provide reusable migration/state coverage but are legacy-shape:
  - `server/workflowstore/*_test.go` (parallel/joins/question/approval/interrupt cases),
  - `server/metadata/workflow_attention_candidates_test.go`,
  - desktop and CLI integration tests,
  - protocol compatibility/fixture tests under `shared/protocol` and `shared/client`.
- Query and projection coupling confirms breadth of replacement:
  - `server/metadata/queries.sql` contains run_count/runnable/active count projections, placement/runnable filters, current run fact builders, and task list sorting dependencies.
  - read path is currently anchored on `task_node_placements` + `task_runs` joins in many boards/activity/status projections.
- High-signal cross-module deletion surface already present (likely candidates for replacement ownership shift):
  - task current work state (`server/workflowsvc`, `server/workflowview`, `server/workflowattention`)
  - session retention/reselection (`server/workflowrunner`, `server/runtimewire`, `server/tools/definitions.go`, CLI completion commands, desktop open/interrupt actions)
  - completion/transition application (`server/workflowstore/transitions.go`, `server/workflowstore/manual_moves.go`)
  - migration hard cutover and state replay (metadata migration stack + session migration legacy records).

## Design

### Confirmed decisions

- Task Cancel is removed in this breaking release. Kent does not introduce a
  Current-Node Cancel lifecycle. Migration consumes legacy cancellation once:
  representable canceled Tasks become terminal Current Nodes; only canceled
  Tasks whose Workflow has no terminal Node are deleted after their Sessions
  become workflow-neutral and external artifacts remain untouched.
- The frozen `tui-rs/` tree is outside KENT-334. Do not edit, build, test,
  migrate, regenerate, or use Rust artifacts as protocol constraints.
- Forced completion outside an agent Session accepts either a Session selector or an unambiguous Task selector. It does not add a Current Node selector.
- Manual movement is a Task-level action. Kent does not offer movement of one
  Current Node within parallel work. After Task-wide Interrupt has fully
  stopped the Task, one manual Move may atomically replace all parallel Current
  Nodes with terminal or backlog state. A serial, quiescent forward Move through
  one concrete latest-definition Workflow Edge may create its executable
  target; missing-edge, history-derived/backward, and parallel executable
  movement remain rejected.
- Manual Move does not cancel or join a waiting Question scope itself. Move is
  rejected while that scope remains; the operator must answer the Question or
  otherwise wait for scope retirement before retrying Move.
- Desktop does not expose Run or Node Placement detail today. The cutover must not add a replacement persistence-oriented “Current work” UI merely to mirror the new storage model.
- The Task Activity tab remains during this cutover, but the no-history decision remains authoritative. Activity contains Comments and entries derived from retained Session creation, rendered as “Session started.” It does not retain workflow movement, Node completion, interruption, attempt, or diagnostic history.
- Restoring Activity to the full worklog contract described by KENT-212 belongs to KENT-212, not this task.
- Task detail shows the total retained Session count. It offers open buttons only for running Sessions; non-running retained Sessions remain available through the Session picker.
- Each running Session in Task detail has Open and Interrupt actions. The Task-wide Interrupt action is shown when several executions are live or a script is live.

## Architecture

### Ownership and module seams

- `server/workflowstore` remains the only durable owner of workflow Task state. Its public interface becomes Task/current-state oriented: create/start a Task, load Current Nodes, prepare a current Node for execution, apply completion, resume/interrupt, consume an Approval, and replace Task state through manual Move. Callers do not compose lower-level current-node, fan-out, Session-association, or Approval writes.
- Deepen the existing scheduler/orchestration code into one `Workflow Execution` server module. It replaces durable Run claiming and polling, owns the context-aware workflow mutation permit, volatile Automatic Intents, automatic-capacity admission, runtime gates, task affinity, and Immutable Live Snapshots.
- `server/workflowrunner` remains the execution adapter behind Workflow Execution. It reuses `launch.Planner`, runtime wiring, worktree restoration, role resolution, and script execution, but receives a prepared Current Node start context rather than loading or mutating a Run.
- `server/sessionruntime.Authority` remains the sole owner of Exact Execution Scopes and agent resource generations. It indexes workflow scopes by a Current Node reference and exposes process-local scope identity; it does not persist or expose a replacement execution entity.
- `server/workflowsvc` remains the API/application edge. Workflow lifecycle methods delegate to Workflow Execution; definition, label, comment, and ordinary Task metadata operations continue to use their existing stores/read models.
- `server/workflowview` consumes durable current-state facts plus one Workflow Execution snapshot. It never infers liveness from SQLite, Session metadata, transcript rows, or client state.

### Task-owned durable state

- Introduce one typed natural reference for current work: Task ID, Workflow Node ID, and optional Transition Branch Key. It is a value, not an entity identity. The same shape is used by the store, Workflow Execution, live-scope matching, attention, and wire DTOs.
- Replace Placement and Run rows with `task_current_nodes`, which has no `id` column. Enforce aggregate cardinality, not only reference uniqueness:
  - serial rows use a partial unique index on `task_id` where Transition Branch Key is `NULL`, so a Task has at most one serial Current Node regardless of Node;
  - parallel rows use a partial unique index on `(task_id, transition_branch_key)` where the branch is present, so each expected branch has exactly zero or one Current Node while still allowing different branches to occupy the same Workflow Node;
  - `(task_id, transition_branch_key)` on a parallel Current Node is a foreign key to the active fan-out branch primary key, and each fan-out branch belongs to the Task singleton fan-out through `task_id`;
  - insert/update triggers reject a serial Current Node while an active fan-out exists and reject creation of an active fan-out while a serial Current Node exists; the aggregate transaction removes one mode before inserting the other;
  - deleting a Current Node that still owns a pending Approval is rejected, requiring the aggregate transaction to consume/delete the Approval first.
  Absence remains SQL `NULL`, never an empty key.
- A Current Node stores only:
  - its natural reference;
  - the exact entering Transition Branch identity needed to resolve the latest prompt and context policy;
  - materialized current input and prior-node value maps needed by its prompt or downstream propagation;
  - nullable Session binding for an agent Node;
  - current scheduling state for executable work;
  - nullable interruption reason, structured detail, and occurrence time.
- Executable scheduling state distinguishes ready, admitted/executing, interrupted, and unrecoverable failed state. Admitted/executing is a restart marker, not proof of liveness; running, queued, and Question status still require the Immutable Live Snapshot. Start timestamps, completion timestamps, attempt counters, completion-mode snapshots, protocol-violation counts, and execution diagnostics do not survive leaving the Node.
- Remove Task Cancel completely. The post-cutover product has no Task Cancel CLI/RPC/service/store operation, request DTO, canceled Task state/status/native-state, cancellation timestamp/reason, cancel capability, or canceled project event. Task Interrupt remains the resumable stop operation, and Task Delete removes a quiescent Task. Ordinary `context.Canceled`, Exact Execution Scope stopping, Question cancellation, and script/process cancellation remain runtime mechanisms and are not Task lifecycle state.
- Node kind, role, script path, completion mode, outgoing Transitions, and Runtime Parameter Contract remain Workflow-definition facts. The store loads them from the latest definition when Start or Resume prepares an executable Current Node. A live scope receives one immutable in-memory contract for its lifetime.
- Applying a Transition carries forward a typed materialized value environment rather than querying prior transitions. Derived wiring determines the minimum values each target needs for `.Inputs`, `.Nodes`, later propagation, or Join aggregation. Missing newly-required values produce the specified typed validation error.

### Parallel fan-out and Join state

- Model active parallel state as a Task-owned singleton fan-out record plus dependent branch rows, all without a fan-out/batch ID:
  - the singleton is keyed by `task_id` and stores no Workflow, Node, edge, provider, outgoing-Transition, or other invocation snapshot; it exists only as the Task's active-parallel mode owner;
  - branch rows have primary key `(task_id, transition_branch_key)`, reference the singleton by `task_id`, and store the expected branch set, arrival state, and only the materialized values needed by the Join;
  - parallel Current Nodes reference one of those branch rows through their Transition Branch Key.
- A branch carries the same Transition Branch Key as it moves through pre-Join Nodes. Reaching the latest-definition Join removes that branch's Current Node and records only its materialized arrival values. The expected Transition Branch Key set remains frozen, but Join identity, providers, aggregation requirements, and outgoing Transition are loaded from the latest Workflow definition on arrival/application.
- When every expected branch has arrived, the same transaction validates that the latest definition maps that frozen branch set to one valid common Join, derives the Join output from retained branch values using the latest provider configuration, deletes the entire fan-out state, and applies the latest Join outgoing Transition. Missing/ambiguous Join topology or newly required unavailable values returns the approved typed graph/input validation outcome; compatible provider or outgoing edits take effect.
- Nested fan-out remains rejected by existing graph validation, so a Task has at most one active fan-out aggregate. Duplicate target Nodes in different branches remain distinguishable by the natural Current Node reference.

### Durable Session associations

- Add a nullable owning Task relation to the authoritative Session metadata row. This is the source for Task Session count, Activity Session creation, workflow-Session restrictions, and Session retarget checks.
- Add a dependent Session-to-Workflow-Node association relation with no independent ID and no duplicated Task column. It records Session ID, Workflow Node identity, optional Transition Branch Key, and latest association time; Task ownership is always joined from the authoritative Session row. Serial associations use a partial unique index on `(session_id, node_id)` where branch is `NULL`; branch associations use a partial unique index on `(session_id, node_id, transition_branch_key)` where branch is present, so SQLite `NULL` behavior cannot create duplicate serial facts.
- Association writes accept the expected Task at the aggregate boundary and succeed only when it equals the Session's owning Task; repeated association of the same natural key upserts its association time instead of appending an attempt. Latest matching Session selection has total order `associated_at_unix_ms DESC, session_id DESC`, including equal-time candidates.
- Context Source selection queries this association relation directly:
  - `immediate_source` uses the source Current Node's Session;
  - selected-node and previous-target modes choose the latest matching retained Session association;
  - when the source Current Node is parallel, the query additionally requires its Transition Branch Key.
- The selected Session is materialized into the target Current Node when a Transition applies or an Approval snapshot is created. `new_session` remains unbound until launch creates the Session; continuation modes retain their selected Session. Session planning and cloning continue through the existing launch/session infrastructure.
- Remove workflow Run state from Session metadata and runtime inspection. A Session's workflow ownership is resolved from metadata by Session ID, and its Node associations are resolved from the association relation. Do not duplicate these facts in `events.jsonl`.

### Pending Approvals

- Replace pending rows in transition-history tables with Task-owned Approval state. A pending Approval may keep its own UUID v4 identity because Approval is a retained interaction target; it is not an execution identity.
- Store the frozen source Current Node reference, source Session when present, Workflow Version, selected Transition and branch snapshots, materialized values, target snapshots, effective edge configuration, and frozen Context Source resolution. Dependent branch snapshot rows have no independent IDs.
- Enforce at most one pending Approval for one natural source Current Node with separate partial unique indexes on `(source_task_id, source_node_id)` for serial sources and `(source_task_id, source_node_id, source_transition_branch_key)` for parallel sources. Insert/update triggers require that exact source Current Node to exist; Current Node deletion is blocked until its Approval is consumed or removed.
- Applying an Approval validates that its source Current Node remains current, atomically consumes the Approval, replaces the source Current Node with its targets or fan-out state, and returns typed affected Current Nodes/Sessions. A Task-wide manual Move that supersedes its source or Task/Workflow/Project deletion removes the Approval rather than retaining a resolved row. There is no standalone Workflow Transition Approval Reject action, API, service operation, or UI control; dismissing execution-target selection leaves the Approval pending.
- Parallel branches may own independent pending Approvals. A Task-wide manual Move that replaces parallel state removes every pending Approval belonging to the replaced Current Nodes.
- Pending Approval is the sole `waiting_approval` source of truth; do not duplicate it as a Current Node scheduling state. One shared execution-eligibility predicate is used by Automatic Intent admission, explicit admission, Resume, startup recovery, and forced idle completion: the executable Current Node must still be current, have no pending Approval whose source reference matches it, and satisfy the operation's scheduling/live-scope requirements. A pending-Approval source may remain current for display and Approval validation but is never executable, resumable, recoverable into ready work, or force-completable.

### Atomic mutation model

- Each lifecycle operation is one aggregate-level store transaction. Completion, Approval, Start, Task-wide Move, and Join application never expose intermediate source/target states.
- Store mutation results contain typed removed/created/updated Current Node references, affected Session bindings, pending/resolved Approval projections, attention changes, handoff display facts, and successor Automatic Intents. They contain no Run/Placement IDs. Workflow Execution enqueues successor intents after commit; when completion came from a live scope, it holds them until that source scope has retired.
- Completion through a live scope is accepted only after Workflow Execution verifies that the supplied Exact Execution Scope is the current scope for the referenced Current Node. The store then revalidates that the source Node is still current inside the transaction.
- Forced completion without a live scope resolves exactly one idle executable Current Node by Session or Task under the same mutation permit. Ambiguous or live matches fail without mutation.
- Start and Resume resolve the latest Workflow definition before admission. Approval uses its frozen snapshot. Script execution reads the latest script path when its scope starts.
- Manual Move is Task-wide. A quiescent parallel Task can replace all Current
  Nodes only with backlog/start or terminal state. A serial quiescent Task may
  move forward to an executable target only through one concrete
  latest-definition Workflow Edge; missing-edge/history-derived/backward and
  parallel executable movement are rejected without a source-branch selector.

### Workflow Execution and Exact Execution Scopes

- Remove durable claims, run generations, runnable scans, startup reconstruction, polling, and scheduler reconciliation. Automatic work is represented only by the existing specified in-memory typed intent queue.
- A committed transition may return Automatic Intents for newly executable Current Nodes. Workflow Execution enqueues them after commit and admits them subject to automatic concurrency. Explicit Start, Resume, Approval, and executable Move call the same admission path but are not rejected by automatic capacity.
- Admission is keyed by Current Node reference:
  1. under the mutation permit, revalidate current state and register a runtime gate;
  2. persist the admitted/executing restart marker;
  3. release the mutation permit while the gate prevents conflicting lifecycle changes, then prepare the latest execution context and Session binding through the runner;
  4. start an Exact Execution Scope in `sessionruntime.Authority`;
  5. reacquire the permit and replace the gate with the live scope, or mark the still-current Node interrupted if preparation/start fails.
- Adapt `sessionruntime.WorkflowExecutionRef` to carry the Current Node natural reference instead of Run ID/generation. `ExecutionScopeID`, execution generation, and resource generation remain process-local safety facts already owned by `sessionruntime`; only the scope ID is used to distinguish a stale predecessor from a resumed successor.
- Runtime workflow configuration becomes live-scope control data: Current Node reference, advertised completion contract, instructions, and a controller callback. Protocol-violation counting is scope-local. Reaching the cap stops the scope and records interruption on the Current Node; no counter is persisted.
- All workflow-visible runtime outcomes enter Workflow Execution as typed intents. Runtime, runner, and attention code do not mutate current state directly.
- Accepted live completion commits the Current Node replacement but keeps successor intents behind the source scope's retirement gate. Runtime terminalizes, the Authority retires the exact scope, and only then may Workflow Execution admit a successor; continuation never overlaps the predecessor on the reused Session.
- Task Interrupt owns every controller representation of work for the Task. Under the mutation permit it removes queued Automatic Intents, removes retirement-held successor intents, cancels matching admission gates, marks matching scopes as stopping, and records interruption immediately for still-current eligible Nodes represented only by removed intents. It releases the permit while gates/scopes join, then reacquires it to record interruption for their references that remain current and to verify that no Task intent/gate/scope survives. A retiring predecessor cannot release an intent removed by Interrupt, so no successor starts after Interrupt returns.
- A Session-targeted Interrupt selects only that agent Session's gate/scope and never removes unrelated Task Automatic Intents or selects a script. Resume cannot register a successor gate/scope until the predecessor has retired. A late completion from the stopped scope fails exact-scope validation.
- On server startup the queue, gates, and scope maps begin empty. A single current-state recovery mutation changes admitted/executing executable Nodes to interrupted with the restart reason; ready/interrupted Nodes are not automatically started.
- Workflow Execution exposes one immutable snapshot containing exact scopes, automatic intents, and runtime gates. Shutdown cancels and joins its owned workers and scopes through existing Authority lifecycle APIs.
- Production composition constructs one Workflow Execution controller, runner, Authority, and workflow service graph; injects the controller into every lifecycle service and runtime callback; and synchronously completes current-state recovery before the Core is returned or any request can be served. Authority retirement callbacks feed the same controller. Shutdown first stops controller admission and joins its gates/scopes/runner work, then closes Authority and persistence so no callback can outlive its owner.

### Serialized edit and deletion mutations

- Immutable Live Snapshots are read-model inputs, not commit-time mutation authority. Workflow graph Save when live-state policy matters, Task Delete, Workflow Delete, and Project Delete enter Workflow Execution under the mutation permit and revalidate exact scopes, queued and retirement-held Automatic Intents, and runtime gates for every affected Task before deciding blockers or mutating.
- The shared mutation permit serializes lifecycle, graph-edit, and deletion
  mutations. Kent does not add a Task/Workflow/Project exclusion mechanism
  unless a deterministic race demonstrates that the permit cannot preserve
  deletion safety through required external cleanup.
- Task Delete, Workflow Delete, and Project Delete retain the shared permit
  for their safety-critical durable mutation and revalidate live controller
  state before that mutation. Any need to release it for external cleanup must
  first produce a deterministic race and an explicit design decision.

### Runtime and transcript implications

- Keep the completion parser/schema generation, prompt construction, completion fence, launch planner, resource replacement, and script process modules. Replace only their workflow-control identity and persistence adapters.
- Remove `WorkflowRunID` from new history-replacement events, Session workflow metadata, compaction state, runtime terminal state, tool execution identity, script input/environment, and workflow instructions. Compaction identity is Session contract generation plus current live scope; Resume derives a fresh workflow contract from current state.
- Remove durable `session.Meta.WorkflowSession`/`sessions.metadata_json.workflow_session` state rather than narrowing it to another workflow identity. Persisted workflow inspection starts from Session ID, resolves its owning Task and matching current Node/direct Node association, and builds a live contract only from current state; a retained Session with no matching Current Node opens as an ordinary retained Session. The hard migration may decode the old object only to validate migration input, strips it from the migrated row, and no post-cutover control flow reads its obsolete Run ID.
- All Session metadata writes share a workflow-neutral serialization shape. Read-only legacy artifact decoders may accept the old `workflow_session` object, but every rewrite and SQLite upsert omits it.
- Direct Session owning-Task is the durable workflow-origin fact used by `launch.ResolveSessionCaller` and policies that intentionally apply to every retained Task Session, including workflow-only runtime-control restrictions. Exact Execution Scope plus the Session-bound Current Node is the live activity fact. Replace Run-bearing runtime/session status DTOs with Task-owned status containing Task/Workflow identity, optional Current Node reference, and live `Active` derived only from the Immutable Live Snapshot: live Task Sessions expose active/current facts, retained non-current Task Sessions retain origin without claiming activity, and ordinary Sessions expose no workflow status.
- Never rewrite `events.jsonl` to erase legacy fields. Version-1 decoders ignore obsolete Run fields in existing records, new writes omit them, and no control flow reads them. The metadata migration removes the authoritative Session workflow Run field from SQLite/session snapshots without changing previously sent provider history.
- Generic runtime/model step IDs and the `kent run` command remain intact; only workflow-domain Run concepts are removed.

### Read models, attention, and Activity

- Replace Placement/Run projections with typed Current Node projections. Status carries Current Node and Session/current-script references as applicable, never execution IDs.
- Workflow Execution supplies one snapshot per read operation. Batch board/detail projections match durable Current Nodes to live refs in memory. Paginated Task-list/status/attention queries receive the bounded live snapshot as structured query input so filtering and sorting remain server-side and cursor-correct; clients do not reconcile status.
- Rebuild attention candidates from:
  - pending Approval rows;
  - current interrupted Nodes;
  - live Questions from the Exact Execution Scope/prompt registry;
  - existing workflow validation blockers.
  Question identity, Approval identity, Session ID, or Current Node reference becomes the focus target. Attention notification finalization reuses the existing broker/finalizer seam with the new typed projections.
- Ordinary and runtime-approval Question issue/await/clear state is volatile Exact Execution Scope plus prompt-registry state. Issuance validates the exact scope and direct Session/Current-Node Task ownership, registers the Question and Task/Session/Current-Node attention target, awaits through the prompt registry, and clears on answer, cancellation, skipped-batch handling, or scope retirement. A stale scope cannot issue or clear a successor's Question. No Current Node column or other durable shadow waiting state is introduced.
- Question answers are addressed by Task ID plus Question identity, not by Run or Current Node selector. Workflow Execution resolves the live Question through the prompt registry/Exact Execution Scope, then validates direct Session ownership and the Session-bound Current Node against the Task. Zero matches return the existing stale/not-pending class, several matches return a typed ambiguity error, and ownership mismatch fails without submitting a response. Request idempotency compares Task ID, Question ID, and answer payload only; project events carry Question/Session or Current Node facts without a Run ID.
- Keep the Activity read-model seam but reduce its data source to a paginated union of durable Comments and Sessions owned by the Task. Use a minimal typed union (`comment`, `session_started`) with UI-neutral payloads and deterministic source-derived cursor keys; remove server-authored summaries and all transition/run payloads. Desktop localizes `Session started`. KENT-212 can extend this union and add a new authoritative worklog later.
- Task detail gets retained Session count from the Session ownership relation. Open/Interrupt controls use only live Session refs from the snapshot; current scripts use Current Node plus latest Workflow script path. Non-live retained Sessions remain discoverable through the existing Session picker.
- Start/Resume/Approve/Move/Complete responses use shared Current Node and Session DTOs. Remove Run/Placement/count/history fields in one protocol cutover across the Go server/remote client and Desktop TypeScript contracts.
- Workflow editor Context Source help and option labels use Session language for previous-target selection. This is a localized client-copy cutover only; typed Context Source values and behavior do not change.
- The embedded `prompts/skills/kent-workflows/SKILL.md` is a production prompt asset. Its Script section documents `_kent` as Task, Node, and optional Transition Branch identity only; its Context Source section selects retained Sessions rather than completed Runs. Keep the guidance operational and avoid teaching removed compatibility concepts.
- The public workflow guide under `docs/src/content/docs/workflows.md` remains the user-facing owner for workflow concepts and usage. Cut it over atomically to Task-owned Current Nodes and retained Sessions, the shared structured Script `_kent` identity, Interrupt/Resume behavior, Session-based Context Sources, Task Activity scope, and current CLI completion selectors. Public prose must describe the resulting product timelessly and must not teach removed Run, Placement, count, selector, history, or Task Cancel concepts.

### Migration and hard cutover

- Land one forward migration that creates the new current-state, active-fan-out, Session-association, and pending-Approval structures; stages and validates projected data; then drops the old Run/Placement/transition-history tables, views, indexes, and generated query assumptions in the same transaction.
- Normalize legacy canceled Tasks before ordinary current-state, fan-out, value-environment, or Approval projection. Resolve the terminal outcome and harvest any Session links needed by retained artifacts first; then discard every pending Approval and dependent edge snapshot, active/waiting Placement and unfinished Run, active fan-out/Join row, and attention candidate derived from that discarded workflow state. A canceled Task never enters ordinary Approval-source validation and leaves no pending Approval, executable Current Node, interruption state, fan-out state, or workflow attention.
- Project each Task deterministically:
  - active backlog/terminal positions of non-canceled Tasks become serial Current Nodes;
  - a legacy canceled Task whose Workflow contains the canonical terminal Node key `done` becomes exactly one terminal `done` Current Node and retains no canceled state, timestamp, reason, event, or status;
  - when a canceled Task's Workflow has terminal Nodes but no canonical `done`, preserve its unique active terminal Placement when valid; otherwise choose one terminal Node deterministically without restoring candidate-rejection behavior;
  - a legacy canceled Task whose Workflow has no terminal Node is deleted after its Sessions become workflow-neutral; its worktree and other external artifacts remain;
  - each unfinished executable placement of a non-canceled Task becomes one Current Node, preserving branch key, materialized current values, Session binding, and interruption details;
  - unfinished started/waiting/runnable execution of non-canceled Tasks becomes interrupted because no Exact Execution Scope survives startup;
  - active fan-out expected branches and arrived Join values of non-canceled Tasks are reconstructed from the old active placement/transition snapshots;
  - pending Approvals of non-canceled Tasks are copied with their frozen snapshots and source Current Node reference.
- Build and validate every replacement value environment before dropping history:
  - current inputs come from the exact applied edge that created the unfinished placement by applying its frozen input bindings to that source Transition's commentary/output and current Task fields; only the first executable Node reached from Start may have no incoming values;
  - prior-node values first use the unfinished Run metadata's already-materialized map, then fill only missing required `.Nodes` entries from the latest qualifying applied Transition before that placement/run was created, using the legacy branch-first then Task-wide lookup order;
  - active Join branch materialized values are projected from old arrival/Transition snapshots, but the replacement fan-out rows retain only branch keys, arrival state, and those values; pending-Approval target values still come from the Approval's frozen edge metadata; history may only fill a required missing value using the same point-in-time and branch rules, never override a frozen value;
  - malformed JSON, conflicting frozen versus reconstructed values, a missing required input/prior-node/Join value, or an ordering tie that prevents choosing the unique latest qualifying source aborts migration with Task, Current Node, branch, and value-key context.
- Before discarding legacy Runs, use all Session links for Tasks that remain persisted—including links from canceled unfinished work—to backfill direct Session ownership and latest Session/Node/branch associations required by Context Source selection and Activity. Sessions retained from an omitted invalid canceled Task remain workflow-neutral. A Session associated with more than one retained Task, multiple unfinished executions for one Current Node reference, an unresolvable active branch, malformed required materialized values, or a non-canceled pending Approval without one source Current Node aborts migration with Task/Session diagnostic context.
- Validate the staged aggregate before schema replacement: reject more than one serial Current Node for a Task, mixed serial and fan-out state, a parallel Current Node without its Task's expected active branch, more than one Current Node for one branch, more than one pending Approval for one natural source reference, or a non-canceled Approval whose exact source is absent. Separately assert that every canceled Task has zero staged Approval/fan-out/executable/attention rows before its terminal projection or omission is applied.
- Preserve Questions in their existing Session/ask persistence. Do not reconstruct runtime waiters; migrated affected Current Nodes are interrupted and Resume performs the existing stale active-segment tool closure.
- After projection validation succeeds, there is no fallback reader, compatibility DTO, dual write, or retained old table. Generated SQLite access is regenerated from the new query source and effective schema.

### Guardrails and second-order updates

- Repository guards distinguish workflow Run/Placement names from legitimate generic runtime/model “run” terminology through positive structural inputs only: the effective schema, the current generated-query source at `server/metadata/queries.sql`, current production Go AST contract/write/control-flow types, current structured wire/new-write/Session-metadata shapes, and parsed Markdown code spans/examples for embedded prompt assets. Guards use no path exclusions, identifier or symbol allowlists, grandfather lists, suppressions, or lint exemptions.
- Historical migrations, migration fixtures, and required read-only legacy decoders are verified separately from architecture guards with structured fixtures: old data must decode and migrate successfully, obsolete decoded fields must be absent from current typed control-flow inputs, current serializers and new writes must omit them, and no current control flow may consume them. Preserve those historical readers; do not delete or weaken them to satisfy a guard.
- Workflow graph preview/deletion impact reads use only Current Nodes, active fan-out state, pending Approvals, and one Immutable Live Snapshot; the snapshot is preview evidence only, and Save/Delete revalidate controller state under the mutation permit. Historical Session associations do not block graph edits.
- Project/task deletion and Session workspace retarget checks use direct Session ownership instead of traversing Runs. Task deletion preserves Session artifacts while removing Task-owned current state and associations according to existing delete semantics.
- Project Home latest activity is the maximum of retained canonical Project, primary Workspace, Task, Comment, launch-visible Session, and Project-Workflow-link timestamps; Current Node mutations touch the owning Task timestamp. Removed Placement/Run/Transition history contributes nothing. Preserve the existing latest-activity descending and stable project tie-break ordering/pagination contract.
- Project deletion durable blockers come from non-terminal Current Nodes and pending Approvals; queued/retirement-held Automatic Intents, gates, and exact scopes are revalidated under the controller permit. Typed blocker behavior remains, but Run-count fields/copy are removed.
- The final prompt-asset guard parses embedded workflow-skill Markdown/code examples, validates Script `_kent` examples against the shared structured script-input contract, and scopes forbidden workflow identifiers to code spans/structured examples. Prose is covered by the final focused prompt review rather than brittle literal-copy assertions.
- Protocol version increments once for the atomic server/client cutover. No compatibility adapters, aliases, deprecated fields, or replacement execution identities are introduced.

## Planning

### Delivery shape

- Observable outcome: existing Tasks migrate to Task-owned Current Nodes, and sequential/parallel workflows can Start, ask Questions, Interrupt, restart, Resume, await Approval, Join, and finish without persisting or exposing workflow Run/Node Placement identities or history.
- Use vertical red-green-refactor slices. Each behavioral item below starts with one public-interface test, adds only enough production code to pass it, then refactors while green. Do not write all replacement tests before implementing the first working path. Workflow-editor and embedded-prompt copy use structural contract checks, focused review, and browser/manual QA instead of banned literal-copy assertions. Public documentation uses the `docs-writing` skill, docs package verification, and focused prose review rather than literal-copy tests.
- The Design already selected the lean variants: no Current Node completion selector, no per-branch manual Move, no executable manual Move from parallel state, and a two-variant Activity projection. Do not reopen or compensate for those removed behaviors.
- Development may temporarily contain unreferenced new structures while the branch is incomplete, but no production operation may dual-write, fall back to, or reconcile old and new state. Complete the hard switch before a repository-wide green checkpoint.
- The original 5,000–11,000 gross-line estimate is obsolete and must not drive
  implementation decisions. Scope is controlled by the coherent server/API
  completion boundary, absence of compatibility ownership, continuous
  demolition, and the net-negative handwritten production gate.
- The current agent completes the coupled Go server/API cutover. After that
  boundary is coherent, lower-coupling Go client/CLI, Desktop, documentation,
  protocol fixtures, guards, and QA work may split across fresh agents.

### Ordered implementation checklist

- [x] **Introduce the task-current-state domain contract.** RED: add behavior tests for a Current Node natural reference, serial versus branch-scoped equality, nullable Session binding, typed scheduling/interruption state, Approval identity, and mutation results without Run/Placement fields. GREEN: add the shared domain types used by store, Workflow Execution, runtime scope refs, read models, and contracts; do not add legacy aliases. **Done when:** the focused workflow domain tests pass and the new types have no independently generated Current Node/fan-out identity. **Completed 2026-07-23:** `./scripts/test.sh ./server/workflow` passes.

- [x] **Add normalized metadata structures with aggregate-level constraints.** RED: add direct schema rejection tests for a second serial Current Node at another Node, serial-plus-fan-out insertion in either order, a parallel Current Node without its Task's expected branch, a second Current Node in one branch, duplicate serial/parallel pending Approvals for one exact source, and deletion of a source while its Approval remains; assert active fan-out tables have no Workflow/Node/edge/provider/outgoing/configuration snapshot columns or JSON. GREEN: add the Task-keyed fan-out/branch/current-node/Approval structures, partial unique indexes, composite branch foreign keys, cross-table triggers, query source, and regenerated SQLite bindings while keeping absence as SQL `NULL` and fan-out persistence limited to branch keys/arrival/materialized values. **Done when:** schema tests reject every invalid aggregate shape, valid serial↔parallel aggregate transactions pass, and `./scripts/dump-metadata-schema.sh` exposes the exact keys/triggers with no Current Node/fan-out IDs or invocation snapshot storage. **Completed 2026-07-23:** `./scripts/test.sh ./server/metadata/...` and `./scripts/dump-metadata-schema.sh` pass.

- [x] **Move Task creation and Task Start onto Current Nodes.** RED: add store-facing tests proving task creation owns one Start/backlog Current Node and Start atomically replaces it with the first executable Current Node plus typed affected-node/session results. GREEN: implement the aggregate store methods and latest-definition validation without creating a Run/Placement or transition-history row. **Done when:** focused workflowstore start tests pass for agent and Script targets and inspect only the public store result. **Completed 2026-07-23:** `./scripts/test.sh ./server/workflowstore -run '^(TestTaskStartReplacesBacklogCurrentNodeWithFirstExecutableCurrentNode|TestStartTaskRejectsUnsafeWorkflowWithoutMutation|TestRepeatedStartAfterRoleToolDriftSkipsInitialExecutionPreflight)$'` and `./scripts/test.sh ./server/metadata/querygen ./server/metadata/sqlitegen` pass.

- [x] **Persist direct Session ownership and deterministic Node associations.** RED: add metadata/store tests for a newly created Task Session, duplicate serial association attempts with `NULL` branch, branch-association uniqueness, a reused Session visiting several Nodes, repeated same-key upsert, cross-Task association rejection, Task Session count, and two matching Sessions with equal association time selecting the greater Session ID. GREEN: bind Sessions to their Task, add the no-ID/no-Task-column Session/Node/optional-branch relation with separate serial/branch partial unique indexes, and order latest selection by association time then Session ID. **Done when:** one row survives each repeated natural-key association, cross-Task writes fail without mutation, equal-time selection is deterministic, and Session ownership/selection no longer depends on Runs. **Completed 2026-07-23:** `git diff --check`; `./scripts/test.sh ./server/workflowstore -run '^Test(AssociateTaskSession|LatestTaskSessionForNode)'`; `./scripts/test.sh ./server/metadata -run '^(TestOpenCreatesWorkflowSchemaAndForeignKeys|TestTaskSessionAssociationSchemaUsesDirectOwnerAndNaturalKeys)$'`; and `./scripts/test.sh ./server/metadata/querygen ./server/metadata/sqlitegen` pass. `./scripts/test.sh ./server/metadata/...` remains blocked at legacy `TestWorkflowAttentionCandidateRelationIsAuthoritative`, which still expects Run-based `StartTask` results and belongs to its later attention cutover.

- [x] **Replace Context Source history queries.** RED: add one vertical test at a time for `new_session`, `continue_session`, `compact_and_continue_session`, `node:<node_key>`, `previous_target`, and `previous_target_or_new`, including parallel Transition Branch scoping and frozen Approval resolution. GREEN: resolve from the source Current Node and direct retained Session associations, materialize the selected Session into target state, and update the embedded workflow skill's Context Source guidance to retained Session semantics. **Decision 2026-07-23:** defer frozen-Approval Context Source coverage to the subsequent Current Approval slice because no Current-Node Approval store/API exists yet, and defer branch-scoped transition-application coverage to the later fan-out slice because no branch progression exists yet. Preserve these ordered dependencies rather than pulling Approval or fan-out persistence forward. **Done when:** all context-preservation tests pass with no completed Run/Transition query in the exercised path and focused prompt review finds no completed-work execution identity in the Context Source guidance. **Completed 2026-07-23:** `git diff --check` and `./scripts/test.sh ./server/workflowstore -run '^Test(CompleteCurrentNode|AssociateTaskSession|LatestTaskSessionForNode)'` pass. Focused review of `prompts/skills/kent-workflows/SKILL.md` confirms Context Source guidance selects retained Sessions, and the exercised Current-Node completion path contains no completed-Run/Transition query.

- [x] **Implement sequential Current Node completion.** RED: add an agent completion test proving one atomic transaction validates the current source, consumes the live completion result, removes the source, creates the target, returns handoff/automatic-intent facts, and retains no completed execution/movement row. GREEN: port completion validation and transition application behind the aggregate store interface. **Done when:** sequential completion and stale/no-longer-current rejection tests pass and a post-completion store read contains only the target Current Node. **Completed 2026-07-23:** `./scripts/test.sh ./server/workflowstore -run '^(TestTaskStartReplacesBacklogCurrentNodeWithFirstExecutableCurrentNode|TestStartTaskRejectsUnsafeWorkflowWithoutMutation|TestRepeatedStartAfterRoleToolDriftSkipsInitialExecutionPreflight|TestCompleteCurrentNodeAtomicallyReplacesAgentAndReturnsSuccessorIntent)$'` passes.

- [x] **Materialize the current value environment.** RED: add a chained-workflow test proving target prompts and downstream `.Inputs`/`.Nodes` propagation use values already owned by the Current Node. GREEN: replace historical transition lookups with derived-wiring-driven materialization that retains only values needed by the current/downstream graph. **Done when:** chained store tests pass without reading completed transition records. **Decision 2026-07-23:** defer the Workflow-edit unavailable-new-input validation to the later admission/Resume slice, where Current Node execution preparation exists. **Completed 2026-07-23:** `./scripts/test.sh ./server/workflow`, `./scripts/test.sh ./server/metadata/querygen ./server/metadata/sqlitegen`, and `./scripts/test.sh ./server/workflowstore -run '^(TestTaskStartReplacesBacklogCurrentNodeWithFirstExecutableCurrentNode|TestStartTaskRejectsUnsafeWorkflowWithoutMutation|TestRepeatedStartAfterRoleToolDriftSkipsInitialExecutionPreflight|TestCompleteCurrentNodeAtomicallyReplacesAgentAndReturnsSuccessorIntent|TestCompleteCurrentNodeMaterializesChainedInputsAndPriorNodeValues)$'` pass.

- [x] **Persist current pending Approval state.** RED: add tests for frozen Workflow Version/branch/context snapshots, restart visibility, multiple parallel pending Approvals, source-Current-Node retention, `waiting_approval` projection derived only from the Approval, application, and Task/Workflow/Project deletion; structurally assert that Approval contracts/actions expose Approve but no standalone Reject operation. GREEN: introduce Approval-owned persistence and application transactions using UUID v4 Approval identity; remove resolved-state retention, do not port legacy rejected-Transition state, and do not add a duplicate waiting scheduling state or Reject path. **Ordered dependencies:** Task-wide Move supersession remains in **Port Task-wide Move semantics**; dismissed execution-target selection and controller-wide eligibility remain in **Enforce one executable-eligibility rule** and **Implement admission and restart-marker flow**. Branch-scoped Approval application was completed with the fan-out slice. **Done when:** Approval persistence/store tests pass, the store reports a pending source ineligible, no resolved Approval row remains, and no standalone Reject contract/action exists. **Completed 2026-07-23:** `git diff --check`; `./scripts/test.sh ./server/workflowstore -run '^Test(CompleteCurrentNode|ListPendingApprovalsKeepsParallelSourcesIndependent|DeleteTaskRemovesPendingApprovalBeforeCurrentNodeCascade|DeleteWorkflowRemovesPendingApprovalsBeforeCurrentNodeCascade|DeleteProjectRemovesPendingApprovalsBeforeCurrentNodeCascade)$'`; `./scripts/test.sh ./server/workflow -run '^TestCurrentNode'`; `./scripts/test.sh ./server/metadata/querygen ./server/metadata/sqlitegen`; and `./scripts/build.sh --output ./bin/kent` pass.

- [x] **Implement fan-out creation and branch progression.** RED: add a fan-out test proving one completion creates only the frozen expected Transition Branch Key rows and branch-scoped Current Nodes, including duplicate target Node IDs distinguished by branch key; assert no Join/Workflow/provider/outgoing configuration is persisted. GREEN: create the Task singleton active-mode marker and branch rows, carry branch identity/materialized values across pre-Join transitions, and leave ordinary invocation facts in the latest Workflow definition. **Done when:** fan-out tests pass without a batch ID, independently addressed branch entity, or renamed Run snapshot. **Completed 2026-07-23:** `git diff --check`; `./scripts/test.sh ./server/workflowstore -run '^Test(CompleteCurrentNode|ApplyPendingApproval|ListPendingApprovalsKeepsParallelSourcesIndependent)$'`; `./scripts/test.sh ./server/workflow -run '^TestCurrentNode'`; and `./scripts/test.sh ./server/metadata/querygen ./server/metadata/sqlitegen` pass. Branch-scoped and fan-out Approval application are covered here under the prior ordered-slice decision.

- [x] **Implement latest-definition Join arrival and aggregation.** RED: add tests for partial arrival, independent sibling interruption, deterministic provider selection, same-key collision failure, all-branches arrival, and fan-out-state deletion; after fan-out begins, apply compatible provider/outgoing-Transition edits and prove they take effect, while missing/ambiguous Join topology or newly required unavailable values returns the approved typed validation outcome. GREEN: record only branch materialized values, derive the unique common Join/providers/outgoing Transition from the latest Workflow definition, apply it atomically, then remove all active fan-out state. **Decision 2026-07-23:** a Join outgoing Transition requiring Approval is invalid and returns the existing typed unsupported-approval validation outcome; do not synthesize a Join Current Node or bypass Approval. **Done when:** Join tests prove no Run snapshot or historical placements/transitions are consulted, compatible edits are live, incompatible edits fail typed without corrupting current state, and no fan-out row survives completion. **Completed 2026-07-23:** `git diff --check`; focused Current-Join tests; `./scripts/test.sh ./server/workflow`; `./scripts/test.sh ./server/metadata/querygen ./server/metadata/sqlitegen`; and `./scripts/build.sh --output ./bin/kent` pass. The broad legacy workflowstore suite remains intentionally red because it asserts Run/Placement behavior removed by preceding cutover slices.

- [ ] **Port Task-wide Move semantics.** RED: add tests proving a quiescent parallel Task moves all Current Nodes only to Start/backlog or terminal state; a serial quiescent Task moves forward only through one concrete latest-definition Workflow Edge; missing-edge, history-derived/backward, and parallel executable movement are rejected; and pending Approvals belonging to replaced Current Nodes are cleared without selecting one parallel branch. GREEN: implement Task aggregate replacement against Current Nodes through the shared mutation permit. **Done when:** focused manual-Move tests pass and no API accepts a per-Current-Node movement selector.

- [x] **Build the sequential/interrupted migration tracer.** RED: seed pre-cutover sequential backlog, terminal, active agent, active script, interrupted agent/script, waiting-Question, pending-Approval source, and retained completed-Session fixtures plus canceled variants, serial waiting-Approval, and parallel fan-out with one or several pending Approvals. Assert ordinary Current Node/restart/Session/Question projection and discarded completed history. **Decision 2026-07-23:** canceled is literally Done; do the minimal migration work. Retain every canceled Task as openable Done state, harvest retained Session links, discard all pending Approval/edge, executable Placement/Run, fan-out/Join, and derived attention state, then project a terminal Current Node. Prefer canonical terminal key `done`; without it choose a deterministic terminal from the Task Workflow. Do not reject canceled Tasks for malformed legacy terminal candidates. A canceled Task whose Workflow has no terminal Node is impossible/corrupt legacy state; delete that Task while preserving Sessions/artifacts workflow-neutral. GREEN: implement canceled-Task normalization as a precedence stage, then deterministic ordinary staging/projection. **Done when:** focused metadata migration tests pass for canceled serial and parallel/pending-Approval states with no Approval or executable/fan-out/attention row surviving and canceled Tasks remain openable as Done. **Completed 2026-07-23:** `git diff --check`; focused `server/metadata` migration tests; `./scripts/test.sh ./server/metadata/querygen ./server/metadata/sqlitegen`; and `./scripts/build.sh --output ./bin/kent` pass. The full `server/metadata` package remains red at legacy `TestWorkflowAttentionCandidateRelationIsAuthoritative`, which expects a Run-based `CompleteRun` Approval path removed by earlier Current-Node slices and belongs to the later attention cutover.

- [ ] **Remove Task Cancel server and public-API behavior.** RED: replace server tests that preserve Task Cancel with structural contract tests proving route/service/store interfaces expose no cancel operation and public API/read-model contracts contain no `WorkflowTaskCancelRequest`, `WorkflowTaskStatusKindCanceled`, `WorkflowTaskNativeStateCanceled`, `WorkflowProjectEventActionCanceled`, `CanCancel`, `canceled_at_unix_ms`, `cancellation_reason`, or equivalent canceled Task field/action. GREEN: remove the RPC route/request, service/store operation and `ErrTaskCanceled`, canceled Task status/event/public DTO fields, live write adapters, and server tests that preserve Task Cancel behavior. Preserve ordinary `context.Canceled`, Exact Scope stopping, Question cancellation, and script/process cancellation. The forward hard-cutover migration remains the sole consumer and owner of physical legacy cancellation columns. **Done when:** focused API, service, store, view, and migration-input tests pass with Task Cancel absent from server/public-API structure, Interrupt and quiescent Delete remain covered, and pre-cutover migration fixtures still read the old cancellation fields. CLI, Go remote-client, and Desktop removal remain in their lower-coupling client slices.

- [x] **Cut legacy Session workflow metadata and persisted inspection.** RED: seed a pre-cutover Session row/artifact with `workflow_session.run_id` alongside direct Task/current-Node/association ownership and prove migration strips the object while persisted inspection ignores the stale Run ID; also prove a retained non-current Task Session opens as an ordinary Session and central metadata sync/mutation/retarget writes never restore the object. GREEN: remove durable `Meta.WorkflowSession` and current serializers/readers, strip `workflow_session` during migration, retain only read-only legacy decoding where required, and resolve persisted inspection by Session ID and direct current state. **Done when:** migrated SQLite JSON and rewritten Session metadata omit `workflow_session`, current write paths share the workflow-neutral shape, and runtime/inspection control flow does not read the legacy Run ID. **Completed at `791217047`:** the dependent live Start response/protocol path remains owned by the runner and server lifecycle API slices; it is not part of this metadata/inspection boundary.

- [ ] **Replace workflow-Session provenance, policy, and status consumers.** RED: add one integration matrix for a live Task Session, a retained non-current Task Session, and an ordinary Session across `launch.ResolveSessionCaller`, workflow-only runtime-control policy such as auto-compaction disable, live runtime status, dormant session projection, and transcript/session DTO validation. GREEN: use direct Session owning-Task for durable workflow origin and retained-session policy, Exact Scope plus Session-bound Current Node for activity, and replace Run-bearing status contracts with Task/Workflow/optional-Current-Node/`Active` facts. **Done when:** live Task Sessions remain restricted and active, retained Task Sessions retain workflow origin without claiming liveness, ordinary Sessions remain unrestricted, and no runtime/session status DTO contains a Run field.

- [ ] **Migrate materialized value environments before deleting history.** RED: seed an interrupted chained executable Node whose incoming edge derives `.Inputs` from Task/commentary/output, whose prompt requires persisted and reconstructable `.Nodes` values, and whose later completion must propagate those values downstream; add malformed, conflicting, missing, branch-ambiguous, Join-arrival, and frozen-Approval cases. GREEN: stage current inputs, prior-node maps, Join values, and Approval target maps using the architecture's source precedence before any legacy row is dropped. **Done when:** after migration with legacy tables/queries unavailable, Resume renders the chained prompt and its completion creates the downstream values exactly, while every invalid source case aborts with Task/Current-Node/branch/value-key context.

- [ ] **Complete parallel/Approval/Join migration and execute the hard schema cutover.** RED: seed active fan-out with partial Join arrival, branch Session associations, reconstructed branch values, and pending Approval snapshots; add validation fixtures for multiple projected serial Nodes, mixed serial/fan-out state, orphan/unexpected branches, two projected Nodes for one branch, duplicate pending Approvals for one natural source, cross-Task Session ownership, unresolved branch identity, missing Approval source, and a canceled pending-Approval/fan-out row deliberately reaching final staging. Assert the canceled row is rejected because normalization must have consumed it, migrated fan-out rows contain only Task/branch/arrival/materialized-value facts, and the migrated Join completes after compatible provider/outgoing configuration changes in the latest definition. GREEN: validate staged aggregate shape, project only branch keys/values rather than old Join/run snapshots, then—only after canceled normalization and all other staging validation succeed—drop `canceled_at_unix_ms`, `cancellation_reason`, canceled query parameters/read models/generated bindings together with old Run/Placement/applied-rejected-transition tables/views/indexes; delete obsolete current-query assumptions and regenerate access from the effective schema. **Done when:** every invalid projection aborts before replacement with Task/source context, migrated Join completion uses the latest definition, no canceled persistence/query/generated field or fan-out snapshot field exists after cutover, old relation/query names are absent, and `server/metadata` plus `server/workflowstore` tests are green.

- [ ] **Adapt `sessionruntime.Authority` to Current Node scope refs.** RED: add Authority tests for agent/script scopes keyed by the natural Current Node reference, duplicate live-scope rejection, exact Scope ID stale-predecessor protection, Task snapshots, Session lookup, and retirement callbacks. GREEN: remove workflow Run ID/generation from scope indexing while retaining process-local execution/resource generations. **Done when:** `./scripts/test.sh ./server/sessionruntime` passes and no workflow scope API exposes a persisted execution identity.

- [ ] **Replace the polling scheduler with Workflow Execution core state.** RED: add controller tests for the mutation permit, volatile typed Automatic Intent order, automatic capacity, explicit-capacity bypass, task affinity, runtime gates, immutable snapshot cloning, empty startup state, and joined shutdown. GREEN: deepen the existing scheduler/orchestration code into the single controller and remove runnable scans, claims, reconciliation, timers, and durable queue facts. **Done when:** focused Workflow Execution tests pass without polling or querying for runnable work.

- [ ] **Enforce one executable-eligibility rule.** RED: add a public-interface matrix proving a pending-Approval source remains current but is excluded from Automatic Intents, explicit admission, Resume, startup recovery into ready work, and Task/Session forced idle completion; add stale, ambiguous, non-executable-state, gated, and live-scope controls that preserve their typed errors. GREEN: centralize the architecture's eligibility predicate in the Task/current-state authority and make every selector/admission path consume it rather than reimplementing approval checks. **Done when:** Approval, controller admission, Resume, recovery, and forced-completion tests all pass against the same eligibility behavior and no `waiting_approval` scheduling enum/flag exists.

- [ ] **Implement admission and restart-marker flow.** RED: test gate registration before slow preparation, durable admitted/executing marker, successful gate-to-scope replacement, preparation/start compensation to interrupted, startup recovery of orphan markers, pending-Approval admission rejection, no automatic retry, and a Current Node whose latest Workflow definition introduces a required unavailable materialized input refusing preparation with the typed validation error. GREEN: connect Workflow Execution to the store, runner, and Authority using the architecture's release/reacquire permit sequence and shared eligibility result. **Done when:** admission/restart tests pass and a crash-recovery fixture leaves affected eligible Current Nodes resumable but not queued/running while pending-Approval sources remain pending and unexecuted.

- [ ] **Implement scope retirement, completion release, and Resume.** RED: add tests proving successor intents wait for source retirement, continuation never overlaps a reused Session, Resume waits for predecessor retirement, Resume rejects a pending-Approval source through shared eligibility, and late completion cannot mutate. GREEN: route completion retirement and Resume through typed Workflow Execution intents and exact-scope validation. **Done when:** controller/Authority integration tests deterministically pass under concurrent completion/retirement/Resume ordering.

- [ ] **Make Task Interrupt drain every controller work state.** RED: add deterministic cases for a queued-only Automatic Intent, successor intent held behind source retirement, runtime gate, live exact scope, and mixed parallel states; race Interrupt against predecessor retirement and prove no successor starts after Interrupt returns. Include Session-targeted interruption proving it selects only one agent gate/scope and leaves unrelated Task intents/scripts untouched. GREEN: under the mutation permit remove queued/held Task intents, cancel gates, mark scopes stopping, record interruption for intent-only Current Nodes, join gates/scopes outside the permit, then revalidate/record remaining Current Nodes. **Done when:** Task Interrupt returns with no matching intent/gate/scope in the immutable snapshot, all still-current affected Nodes are interrupted as specified, and the retirement-held race is deterministic.

- [ ] **Prove or reject the need for controller mutation exclusions.** Use the
  shared mutation permit for edit and deletion serialization. Add no exclusion
  framework unless a deterministic external-cleanup race proves the permit
  insufficient. **Done when:** deterministic deletion/edit races pass with the
  permit alone, or the demonstrated blocker and an approved minimal design are
  recorded before implementation.

- [ ] **Cut production composition and lifecycle to Workflow Execution.** RED: add a deterministic `server/core` integration test that constructs the real store/controller/runner/Authority/service graph around an admitted-marker fixture, proves recovery finishes before the first service request, proves lifecycle service calls delegate to the controller, observes Authority retirement reaching that same controller, and records joined close ordering. GREEN: replace scheduler construction/start/notification/cleanup wiring with the single Workflow Execution instance and its retirement callback, keeping services unavailable until startup recovery succeeds. **Done when:** core/startup tests pass, no production composition references the old scheduler, and close returns only after controller-owned work is joined before Authority and metadata close.

- [ ] **Serialize Task deletion through Workflow Execution.** RED: race queued/retirement-held Automatic Intents and admission against Task deletion and prove the shared mutation permit plus controller Quiescence check prevents work from crossing the durable delete. GREEN: reject non-quiescent Delete, retain the permit through the safety-critical mutation, delete Task-owned state while preserving Session artifacts, then finalize attention/events. **Done when:** no intent/gate/scope can appear between the Quiescence check and deletion, automatic-intent-only Tasks are covered, and existing worktree blockers remain typed. If external cleanup cannot safely remain under the permit, return to the preceding proof item before adding machinery.

- [ ] **Port agent execution preparation in the runner.** RED: add runner tests for fresh Session, resumed Session, continuation, compact-and-continue contract generation, fan-out cloning/isolation, latest role/completion contract, task comments, worktree restoration, and cleanup of only newly created disposable Sessions. GREEN: replace Run-loading/mutation interfaces with prepared Current Node context and direct Session binding while reusing `launch.Planner` and runtime wiring. **Done when:** focused `server/workflowrunner` agent tests pass with no Run snapshot/effective-mode persistence.

- [ ] **Port Script Node execution.** RED: add tests for `_kent` containing only Task/Node/optional branch identity, absence of Run/Placement environment variables, current input stdin, latest script path/completion contract on Resume, Task-wide interruption, structured failure details, and no retained attempt. GREEN: adapt the script runner to Exact Execution Scope and Current Node completion and update the embedded workflow skill's Script section to the same structured `_kent` contract. **Done when:** script tests pass on supported platforms, the embedded example validates against the shared input shape, and no script contract contains removed IDs.

- [ ] **Remove workflow Run identity from runtime and transcript writes.** RED: add runtime tests for scope-bound completion/observation/protocol cap, stable live advertised contract, compaction across workflow continuation, terminalization, and parsing old v1 events with ignored obsolete fields. GREEN: rename live workflow config/control types, make violation counts scope-local, remove workflow Run fields from new Session/history events and compaction state, and keep legacy bytes untouched. **Done when:** runtime/workflowruntime/session tests pass and new serialized fixtures contain no workflow Run field.

- [ ] **Port Question issuance, await, clear, and skipped batches to Exact Scopes.** RED: add vertical ordinary-Question and runtime-approval-Question tests for issue/attention registration, await, answer clear, cancellation, skipped batch members, scope retirement, and a stale predecessor attempting to issue or clear the successor's Question. GREEN: replace `SetRunWaitingAsk`/`ClearRunWaitingAsk` and Run-bearing question targets with Exact Scope plus prompt-registry lifecycle and Task/Session/Current-Node attention facts. **Done when:** focused workflowattention/runner/Authority tests pass, no durable waiting-Question column/table or Run target remains, and restart relies only on current-Node interruption plus existing ask persistence.

- [ ] **Cut lifecycle server APIs and transport handlers to Current Node/Session contracts.** RED: add shared-contract, service, and server-transport tests for Start/Resume/Approve/Move/Complete affected Current Nodes/Sessions, Task-or-Session forced completion, pending-Approval ineligibility, typed ambiguity/stale errors, Approval focus, and interruption results. GREEN: update server API DTOs/validation, application-service delegation to Workflow Execution, and server route/transport handlers in one breaking shape. **Done when:** `shared/serverapi`, `server/workflowsvc`, and server transport contract tests pass with no Run/Placement/count fields. This is part of the current agent's mandatory server/API checkpoint; the Go remote client is a later lower-coupling slice.

- [ ] **Cut server Question answering from Run identity.** RED: add one vertical shared-contract/service/server-transport test path proving `WorkflowTaskQuestionAnswerRequest` and the memo payload contain Task ID + Question ID + answer only, the live Question resolves through prompt registry/Exact Scope plus direct Session/Current-Node Task ownership, replay is idempotent, payload mismatch is rejected, stale/missing and multiple matches remain typed, ownership mismatch submits nothing, and project events contain no Run ID. GREEN: replace `ResolveTaskWaitingAsk`/Run-based service logic and update server event publication and transport handling without adding another selector. **Done when:** `shared/serverapi`, `server/workflowsvc`, and server transport Question tests pass and the server wire request/memo/event path has no Run field.

- [ ] **Cut the Go remote client to the locked server contracts.** RED: update `shared/client` transport tests for lifecycle Current Node/Session results and Task-ID-plus-Question-ID answering, including typed stale/ambiguity failures and structural absence of Run/Placement/count fields or a Task Cancel method. GREEN: update remote-client methods and DTO consumption without compatibility parsing or fallback fields. **Done when:** `./scripts/test.sh ./shared/client/...` passes against the locked server API and the Go client exposes no removed workflow identity or Cancel lifecycle.

- [ ] **Update CLI workflow controls and output.** RED: add structural CLI tests proving `kent task complete` accepts agent `KENT_SESSION_ID`, human `--session`, or an unambiguous `--task`; the command registry/flag parser does not register `--run`, a Current Node selector, or `task cancel`; list sort fields and typed JSON contracts contain no `run_count` or workflow Run/Placement/Cancel fields. Do not assert literal help or rendered output wording. GREEN: port lifecycle response formatting, selectors, structured output, and sort registration to the new contract while retaining the `kent run` command's Session meaning. **Done when:** `./scripts/test.sh ./cli/kent` passes against command/flag/sort/JSON structure, and focused CLI review/manual QA verifies the human-facing help and output use approved terminology and offers Interrupt/Delete without Task Cancel.

- [ ] **Rebuild Task status, board, and paginated list projections.** RED: add projection/integration tests for serial/parallel Current Nodes, done/backlog/interrupted states, live running/queued/Question precedence, server-side status filtering/sorting/cursors with a supplied Immutable Live Snapshot, column keys, and `can_delete`. GREEN: replace Run/Placement status views with Current Node facts and pass one structured live snapshot into generated paginated queries. **Done when:** workflowview board/list/detail tests pass with cursor correctness and no persisted liveness source.

- [ ] **Rebuild attention and notification projections.** RED: add tests for pending Approval, live Question, interrupted agent Session, interrupted script/current Node, validation blocker, global pagination, task feed, resolution, and notification focus without Run IDs. GREEN: rebuild durable candidates and merge the bounded live snapshot/prompt facts through the existing broker/finalizer seam. **Done when:** workflowattention/workflowview notification tests pass and every item targets Question, Approval, Session, or Current Node.

- [ ] **Rebuild Workflow graph edit impact and serialize Save.** RED: add preview/save tests proving removed Node/edge and contract-change impact comes from Current Nodes, active fan-out branch keys/values, and pending Approvals; prove a matching historical Session association alone does not block an edit. For active fan-out, reject or return the approved typed validation result for removing the unique Join, changing topology incompatibly, or adding unavailable required values, while allowing compatible Join-provider and outgoing-Transition edits that later Join application observes. Race Automatic Intent-only work and admission after preview but before Save, and require commit-time revalidation under the controller permit. GREEN: replace Run/Placement/history/snapshot reference queries and policy fields, route Save through Workflow Execution, and preserve confirmation/impact-change behavior without treating preview or persisted fan-out configuration as authority. **Done when:** graph-save tests pass for serial, parallel-compatible/incompatible edits, Approval, intent-only, gate/scope, concurrent-admission, and history-only fixtures with no Run/Placement/snapshot count or query.

- [ ] **Rebuild Workflow deletion impact and serialize deletion.** RED: add preview/delete tests for ready/interrupted Current Nodes, active fan-out, pending Approval, queued/retirement-held intents, gates/live scopes, quiescent retained Sessions, confirmation drift, attention resolution, and concurrent admission between preview and Delete. GREEN: derive preview impact from current state/snapshot and revalidate controller state under the shared mutation permit before removing Task-owned current/fan-out/Approval state while preserving Sessions. **Done when:** workflow deletion tests cover automatic-intent-only races, historical Session associations do not become liveness blockers, the permit prevents concurrent admission across deletion, and DTOs contain no Run counts.

- [ ] **Rebuild Project Home activity from retained canonical facts.** RED: add metadata/project-view ordering tests showing Project, primary Workspace, Task, Comment, launch-visible Session, and Project-Workflow-link updates advance canonical activity; prove legacy Placement/Run/Transition-only timestamps do not; cover equal timestamps, stable project tie-break ordering, filters, and page continuation. GREEN: replace historical workflow subqueries and ensure Current Node mutations touch Task update time. **Done when:** Project Home tests preserve the existing order/pagination contract using only retained facts.

- [ ] **Serialize Project deletion and replace old blocker counts.** RED: add metadata/project-service tests for non-terminal Current Nodes, pending Approvals, queued/retirement-held intents, gates/scopes, ordinary active Sessions, automatic-intent-only Tasks, typed blocker responses, and admission racing artifact cleanup/deletion. GREEN: derive durable blockers from Current Nodes/Approvals, combine the shared mutation permit with existing Session-start maintenance blocking, revalidate controller and Session state, and delete durably. **Done when:** Project deletion cannot race new workflow work or Session starts, failure releases its existing maintenance block, Project rows/artifacts follow existing semantics, and no blocker/output uses Run counts. If external cleanup requires releasing the permit, prove that blocker before adding a scoped mechanism.

- [ ] **Move Session workspace-retarget ownership checks to direct Task ownership.** RED: add metadata/session-service tests proving same-project retarget remains allowed, cross-project retarget of a Task-owned Session is blocked with owning Task IDs even after its Node is no longer current, and a non-workflow Session is unaffected. GREEN: replace Run traversal and legacy workflow-session metadata checks with the authoritative Session owning-Task relation. **Done when:** focused retarget tests pass after legacy Run rows/fields are unavailable and ownership has one source of truth.

- [ ] **Reduce Activity and Task detail to approved Session behavior.** RED: add Activity pagination tests for mixed Comments and retained Session creation, edit/delete behavior, equal timestamps, typed variants, and no movement/completion rows; add Task-detail tests for total retained Session count, live Session refs, current scripts, and action availability. GREEN: replace the old activity projection/contract and task-detail Run data with direct Session ownership and Current Node/live-snapshot facts. **Done when:** server Activity/detail tests pass, server emits no UI summary strings, and KENT-212 remains the only scope for a fuller worklog.

- [ ] **Cut remaining Desktop API contracts to the new protocol.** RED: update schema/client tests first for Current Node refs, affected Session results, typed Activity variants, attention focus, retained Session count, live Session actions, current scripts, and absence of all removed Run/Placement/count/Task-Cancel fields and actions outside the already-cut Question path. GREEN: update TypeScript models, Zod schemas, API composition, query invalidation, and fixtures without compatibility parsing. **Done when:** Desktop API/schema tests pass, only Interrupt/Delete lifecycle choices remain, and old payloads are rejected rather than normalized.

- [ ] **Update Workflow editor Context Sources to Session language.** Update the existing localized help/options for previous-target and previous-target-or-new selection while preserving typed values, availability rules, and editor/read-only inspector behavior. Reuse existing structural editor tests; do not add tests that assert literal copy. **Done when:** Desktop Workflow editor tests pass and isolated browser QA verifies editable and read-only Context Source controls consistently describe retained Sessions, with no workflow Run wording on that surface.

- [ ] **Update Desktop behavior without adding a persistence-model UI.** RED: add task-detail/board tests for total Session count, typed live-Session Open/Interrupt action availability, conditional Task-wide Interrupt, current Script open, comments plus the typed `session_started` Activity variant, use of the existing localization key/boundary for that variant, and structural absence of workflow-history variants/actions from component inputs. Do not assert rendered copy or search rendered text for removed terminology. GREEN: adapt existing action/property/activity components and remove obsolete move-feedback/history wiring. **Done when:** Desktop feature/integration/accessibility tests pass in light/dark-independent structural behavior assertions, no new persistence-model detail section exists, and isolated browser QA verifies the approved Activity and control wording.

- [ ] **Bump the Go/Desktop protocol once.** Update `shared/protocol/version.json`
  once after the Go server, Go client, and Desktop contract shapes are locked.
  Update only active Go/Desktop fixtures and handshake tests.

- [ ] **Cut the public workflow guide to Task-owned current state.** Invoke the `docs-writing` skill before editing the owning public guide at `docs/src/content/docs/workflows.md`. Describe a Task as owning one Current Node, or several branch-scoped Current Nodes during fan-out, without a Run or Placement entity/history. Update the Script example from the shared structured input contract so `_kent` contains Task, Node, and optional Transition Branch identity only. Rewrite interruption and Resume around current agent Sessions/current Script work; use Interrupt for resumable stopping and quiescent Delete for removal. Rewrite immediate-source, selected-node, previous-target, and previous-target-or-new Context Sources around retained Sessions. Update Task-management semantics for retained Session count/discovery and Activity's Comments-plus-Session-creation scope without adding a UI tour. Update CLI completion guidance to agent `KENT_SESSION_ID` and human `--session` or unambiguous `--task`, with no workflow Run selector. Review the whole guide for removed Run/Placement counts, fields, selectors, Task Cancel, and execution-history concepts while preserving legitimate generic `run` usage such as running a command or agent. Extend the existing built-doc smoke page set to cover the generated workflow guide through structural page checks only. Keep prose timeless, concise, and user-facing; do not add tests that assert literal documentation copy. **Done when:** focused prose review confirms the guide consistently teaches the post-cutover product and shared contracts, `pnpm --dir docs test`, `pnpm --dir docs build`, and `pnpm --dir docs smoke:built` pass, and the built workflow guide passes the existing structural smoke contract without copy assertions.

- [ ] **Delete obsolete mechanisms and add exact architecture guards.** Remove old scheduler/reconciliation code, Run/Placement/history store types, durable Session workflow metadata, fan-out Join/configuration snapshots, current query adapters, DTOs, selectors, activity projectors, tests that only preserve the deleted implementation shape, and unused generated artifacts. Add positively scoped Go AST guards over current production contract/write/control-flow types, exact guards over the effective schema and current `server/metadata/queries.sql`, structured guards over current wire/new-write/Session-metadata shapes, and parsed-Markdown checks over embedded workflow-skill code spans/examples. Add no path exclusions, identifier/symbol allowlists, grandfather lists, suppressions, or lint exemptions. Separately, add structured legacy fixtures proving historical migrations and required read-only decoders accept old data, remove obsolete fields before constructing current typed control-flow inputs, never emit them from current serializers/new writes, and never let them affect a current control-flow decision. **Done when:** focused guards fail against deliberately reintroduced current production/schema/fan-out-snapshot/wire/Session-write/prompt-example structures and pass after removal; legacy fixture tests still decode/migrate old data but expose only current typed outputs; scoped review includes `prompts/skills/kent-workflows/SKILL.md`.

- [ ] **Run focused end-to-end workflow tests.** Exercise real production composition, store, Workflow Execution, runner, runtime, services, and read models for sequential agent, Script, Question issue/answer/clear, Approval eligibility, queued/held-intent Interrupt, restart-before-service recovery, Resume with latest contract, forced idle completion, serialized Task/Workflow/Project deletion, and terminal completion. **Done when:** focused server packages pass through `./scripts/test.sh` with no fake-only production hooks or production APIs widened for tests.

- [ ] **Run focused parallel and migration tests.** Exercise fan-out admission, independent per-Session interruption, Task-wide script interruption, partial arrival, restart, Resume, all-branches Join, branch-scoped context selection, migration preservation, and discarded history. **Done when:** metadata/workflow packages pass repeatedly without timing flakes and migrated fixtures expose no Run/Placement/history data.

- [ ] **Perform repository-wide verification and manual QA.** Run `./scripts/test.sh`, `./scripts/ci-check.sh`, and `./scripts/build.sh --output ./bin/kent`. Then use the isolated `qa-harness` with local models to QA sequential and parallel Tasks through Start, Question, Interrupt, server restart, Resume, Approval, Join, and Done in browser/Desktop plus CLI control checks, and inspect Workflow-editor Context Source controls in editable/read-only states for Session terminology; do not spend real-provider credits without separate approval. **Done when:** all commands pass, the built binary is refreshed, QA confirms no Run/Placement/history concepts or stale controls appear anywhere including the editor, and failures/caveats are recorded rather than bypassed.

## Verification state

- Current, exact verification evidence belongs in
  `.kent/evidence/KENT-334-cutover-checklist.md`.
- Earlier slice checks are historical and do not imply that the changing
  worktree is repository-wide green.
- Final verification remains the focused server/client suites, full
  `./scripts/test.sh`, `./scripts/ci-check.sh`, the production build, and manual
  sequential/parallel QA. Rust is outside this verification boundary.

## Review follow-up — 2026-07-23

- **Resolved 2026-07-23:** the sequential/interrupted migration tracer now projects pending Approval headers and branches from legacy transition/edge facts after mutable graph deletion, validates every staged Approval header/branch count, and preserves target edge keys when a serial Approval fans out. Its migration test applies that migrated fan-out through `workflowstore.ApplyPendingApproval`. Legacy Script-run Session links now remain workflow-neutral with no Task owner or Script-Node association.
- The later **Migrate materialized value environments before deleting history** slice owns current-input/prior-node reconstruction and its malformed/missing-value diagnostics. Do not pull it forward solely to make this tracer look complete.
- User decision: canceled Tasks remain Done. Preserve canonical-`done`/deterministic-terminal normalization and do not restore legacy candidate-rejection behavior; delete only a canceled Task whose Workflow has no terminal Node.
- **Superseded 2026-07-24:** Task Cancel removal is in scope for KENT-334
  and ships with this breaking cutover. Do not add a Current-Node Cancel
  compatibility path.
- **Critical path 2026-07-24:** before splitting lower-coupling client work,
  complete the server/API boundary: Current Node controller authority,
  lifecycle operations, Questions, terminal/board/attention projections, and
  the Comments-plus-retained-Session Activity projection must be end-to-end
  without a Run/Placement fallback. Delete scheduler/reconciliation and
  Run/Placement control code continuously as replacements land.
- **Delivery guard 2026-07-24:** maintain a demolition ledger. Before final
  verification, handwritten non-test production code must be net-negative
  against integrated base `3c7d45a62`; tests, generated SQL, migrations, and
  docs are excluded.

## Server/API checkpoint progress — 2026-07-24

### Review round 3 human decision — 2026-07-25

- The inherited Frozen Rust change in `AGENTS.md` is explicitly
  human-approved. Preserve that existing edit unchanged; this does not
  authorize any further edit to `AGENTS.md`.

### Review round 3 decision — 2026-07-25

- The inherited Frozen Rust edit in `AGENTS.md` is explicitly approved by the
  human. Preserve that existing diff unchanged; this authorization does not
  authorize further edits to `AGENTS.md`.

### Review round 4 follow-up — 2026-07-26

- [x] Remove current-path Workflow Run and run-start-snapshot messages from
  runtime errors and advertised completion-contract validation.
- [x] Replace the deleted AST identifier blacklist/path exclusion with positive
  typed Current-Node configuration and Authority-to-runner wiring contracts;
  keep legacy completion decoding under structured fixtures.
- [x] Restore the pure completion mode, schema, and decoder edge matrix while
  leaving obsolete Run-controller tests deleted.
- [x] Remove the redundant test-fixture persistence mutation after
  `session.Create`; the exact runtime/runtimeview/session/projectview/client-ui
  suite passes under the mandatory cap without changing the cap or test
  selection.

Round-4 verification: the exact broad suite passed in 94.251 seconds;
`./scripts/test.sh ./server/runtime ./server/workflowruntime
./server/workflowrunner ./server/metadata/sqlitegen -count=1` passed in
131.766 seconds; server build and metadata schema dump passed. The
handwritten-production ledger is `added=8751 deleted=11983 net=-3232` against
`3c7d45a62`.

- [x] Remove Task Cancel from the server/store/public server API and consume
  its legacy columns only in the one-way migration.
- [x] Drop Run, Placement, and applied/rejected Transition persistence,
  views, triggers, queries, generated bindings, and server projections in
  `00065_remove_workflow_run_history`.
- [x] Replace server Board, Task Detail, Task List, Activity, Attention, and
  notification boundaries with Current Nodes, retained Sessions, pending
  Approvals, and Authority snapshots.
- [x] Prove script finalization, bounded automatic admission/wakeup,
  interruption cleanup after caller cancellation, current-node outgoing-edge
  completion mode, and forced-shell validation in focused server tests.
- [x] Run the focused server checkpoint:
  `./scripts/test.sh ./server/metadata ./server/workflowstore
  ./server/workflowexecution ./server/workflowrunner ./server/workflowsvc
  ./server/workflowview ./server/workflowattention ./server/sessionruntime
  ./shared/serverapi -count=1`.
- [x] Render/regenerate metadata, dump the effective schema, build
  `./server/... ./shared/serverapi ./shared/clientui`, and pass the
  net-negative gate at `-3512` handwritten production lines.
- [ ] Keep the Go remote client, CLI, Desktop, protocol version, public docs,
  repository-wide verification, and manual QA in their explicitly deferred
  follow-up slices. Do not restore server compatibility shapes to make those
  old clients compile.
