# Workflow Orchestration Spec

## Purpose And Scope

- Workflow orchestration turns Kent from a manually driven terminal coding-agent harness into a project-scoped workflow orchestrator.
- Users define workflows made of nodes, transition groups, and edges.
- Tasks move through graph nodes, Kanban statuses, agent workers, review loops, joins, and terminal states.
- Backend/domain/persistence/runtime are primary. Frontend surfaces follow backend API/read-model needs.
- Workflow API/read-model shapes are mutable before Kent 2.0.
- CLI is an internal backend-testing and agent-control surface, not the primary user manual QA surface.
- Real-provider workflow QA requires explicit User approval because it spends provider credits and can fail for provider/model reasons unrelated to orchestration correctness.

## Domain Model

- `Task` is the primary durable work item. Existing Kent sessions are execution artifacts under tasks.
- A task may accumulate many sessions through loops, branches, retries, and complex chains.
- Task creation creates a durable task at the workflow start node.
- Automation starts only through explicit task-start, which applies the start node's outgoing transition and requests automation for the first executable placement.
- Automation then runs through automatic nodes until terminal or blocked by question, approval/manual gate, error, capacity, interruption, cancellation, or validation.
- Task lifecycle state derives from node placements/runs plus task cancellation metadata, not from a separate task status enum.
- Node placement is workflow/Kanban state; active/interrupted/done conditions come from runs and terminal nodes.
- Terminal-node placements remain active sink placements. Board/read models infer done from an active placement whose node kind is terminal.
- Task cancellation records cancellation metadata, interrupts active runs, suppresses scheduling, and archives the task to terminal/Done for board visibility.

## Workflow Definitions

- Workflow definitions are globally reusable and linked to projects. Projects do not copy graph definitions.
- Workflow validation is project-contextual because subagent roles and workspace config differ by project.
- SQLite is authoritative for workflow definitions in v1. No stable graph file format/import/export is required in v1.
- V1 workflow definitions may be created/edited through backend API plus minimal CLI.
- Workflow definitions may be saved, linked, and made project default while semantic validation fails.
- Draft saving still enforces storage invariants: valid IDs, valid references, valid enum values, unique keys, and exactly one start node.
- Backlog task creation can persist tasks for an invalid linked/default workflow so users can collect work while fixing the graph. Task start and runtime scheduling require project-context validation and reject invalid workflows with accumulated safe actionable errors.
- A project can link multiple workflows and has one default workflow for task creation.
- Invalid default workflows are allowed. Task creation against an invalid default creates Backlog tasks, while starting/running those tasks fails with accumulated validation errors until the workflow is fixed.
- Workflow creation auto-creates ordinary editable `backlog` and `done` nodes.
- Workflows carry a monotonic `version` over persisted definition changes. This is traceability/stale-warning data, not immutable graph versioning.
- Metadata-only changes and graph changes each increment workflow `version` once; combined metadata+graph saves also increment it once; no-op saves increment neither.
- Tasks, runs, transitions, approvals, and edge snapshots store observed workflow version where historical traceability is required.
- Run-start snapshots and transition/approval/fan-out edge snapshots keep using their snapshot. Anything not snapshotted uses current workflow graph/config at execution time.

## Nodes, Edges, And Validation

