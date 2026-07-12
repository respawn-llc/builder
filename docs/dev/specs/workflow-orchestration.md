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
- Every workflow has exactly one execution target policy: no managed worktree, source `HEAD`, repository default branch, custom Git ref, or operator selection when execution first begins.
- New workflows default to `ask_on_first_execution`. Workflows created before execution target policy existed migrate to source `HEAD` so their established behavior continues.
- Custom-ref policy stores one Git revision value. Other policies do not retain a custom-ref value.
- A draft may save custom-ref policy without a value and reports a semantic validation issue. Saving validates presence only; Git resolution is authoritative against the task's source workspace when execution first begins.
- Workflow creation auto-creates ordinary editable `backlog` and `done` nodes.
- Workflows carry a monotonic `version` over persisted definition changes. This is traceability/stale-warning data, not immutable graph versioning.
- Metadata-only changes and graph changes each increment workflow `version` once; combined metadata+graph saves also increment it once; no-op saves increment neither.
- Tasks, runs, transitions, approvals, and edge snapshots store observed workflow version where historical traceability is required.
- Run-start snapshots and transition/approval/fan-out edge snapshots keep using their snapshot unless a node-specific runtime contract says otherwise. Script nodes live-load their current `script_path` and completion contract from the workflow graph when the run executes/completes.

## Nodes, Edges, And Validation

- Nodes configure workflow states and executable behavior. Agent nodes configure subagent role, completion mode, and worktree/session execution policy. Script nodes configure an executable script path.
- Edges configure transitions: target node, approval/manual interaction, context preservation, context source, input bindings, output requirements, routing, and join/aggregation behavior.
- Subagent role is the executable node assignee. There is no separate assignee field.
- Workflow nodes select existing subagent roles only. There are no per-node model/provider/tool/auth overrides.
- Visible executable/terminal node identity is Kanban column/status identity. Join nodes are internal merge plumbing omitted from board read models.
- Workflows can contain start, agent, script, join, and terminal nodes. Approval is an edge property, not a manual-node requirement.
- V1 has exactly one start node. The start node is non-executable and has no inputs.
- For task creation/automation, the start node must have exactly one outgoing transition group containing exactly one edge targeting an executable node.
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

## Script Nodes

- Script nodes are first-class executable workflow nodes. They can be Start targets, fan-out branches, join predecessors/successors, manual automation targets, and board columns anywhere agent nodes are accepted by graph semantics.
- Script nodes store nullable `script_path`. Missing, nonexistent, directory, and non-executable paths do not block graph save or node add/update; they block execution validation, task start, or target run execution as appropriate.
- Relative script paths resolve against the task execution root. Absolute paths resolve on the Kent server host.
- Script execution directly `exec`s the resolved file with the task execution root as cwd. It does not use a shell wrapper, retries, or a timeout.
- Script stdin is one JSON object. Incoming workflow parameter values are top-level properties. `_kent` is reserved for minimal runtime identifiers, including `run_id` and `placement_id`.
- Script stdout is parsed as the workflow completion JSON using the same completion contract as agent nodes. Stderr is diagnostics only and is not mixed into completion parsing.
- Script run failures, invalid stdout, invalid script path, cancellation, and execution errors interrupt the run with bounded structured details.
- Script completion persists transition actor `script`.
- Resuming an interrupted script run reruns the same task run generation with cached incoming parameter values, the current workflow DB script path, and the current script outgoing transition/output contract.

## Workflow Prompting

- Workflow runs use dedicated workflow-mode developer instructions.
- Prompt explains task identity, node role/assignee, selected completion behavior, question behavior, handoff/transition mechanics, task comments, and why ordinary final answers are invalid when the selected mode does not accept them.
- Workflow runtime builds on reusable headless/session infrastructure for session launch, runtime wiring, logging, progress, subagent role handling, and mode prompts.
- `RunPromptService.RunPrompt` final text is not workflow completion authority.
- Existing user goal state is not reused as workflow autonomy state.
- Workflow task sessions reject user `/goal` control; the workflow node/run is the task objective driver. Agents may still set themselves goals and complete them, per the agent goal rules in core-runtime-tools.
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
- Pending approvals store resolved transition group, edge set, workflow version, source node snapshot, transition display snapshot, target node snapshots, effective edge config snapshots, and frozen context-source resolution.
- Later graph edits do not change what a user approves.
- Every applied transition stores transition-edge snapshot rows, not only pending approvals.
- A task awaiting approval has no active placement; its live position is the pending transition's source node, surfaced as a synthesized `waiting_approval` placement.
- Manually moving a task that is awaiting approval overrides the proposed transition: the pending approval is marked `rejected` (auditable, not deleted) and the task moves from the approval's source node to the chosen target. This is the operator path to reject a proposed transition (e.g. sending an awaiting-approval plan back to Backlog).
- Missing-edge manual overrides cannot target executable nodes. Manual movement into an agent or script node requires a concrete workflow edge so the target run has a real prompt/contract.
- Task start, manual movement into an executable node, and approval into an executable node apply no movement, approval, or scheduling when target selection is required. A valid selection retries and applies the original action once; dismissal leaves it unchanged.