- Nodes configure agent runs: subagent role, prompt/template, output schema, limits, stop conditions, and worktree/session execution policy.
- Edges configure transitions: target node, approval/manual interaction, context preservation, context source, input bindings, output requirements, routing, and join/aggregation behavior.
- Subagent role is the executable node assignee. There is no separate assignee field.
- Workflow nodes select existing subagent roles only. There are no per-node model/provider/tool/auth overrides.
- Visible executable/terminal node identity is Kanban column/status identity. Join nodes are internal merge plumbing omitted from board read models.
- Workflows can contain start, agent, join, and terminal nodes. Approval is an edge property, not a manual-node requirement.
- V1 has exactly one start node. The start node is non-executable and has no inputs.
- For task creation/automation, the start node must have exactly one outgoing transition group containing exactly one edge targeting an agent node.
- Terminal nodes are strict sinks. Manual reopen/rework is a user override execution, not a durable graph transition.
- Draft validation reports semantic errors but does not block save/link/default selection.
- Task creation and execution validation accumulate all safe actionable errors and reject invalid graph/role/input configurations.
- Execution-valid graphs reject detached islands: every node reachable from start, every non-terminal can reach terminal, terminal cannot auto-run.
- Cycles/self-loops are allowed outside restricted fan-out branch paths.
- Graph identity uses opaque server-generated primary keys plus stable human/model-facing keys.
- `node_key`, `transition_id`, `edge_key`, output field names, and binding names match `^[a-z][a-z0-9_]{0,63}$`.
- Workflow display names are labels, not references, and are trimmed non-empty strings capped at 120 chars.

## Completion Runtime

- Agent nodes complete by producing structured workflow completion, not by returning natural language.
- Completion chooses an outgoing transition group and supplies derived provision field values required by downstream consuming nodes.
- Runtime failure, cancellation, unanswered questions, and validation blockers are orchestration outcomes, not model-selected terminal statuses.
- Completion modes are `structured_output`, dynamic `complete_node` tool, `shell_command`, and `unstructured_output`. Global `[workflow].completion_mode` selects `auto`, `structured_output`, `tool`, `shell_command`, or `unstructured_output`; agent nodes can override it with the same values or inherit the global default.
- Start, join, and terminal nodes reject non-empty completion-mode overrides.
- Node completion-mode override is an agent-node execution property, not a transition-branch property. Edges define possible transition branches and parameter requirements; the source agent node owns the completion contract used to choose among them.
- `auto` resolves per run after session planning and tool availability are known: shell-unavailable runs use `unstructured_output`; workflows with any literal `continue_session` edge use `shell_command`; all other runs use structured output when provider capabilities support it and dynamic tool mode otherwise. `compact_and_continue_session` does not trigger shell fallback. A node-level `auto` override applies this policy even when the global config is a fixed mode.
- The resolved effective mode is stored on `task_runs.effective_completion_mode` and reused for resumed activations of the same run.
- Forced `structured_output` fails fast with an actionable error when unsupported. Forced `tool` always uses dynamic tool mode. Forced `shell_command` fails run start when the resolved runtime shell tool is unavailable.
- `complete_node` is workflow-control infrastructure and is available in tool completion mode regardless of subagent role tool config.
- `shell_command` mode keeps dynamic completion contracts out of request metadata and instructs the agent to run `kent task complete` from the shell. The command infers the current run from `KENT_SESSION_ID` in agent sessions; outside agent sessions it requires `--force` plus one explicit selector.
- `unstructured_output` mode keeps dynamic completion contracts out of request metadata and requires the assistant final answer to be exactly one raw JSON object.
- Normal assistant final answers are invalid in tool and shell-command workflow modes. Runtime appends a nudge and continues until valid completion, `ask_question`, interruption, cancellation, protocol cap, or runtime error.
- Completion payloads expose only optional `transition`, optional `commentary`, and server-derived possible provision fields as top-level properties. They never expose raw `next_node`.
- Provision field outputs are flat strings. Completion payload parsers accept any JSON value for a provision field and serialize non-string values into that flat string slot; downstream input bindings never receive structured values.
- Possible provision fields are optional in generated request metadata where a mode uses request metadata. Selected transition groups impose required provision fields after `transition` is known.
- Required provision fields must be present as trimmed non-empty strings after parser stringification.
- Size limits: output field name `<= 64` chars, output field description `<= 1000`, output value `<= 64 KiB`, commentary `<= 64 KiB`, task comment body `<= 256 KiB`.
- Dynamic request metadata in `structured_output` and `tool` modes can affect prompt-cache continuity when workflow completion contracts change. `shell_command` and `unstructured_output` keep completion contracts in appended prompt text instead of request metadata.
- Runtime observes durable external completion before each model turn, immediately after a model response returns and before assistant/tool persistence, and after local tool results are persisted.
- Runtime enforces one protocol cap. Repeated final answers in invalid modes or invalid completion attempts interrupt the run after `[workflow].max_invalid_completion_attempts = 5`.
- No wall-clock runtime cap is required for v1.

## Workflow Prompting

- Workflow runs use dedicated workflow-mode developer instructions.
- Prompt explains task identity, node role/assignee, selected completion behavior, question behavior, handoff/transition mechanics, task comments, and why ordinary final answers are invalid when the selected mode does not accept them.
- Workflow runtime builds on reusable headless/session infrastructure for session launch, runtime wiring, logging, progress, subagent role handling, and mode prompts.
- `RunPromptService.RunPrompt` final text is not workflow completion authority.
- Existing user goal state is not reused as workflow autonomy state.
- Workflow task sessions reject `/goal`; the workflow node/run is the task objective driver.
- If terminal workflow completion commits before accepted client steering is drained, the queued steering resolves with a visible failure and is not applied to the completed run.
- Task comment bodies are not automatically injected into agent context. When a task has visible comments, workflow-mode instructions include the visible comment count and a `kent task comment list <task>` pull command. Kent re-queries the visible comment count each time the workflow instructions are appended without mutating previously persisted model-visible prompt items.

## Questions And Approvals

- User questions use existing `ask_question` tool-call/session infrastructure.
- A model does not report `needs_user_input` as a completion status; it calls `ask_question`.
- The run pauses until answered.
- TUI and GUI prompt/approval state is derived from shared server prompt state. A client marks a question or approval resolved only after server acknowledgement.
- V1 must not introduce a shadow task-question table. If existing ask persistence cannot support workflow asks, upgrade ask persistence as source of truth.
- Ask rehydration must be proven before scheduler recovery depends on it.
- Scheduler uses a boundary such as `PendingAskResolver.CanRehydrate(sessionID, runID, askID)`.
- If pending ask cannot rehydrate, workflow run becomes interrupted with actionable resume path.
- Edge approval is a boolean edge property.
- When any edge in a selected transition group requires approval, the whole group waits for one approval before any target placement/run starts.
- Pending approvals store resolved transition group, edge set, workflow version, source node snapshot, transition display snapshot, target node snapshots, and effective edge config snapshots.
- Later graph edits do not change what a user approves.
- Every applied transition stores transition-edge snapshot rows, not only pending approvals.
- A task awaiting approval has no active placement; its live position is the pending transition's source node, surfaced as a synthesized `waiting_approval` placement.
- Manually moving a task that is awaiting approval overrides the proposed transition: the pending approval is marked `rejected` (auditable, not deleted) and the task moves from the approval's source node to the chosen target. This is the operator path to reject a proposed transition (e.g. sending an awaiting-approval plan back to Backlog).

## Context Preservation And Bindings