## Context Preservation And Bindings

- Per-edge context preservation supports `new_session`, `continue_session`, and `compact_and_continue_session`.
- Continuation modes may select `immediate_source`, `node:<node_key>`, `previous_target`, or `previous_target_or_new` as context source.
- `previous_target` resolves the latest completed run of the target node before the transition event and fails when none exists.
- `previous_target_or_new` resolves the latest completed run of the target node before the transition event when one exists; otherwise the target run starts with effective `new_session` and no source run/session.
- Pending approvals freeze context-source resolution before approval. A fallback-to-new result remains effective `new_session` even if another target run completes before approval, and a resolved prior-target source remains fixed even if a newer target run completes before approval.
- Continuation modes apply the target node's subagent role context. `continue_session` preserves the reused session's contract generation. `compact_and_continue_session` compacts the reused session and establishes a fresh target-node contract generation, including model/provider setup, generation parameters, capabilities, enabled tools, native web-search mode, prompt snapshots, context budget, and cache lineage.
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

- Scheduler has durable inputs in SQLite, but pending scheduler work and active runtime execution are live memory, not durable run states.
- Runnable work derives from active executable placements with approved automation intent, no terminal run outcome, and no task cancellation.
- An executable task whose run has not started is `queued`.
- Pending-work ordering is scheduler memory.
- Active execution derives from the live runtime registry and scheduler.
- Concurrency limit is global only and configured in `[workflow].concurrency`.
- Scheduler does not control runtime execution. Runtime execution is ownerless registry lifecycle state, not scheduling authority.
- Startup rebuilds runnable work from durable state.
- Completed runs and pending approvals remain as-is.
- Waiting-for-question remains only if the pending ask can rehydrate.
- Started runs with no terminal outcome and no live owner after startup become interrupted with restart/shutdown reason.
- Interrupted runs are never automatically retried.
- Explicit resume is task-level: it continues every interrupted run of the task from current transcript/worktree state, with no run selection.
- Explicit interrupt is task-level with an optional session-id selector: no session interrupts all running runs of the task; a specific session interrupts only that run. Run id is never an operator-facing selector — the public per-run identifier is the session id.
- Manual moves are rejected while a task has any started active run that is not completed or interrupted, including runs waiting on a question. The operator must interrupt, wait for completion, or resume from an interrupted state before manually moving the task.
- Completion/transition application uses a fence/generation or compare-and-swap so stale runtime callbacks cannot mutate superseded runs.
- Run completion and transition application remain one SQLite transaction.
- Runtime failures, cancellation, crashes, model/runtime interruptions, and fixable scheduling validation blockers converge on interrupted outcome with reason metadata.
- `failed` is reserved for unrecoverable corrupted orchestration state.
- Kent does not migrate, reconcile, diagnose, or repair unfinished historical runs owned by completed placements.

## Task Status And Listing

- Task detail, workflow board cards, and paginated task lists use one server-authoritative typed task-status projection derived only from current placements and their runs.
- Task status is UI-neutral structured data. Clients render and localize status labels.
- One primary status uses this precedence: canceled, done, waiting for a question, waiting for approval, interrupted, running, queued, backlog, active.
- The status projection preserves all applicable typed attention kinds and run references even when parallel branches have different conditions.
- Workflow validity is workflow-level state and is not a task status.
- Task lists expose typed task status and attention filters. They do not expose a separate coarse run status.
- CLI `--status` filters typed task status, `--column` filters workflow node keys, and `--attention` filters attention kinds.
- Either project or workflow task-list scope may be inferred only when exactly one active link is possible. Zero links return a typed not-linked error; multiple links return a typed ambiguity error with the available choices.
- Explicit workflow selectors are workflow UUIDs. When both project and workflow are supplied, Kent validates their active link.
- Task-list status sorting follows primary typed-status precedence; column sorting follows workflow column position.

## Execution Targets And Worktrees