- Per-edge context preservation supports `new_session`, `continue_session`, and `compact_and_continue_session`.
- Continuation modes may select `immediate_source` or `node:<node_key>` as context source.
- Continuation modes apply the target node's subagent role context. A reused session remains authoritative for immutable contract fields already snapshotted by prior model dispatch.
- `new_session` uses current role config at its fresh context boundary.
- Consuming agent nodes own required inputs as named top-level string fields with descriptions.
- Prompt placeholders validate against the consuming node's required inputs through `.Inputs.<name>`.
- Prompt templates may reference guaranteed-prior agent node outputs through `.Nodes.<node_key>.<output_name>`.
- `.Nodes` references use stable node keys and declared source-node output fields. The referenced source node must dominate the consuming node in the workflow graph, the source node must not be the consuming node, and unsupported dynamic template access to `.Inputs` or `.Nodes` is invalid.
- Runtime freezes `.Nodes` values when the consuming run or approval edge is created. Prompt rendering uses the frozen values and does not re-resolve prior runs.
- Run start context is materialized by the workflow store from typed task/run records, run-start snapshots, typed transition-edge invocation snapshots, parameter values, context-preservation mode, and context source provenance. Target runs do not carry an opaque metadata envelope for prompt/context facts.
- The first executable node reached from `start` cannot declare upstream inputs and should use task fields such as `.TaskTitle` and `.TaskBody`.
- Source-node output fields declare reusable outputs that later prompts can reference through `.Nodes.<node_key>.<output_name>`.
- Edge input bindings and edge output requirements are not canonical workflow-editing concepts.
- The server derives provision fields, same-name input bindings, selected-transition output requirements, and possible completion fields from node required inputs, prompt node-output references, graph topology, and join provider selections.

## Parallelism And Joins

- Transition groups model fan-out. Multiple edges in one group create parallel branch placements/runs.
- Branches are ordinary workflow nodes, not subtasks.
- GUI-authored node groups are saved only as execution-shaped parallel groups. A node group contains branch nodes and one join; the fan-out remains canonical workflow graph structure through one transition group with multiple edges.
- A task may have multiple active placements/runs only when the graph explicitly fans out.
- Parallel joins always wait for all required inputs in v1. Racing/first-success semantics are out of scope.
- Fan-out topology must have exactly one unambiguous nearest common join reachable from every branch.
- Branch paths before that join may not terminate, enter nested fan-out, or contain cycles.
- Ambiguous/complex fan-out is rejected in v1.
- Fan-out join readiness uses persisted transition-edge snapshot rows from accepted source transition as expected edge set.
- Later graph edits do not change an in-flight parallel batch's wait set.
- Join nodes are non-agent fan-in points that aggregate inbound output values into deterministic results then follow their outgoing transition group.
- Agent synthesis belongs in a normal agent node after the join.
- Orchestrator-workers do not dynamically create workflow nodes or Kanban columns in v1.

## Scheduler And Recovery

- Scheduler has durable inputs in SQLite, but pending scheduler work and active runtime ownership are live memory, not durable run states.
- Runnable work derives from active executable placements with approved automation intent, no terminal run outcome, and no task cancellation.
- Pending-work ordering is scheduler memory.
- Active execution derives from live runtime registry/scheduler ownership.
- Concurrency limit is global only and configured in `[workflow].concurrency`.
- Scheduler does not own runtime leases. Runtime leases remain execution-control state, not scheduling authority.
- Startup rebuilds runnable work from durable state.
- Completed runs and pending approvals remain as-is.
- Waiting-for-question remains only if the pending ask can rehydrate.
- Started runs with no terminal outcome and no live owner after startup become interrupted with restart/shutdown reason.
- Interrupted runs are never automatically retried.
- Explicit resume continues the interrupted session/run from current transcript/worktree state.
- Completion/transition application uses a fence/generation or compare-and-swap so stale runtime callbacks cannot mutate superseded runs.
- Run completion and transition application remain one SQLite transaction.
- Runtime failures, cancellation, crashes, model/runtime interruptions, and fixable scheduling validation blockers converge on interrupted outcome with reason metadata.
- `failed` is reserved for unrecoverable corrupted orchestration state.

## Worktrees

- A task owns one managed worktree by default.
- All executable agent nodes require and reuse the task managed worktree.
- Kent creates the managed worktree on task start before first executable run is scheduled.
- Task worktree branch name is the task short ID.
- Worktree creation reuses existing worktree branch/root collision handling.
- Worktree deletion/retargeting treats non-terminal tasks referencing a managed worktree as blockers.
- Workflow worktree creation uses lower-level primitives directly.

## Project Keys And Task IDs

- Project keys are uppercase, globally unique within a persistence root, 2-8 chars, and match `^[A-Z][A-Z0-9]{1,7}$`.
- Project creation chooses a key explicitly; default suggestion can use the first three letters of project name.
- Existing projects without a key get one from default project-name logic when task support initializes, with collision handling.
- Project keys are editable at any time, including after a project has tasks. A key change only sets the prefix for tasks created afterward and never rewrites existing task short IDs, so a project's history can contain mixed prefixes. The change is rejected only for format violations or a collision with another project's key.
- Existing task short IDs keep their historical key forever; a rename does not cascade to them.
- Task short IDs are stored durable product identifiers, not derived display strings.
- Task required fields are title, short ID, and body.
- Task metadata is designed for import/export and may include `source_url`.

## Comments

- Agents may add, replace, and delete task comments through CLI/API task management.
- There are no model-callable comment tools.
- Comments record author/source agent when available.
- Comments stay in Kent persistence, not files in the worktree.
- Task comments are hard-deleted task-local notes.
- CLI task comment management accepts both `kent task comment ...` and `kent task comments ...`.
- Comment rows do not store source-run links, deleted tombstones, or opaque metadata.
- Include-deleted comment APIs and read-model state are not product scope.

## Persistence And Schema

- Use SQLite for structured workflow/task state. Keep transcripts and large session artifacts file-backed.
- Production metadata SQL is declared in `server/metadata/queries.sql` and consumed through generated `server/metadata/sqlitegen` APIs. Transaction-scoped SQLite lifecycle SQL that sqlc cannot emit is declared in `server/metadata/lifecycle.sql` and consumed through generated `server/metadata/sqlitelifecyclegen` APIs. Workflow packages do not embed private SQL files or execute raw SQL strings directly.
- Workflow implementation package boundaries are locked:
- `server/workflow`: pure domain types, validation, state-machine logic.
- `server/workflowstore`: metadata persistence adapter.
- `server/workflowsvc`: use-case/service orchestration.
- `server/workflowscheduler`: runnable derivation and workers.
- `server/workflowruntime`: completion/runtime contracts used by runtime.
- `server/workflowrunner`: session/runtime/headless adapter for scheduling.
- `server/workflowview`: read models.
- Start node is derived from `workflow_nodes.kind = 'start'` and enforced with a partial unique index; do not store `workflows.start_node_id`.
- Workflow graph storage derives membership from relationships instead of duplicate workflow IDs where practical.
- Workflow definitions do not persist opaque `metadata_json` on workflows, nodes, node groups, transition groups, or edges.
- Project workflow links are active membership rows only. Do not soft-unlink.
- Unlink hard-deletes unused links. If tasks exist, user must move/delete tasks before unlinking.
- Blocked unlink returns typed blockers with counts/references.
- Retiring a workflow means deleting the workflow definition and cascading/deleting tasks through explicit workflow deletion.
- `tasks.project_workflow_link_id` is the source of truth for task project/workflow pairing.
- Direct duplicated `tasks.project_id` and `tasks.workflow_id` columns are removed with a hard cutover.
- Project default pointers use `projects.default_project_workflow_link_id` and `projects.primary_workspace_id`, each constrained to rows owned by the same project.
- Workspace/worktree labels, availability, primary/default status, and main-worktree status are read-model facts derived from canonical roots/pointers.
- Runtime leases persist durable controller-token facts only: `id`, `session_id`, and `created_at_unix_ms`.
- Workflow invalidation events are process-local live signals, not durable/replayable sequence state. SQLite does not store `workflow_events`.
- GUI clients refetch read models after subscription ACK/reconnect/error and treat live events as invalidation hints.
- There is no product archive lifecycle for workflows or nodes.
- Workflow deletion impact previews return counts only.
- Confirmed workflow deletion removes DB workflow-linked tasks, links, and graph rows; it blocks active/runnable runs and default-without-replacement states.
- Workflow deletion is DB-only by default and preserves artifacts/worktrees. Artifact cleanup is an explicit future opt-in path.
- Batch graph save uses a store-owned transaction with expected workflow `version`, draft validation, process-local edit semantics, typed blockers, and confirmation for unreferenced graph row removals.
- Graph saves never delete or move tasks; whole-workflow deletion is the task-deleting path.
- Run-start context has one store-owned materialization seam. It resolves target run invocation facts through the accepted transition-edge snapshot that created the target placement.