- A workflow execution target policy is evaluated only when an unlocked task first reaches an executable node through task start, manual movement, or approval.
- No managed worktree uses the source workspace as the execution root, supports non-Git workspaces, and creates no branch or worktree and runs no worktree setup.
- A no-managed-worktree target follows the task's current source workspace. Changing that workspace intentionally changes later execution roots.
- Source `HEAD`, repository default branch, and custom Git ref resolve server-side to an immutable commit before managed-worktree creation. Custom ref accepts any Git revision that resolves to a commit.
- Repository default branch uses configured local remote-HEAD metadata: `origin` when configured, otherwise one unambiguous configured remote HEAD. Kent does not contact remotes or guess branch names; missing or ambiguous metadata makes the configured target unavailable.
- `ask_on_first_execution` and an unavailable configured target use the same task-local selection flow. They offer no managed worktree, source `HEAD`, repository default branch, and custom Git ref.
- For `ask_on_first_execution`, repository default branch is preselected. For an unavailable configured target, the configured mode and custom-ref input remain selected when useful; otherwise repository default branch is preselected.
- An unresolvable configured target asks the operator to select a concrete target and explains which configured target failed and why.
- Selection-required results distinguish exactly two interaction reasons: workflow policy requires selection or configured target is unavailable. Every selection flow offers all four concrete modes; the wire contract does not carry a dynamic allowed-mode list.
- Failure to resolve an explicitly selected custom ref is a validation failure. It does not recursively request selection or fall back to another target.
- A task locks target-selection provenance only when the initiating action successfully reaches its first executable placement. Later nodes and retries reuse the locked mode and managed requested/resolved facts despite workflow edits or Git ref movement.
- Every pre-upgrade task with a recorded managed worktree and usable recorded HEAD metadata continues using that worktree after upgrade, regardless of whether it already has a run. Its observed commit is identified as legacy provenance and is not presented as a known original branch point.
- Pre-upgrade tasks without a managed worktree remain unlocked and inherit their migrated workflow's source-`HEAD` policy.
- Managed targets use the existing task-worktree creation, setup, and collision behavior. Before the first executable run is scheduled, setup either succeeds for the newly created candidate in that request or a later explicit request accepts the same available, base-compatible candidate as manually repaired.
- Managed worktree setup failure leaves the initiating action unapplied and unscheduled. Any created worktree remains available for inspection or manual repair.
- Setup runs only when the current request creates or recreates a worktree root. A later retry trusts an already-existing compatible root and does not rerun setup; no durable setup-readiness state exists.
- A managed target remains tied to its original source workspace, but its current root, metadata binding, branch history, and named branch may change.
- Before execution Kent validates that the bound root is the exact worktree root for the source repository and has a named branch. It never compares current history or HEAD with the originally resolved commit, and it accepts any current named branch.
- When a locked managed root or metadata binding is missing, the initiating action or workflow runner may synchronously invoke the single worktree materializer to conservatively restore an existing named branch at a collision-safe managed root, persist the relation, and run setup for the recreated root.
- Conservative repair never recreates a missing branch from the old base commit, overwrites an existing directory, resets or renames a branch, accepts detached HEAD, repairs another repository, or infers ownership by scanning arbitrary roots. Unsafe or ambiguous states return one typed locked-target error with a small product-level cause.
- There is no target-replacement flow. A locked target is never converted to no managed worktree.
- Task detail always exposes the source workspace. After lock it exposes the current target mode and derived execution root, plus the requested revision and resolved commit. Managed targets show the current named branch only while the root is available and do not persist branch name in the target snapshot.
- Human task detail shortens the resolved commit for readability. Structured JSON retains the full commit value.
- Initial managed worktree creation uses the task short ID as the branch name.
- Worktree creation reuses existing worktree branch/root collision handling.
- Worktree deletion/retargeting treats non-terminal tasks referencing a managed worktree as blockers.
- Worktree deletion blocks if another session targeting the worktree has an active run, and holds a run-exclusion on all targeting sessions across the `git` removal so no new run can start mid-deletion; a run submitted during the window is rejected with `ErrSessionWorktreeDeleting` until the exclusion releases.
- Initial materialization and conservative locked-target repair reuse one managed-worktree creation/setup implementation.
- The CLI task-start command may select a concrete target for an unlocked task even when the workflow has a fixed policy. Task creation has no target override.
- CLI target selection uses `--execution-target none|head|default-branch|ref:<revision>`; custom Git revisions require the explicit `ref:` namespace.
- CLI task start never prompts interactively. Selection-required output identifies the reason and concrete rerun flags in human output and exposes the same typed outcome in JSON.

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

- Approved cutover removals include `workflow_events`, `project_workflow_links.unlinked_at_unix_ms`, duplicated task project/workflow columns, workflow graph opaque metadata, the `runtime_leases` table, workspace/worktree display labels, `task_comments.source_run_id`, comment soft-delete, and redundant indexes when equivalent unique/leading-key indexes remain.
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
- `kent task list` exposes one typed task status. `--status` filters primary status, `--attention` filters typed attention, and `--column` filters workflow node keys.
- `kent task list` filters and sorts before pagination through server-owned structured request fields. Multiple values for one filter are ORed; different filter types are ANDed. Tasks with multiple current placements expose all matching column keys in workflow order.
- `kent task list` default ordering is `status:asc,updated:desc`, where `status` uses primary typed-status precedence and `updated` is newest-first. Custom `--sort` accepts ordered `field:direction` selectors for `created`, `updated`, `status`, `column`, `run_count`, and `title`; selectors can be comma-separated in one flag and may be supplied by repeated flags.
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