## Schema Minimization Decisions

- Approved cutover removals include `workflow_events`, `project_workflow_links.unlinked_at_unix_ms`, duplicated task project/workflow columns, workflow graph opaque metadata, `runtime_leases.request_id`, workspace/worktree display labels, `task_comments.source_run_id`, comment soft-delete, and redundant indexes when equivalent unique/leading-key indexes remain.
- Keep `tasks.source_url` as a structured task field.
- Keep `tasks.short_id` as stored durable product data.
- Task sequence allocation is transactional behavior, not product state stored as `projects.next_task_seq`.
- Runtime context/source-run hints belong in typed relations or derivation, not `task_runs.metadata_json`.
- Removing `task_runs.metadata_json` uses a one-way schema migration that backfills valid existing JSON into typed storage and then removes the column. Runtime code does not keep a `metadata_json` read fallback after the migration.
- Continuation provenance persists typed source run IDs. Source session IDs derive from the referenced source run when run-start context is materialized.
- Typed transition-edge snapshots own accepted branch invocation facts: context source, prompt template, transition parameters, prior parameter values, and frozen pending-approval target run-start snapshots.
- Observed run workflow version derives from run-start snapshots; `task_runs.workflow_revision_seen` is not the long-term authority.
- Keep `task_comments.author_id`; future multi-agent/user identity display depends on it.
- Run-start snapshots are the long-term historical node/graph contract authority. Transition-edge snapshots keep accepted branch invocation facts rather than generic duplicate display/config snapshots.
- Keep `sessions.first_prompt_preview` as stored listing/read-model data.
- Keep `sessions.input_draft` as stored unsent prompt recovery data.

## CLI Surface

- Minimal workflow/task CLI exists to exercise backend behavior and teach agents task usage.
- Agents must be able to build and edit complete workflow definitions through CLI commands; command grouping and syntax are not stable product contracts.
- High-level workflow mutation subcommands are the complete agent editing path; workflow import/export is a separate sharing feature, not the primary edit interface.
- High-level workflow mutation commands use a CLI-local draft-edit module, then persist through batch graph save. The server does not expose row-level or semantic edit RPC routes for workflow graph mutation. Extract the draft-edit module only when a second Go caller exists.
- Row-level workflow graph RPC methods, client methods, protocol constants, and route entries are removed in the graph-save cutover instead of preserved as migration stubs.
- CLI output must include stable IDs needed by later commands.
- `kent task list` uses `status` for workflow/Kanban status, backed by current workflow column/node keys such as `backlog`, `recon`, `plan`, and `done`; `--column` is an alias for `--status`, and raw node IDs are not exposed as list filters.
- `kent task list --run-status` filters the coarse operational states `open`, `running`, `done`, and `canceled`. Human output distinguishes `Status:` (workflow/Kanban status key) from `Run status:` (coarse operational state), and JSON output carries both as structured fields. JSON keeps `status` as a compatibility alias for coarse run status; `run_status` is the canonical coarse field and `status_keys` carries workflow/Kanban status keys.
- `kent task list` filters and sorts before pagination through server-owned structured request fields. Multiple values for one filter are ORed; different filter types are ANDed. Tasks with multiple active placements match a status filter when any active placement matches, display all current status keys in board order, and sort by the earliest current status order.
- `kent task list` default ordering is `status:asc,updated:desc`, where `status` uses board column order and `updated` is newest-first. Custom `--sort` accepts ordered `field:direction` selectors for at least `created`, `updated`, `status`, `run_count`, and `title`; selectors can be comma-separated in one flag and may be supplied by repeated flags.
- `kent task complete` accepts dynamic parameter flags, repeatable `--param name=value`, and `--json`/`--json-file` completion payload input. JSON input modes print JSON responses.
- `kent task edit <task>` mutates an existing task's title, body, and source workspace through `UpdateWorkflowTask`. It requires at least one of `--title`/`--body`/`--body-file`/`--source-workspace`, reuses the current title when `--title` is omitted, and is available to agents like `task create` (no human-only gate). `--json` prints the update response.
- `kent task create` and `kent task edit` accept `--source-workspace` as either a workspace id or a path; a path is resolved through its project binding. An omitted source workspace leaves it unchanged on edit.
- Unsupported commands may fail loudly before backend semantics land rather than implementing partial behavior.

## Q/A Decisions Preserved

- Q: Should workflow definitions use a stable graph file format in v1? A: No; SQLite/API/CLI are authoritative for v1.
- Q: Is task creation the same as starting automation? A: No; creation makes a backlog task, and task-start is explicit.
- Q: Is completion mode per workflow/node? A: A global `[workflow].completion_mode` config provides the default, agent nodes may override it, and per-run effective-mode snapshots record the resolved value.
- Q: Should workflow runs have a wall-clock cap? A: No v1 wall-clock cap.
- Q: Should v1 auto-retry interrupted/runtime-failed runs? A: No; human resume is required.
- Q: Are racing/first-success parallel branches in scope? A: No; joins wait for all required inputs.
- Q: Can orchestrator-workers dynamically create workflow nodes/columns? A: No in v1.
- Q: Should pending workflow questions get a task-question shadow table? A: No; use `ask_question` source of truth or upgrade ask persistence.
- Q: Does real-provider workflow QA need explicit approval? A: Yes, ask the User before spending provider credits.
- Q: How do agents complete shell-command workflow runs? A: They run `kent task complete` from a shell command; `KENT_SESSION_ID` targets their current run.
- Q: Must low-level workflow CLI command shape stay stable? A: No; full workflow build/edit capability for agents matters, not the specific command grouping.
- Q: Should full workflow graph files be the primary agent editing interface? A: No; agents edit through high-level CLI mutation commands, while import/export is a separate sharing feature.
- Q: Where should high-level workflow edit intelligence live? A: Start with a CLI-local draft-edit module; extract it only when a second Go caller exists. The server persists graph edits only through batch graph save.
- Q: Should row-level workflow graph RPC methods remain as migration stubs? A: No; remove the protocol methods, clients, routes, service methods, and tests for that external seam.
- Q: Should `tasks.short_id` be stored or derived from `project_key + task_seq`? A: Keep it stored as durable product data.
- Q: Should `projects.next_task_seq` stay stored? A: No; replace it with transactional task sequence allocation.
- Q: Should `task_runs.metadata_json` stay? A: No; use a one-way migration that backfills valid JSON into typed storage, removes the column, and keeps no runtime read fallback.
- Q: Should continuation provenance persist source session IDs? A: No; persist typed source run IDs and derive the source session from the referenced run when materializing run-start context.
- Q: Where do frozen branch invocation facts live? A: Typed transition-edge snapshots own context source, prompt template, transition parameters, prior parameter values, and frozen pending-approval target run-start snapshots.
- Q: Should `task_runs.workflow_revision_seen` stay stored? A: No; derive it from run-start snapshots after migration.
- Q: Should `task_comments.author_id` stay? A: Yes; keep it for future identity display.
- Q: Should transition-edge display/config snapshots stay? A: Keep typed accepted branch invocation facts on transition-edge snapshots; remove redundant display/config duplication that is not needed to materialize run starts or audit applied branches.
- Q: Should `sessions.first_prompt_preview` stay stored? A: Yes.
- Q: Should `sessions.input_draft` stay stored? A: Yes.
