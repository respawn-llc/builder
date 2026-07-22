# Workflow Orchestration Spec

## Purpose And Scope

- Workflow orchestration turns Kent from a manually driven terminal coding-agent harness into a project-scoped workflow orchestrator.
- Users define workflows made of nodes, transition groups, and edges.
- Tasks move through graph nodes, Kanban statuses, agent workers, review loops, joins, and terminal states.
- Backend/domain/persistence/runtime are primary. Frontend surfaces follow backend API/read-model needs.
- Bundled clients and the server may make hard workflow RPC contract cutovers together. Incompatible cutovers bump the protocol version and do not retain old-client compatibility adapters.
- CLI is an internal backend-testing and agent-control surface, not the primary user manual QA surface.
- Real-provider workflow QA requires explicit User approval because it spends provider credits and can fail for provider/model reasons unrelated to orchestration correctness.

## Domain Model

- `Task` is the primary durable work item. Sessions are retained artifacts associated with agent Nodes on the Task.
- A Task directly owns its Current Nodes. It usually has one and may have several only while a fan-out executes parallel branches.
- Current Nodes have no independent entity IDs. Parallel state uses the existing Transition Branch Keys needed by the fan-out and Join.
- An executable current Node owns only its current execution state and optional Session binding. Script Nodes have no Session.
- Leaving a Node removes that current execution state. Kent does not retain completed Node execution, execution-attempt, or workflow-movement records as hidden history.
- Task creation creates a durable Task at the Workflow's Start Node.
- Automation starts only through explicit Task Start, which applies the Start Node's outgoing Transition and adds the first executable current Node.
- Automation continues through automatic Nodes until terminal or blocked by a Question, Approval/manual gate, error, capacity, interruption, or validation.
- Task lifecycle projection combines durable Current Nodes with one Immutable Live Snapshot; it is not a separate durable task-status enum.
- Running, queued, and waiting require matching Exact Execution Scope evidence. A current Terminal Node makes the Task done.
- A Project owns one shared label catalog reused by every workflow board linked to that Project. Tasks may only use labels owned by their own Project.
- Labels are many-to-many organizational metadata on tasks. They never affect workflow state, scheduling, prompts, task status, or execution.

## Task Labels And Filtering

- Each label has an immutable UUID v4 identity. Its mutable name is trimmed, 1–64 characters, preserves display capitalization, and permits Unicode letters and numbers plus spaces and `: & * % $ # @ ! ? . , / \ + | - _ ~ '`.
- Label names are case-insensitively unique within a Project. Capitalization-only rename is allowed without changing identity or task assignments.
- A Project may own at most 100 labels. The server enforces the bound, and clients load the complete bounded catalog without pagination.
- A task may use any subset of its Project's catalog; there is no separate per-task label limit.
- Label creation, rename, deletion, assignment, and removal remain available regardless of whether affected tasks are Backlog, active, running, interrupted, done, or canceled.
- Label catalog and assignment changes do not change task updated timestamps; label events refresh projections without reordering tasks or moving pagination anchors.
- Task creation may atomically assign existing Project labels. Later assignment changes use idempotent add/remove semantics: adding an existing assignment or removing an absent assignment succeeds and returns the authoritative resulting label set.
- Renaming takes effect everywhere without changing assignments. Deletion requires confirmation and atomically removes the label from every task; the confirmation does not require an affected-task count.
- Labels have no color or manual ordering in the initial release. Label catalogs and assigned chips display in case-insensitive alphabetical order; label-based task sorting is not part of the initial release.
- Label filtering is server-side for workflow boards and paginated task lists.
- OR matches tasks carrying at least one selected named label. AND matches tasks carrying every selected named label while allowing additional labels. One selected label behaves identically in both modes.
- `No labels` matches tasks with zero label assignments and is mutually exclusive with named-label selection. No selected labels means no label restriction.
- The complete label expression is ANDed with every other active task-list filter type. Filtering then preserves existing sorting and cursor semantics and never loads a full board or task list into a client.

## Workflow Definitions

- Workflow definitions are globally reusable and linked to projects. Projects do not copy graph definitions.
- Workflow validation is project-contextual because subagent roles and workspace config differ by project.
- SQLite is authoritative for workflow definitions in v1. No stable graph file format/import/export is required in v1.
- V1 workflow definitions may be created/edited through backend API plus minimal CLI.
- Workflow definitions may be saved, linked, and made project default while semantic validation fails.
- Draft saving still enforces storage invariants: valid IDs, valid references, valid enum values, unique keys, and exactly one start node.
- Backlog task creation can persist tasks for an invalid linked/default workflow so users can collect work while fixing the graph. Task start and runtime scheduling require project-context validation and reject invalid workflows with accumulated safe actionable errors; the effective-role `ask_question` rule below is an explicit initial-execution-only exception.
- A project can link multiple workflows and has one default workflow for task creation.
- Invalid default workflows are allowed. Task creation against an invalid default creates Backlog tasks, while starting/running those tasks fails with accumulated validation errors until the workflow is fixed.
- Every workflow has exactly one execution target policy: no managed worktree, source `HEAD`, repository default branch, custom Git ref, or operator selection when execution first begins.
- New workflows default to `ask_on_first_execution`. Workflows created before execution target policy existed migrate to source `HEAD` so their established behavior continues.
- Custom-ref policy stores one Git revision value. Other policies do not retain a custom-ref value.
- A draft may save custom-ref policy without a value and reports a semantic validation issue. Saving validates presence only; Git resolution is authoritative against the task's source workspace when execution first begins.
- Workflow creation auto-creates ordinary editable `backlog` and `done` nodes.
- Workflows carry a monotonic `version` over persisted definition changes. This is current-definition and pending-Approval stale-warning data, not immutable graph versioning.
- Metadata-only changes and graph changes each increment workflow `version` once; combined metadata+graph saves also increment it once; no-op saves increment neither.
- Pending Approvals retain the Workflow Version and branch snapshots the operator is approving.
- Executable Current Nodes resolve the latest Workflow definition and Runtime Parameter Contract when they start or resume. Completed execution contracts are not retained as history.
- Script Nodes load their current `script_path` and completion contract from the latest Workflow definition whenever they execute.

## CLI Workflow Selection And Discovery

- CLI workflow selectors are bare canonical UUIDv4 values. Workflow display names and prefixed persistence identifiers are not selectors.
- Every CLI command that accepts a workflow selector uses one shared parser and emits copyable bare UUIDs in human and JSON output. This CLI boundary does not change persisted or server-facing workflow identifiers.
- Long flags are rendered with double dashes in help, examples, and Kent-authored diagnostics. Single-dash long flags remain accepted for compatibility; standard-library parser failures retain their native formatting.
- `kent workflow list` is paginated. `--project <path-or-id>` filters the existing workflow list to workflows linked to the resolved project.
- Project-filtered workflow discovery is served by one paginated database query. It does not load or validate workflow graphs.
- Project-filtered workflow results are ordered with the project default first, then by project-local task activity, then by workflow name.
- Project-filtered human results preserve the global one-line format and append default/link status plus execution-target policy. JSON includes the resolved project ID once and adds only the default-link fact to each workflow record.
- `kent workflow inspect <uuid> --summary` returns only global workflow metadata: name, bare UUID, description, version, and execution-target policy. It does not accept project context; full inspect retains the workflow graph.
- `kent task show` does not accept a workflow selector. It reports the selected task's actual workflow with a bare UUID.
- Task creation without an explicit workflow uses the project default. If no default exists and exactly one workflow is linked, Kent uses that workflow.
- Task creation with no linked workflows explains that a workflow must be created or linked before retrying.
- Task creation with several linked workflows and no default does not enumerate candidates. It directs the operator to paginated project workflow discovery, an explicit `--workflow <uuid>` retry, or default selection.

## Nodes, Edges, And Validation

- Nodes configure workflow states and executable behavior. Agent nodes configure subagent role, completion mode, and worktree/session execution policy. Script nodes configure an executable script path.
- Edges configure transitions: target node, approval/manual interaction, context preservation, context source, input bindings, output requirements, routing, and join/aggregation behavior.
- Subagent role is the executable node assignee. There is no separate assignee field.
- Workflow nodes select existing subagent roles only. There are no per-node model/provider/tool/auth overrides.
- Agent nodes do not own invocation prompts. Each transition branch into an agent node owns its prompt template; node add/update contracts and persistence contain no node prompt or prompt fallback.
- Every agent Node's effective subagent role, including the default and built-in roles, must have `ask_question` enabled. Validation reports every affected Node without rewriting role configuration; drafts and Backlog Tasks remain allowed. Ordinary Task Start, Resume, and every manual move to an executable target validate the latest Workflow definition before execution-target selection, current-Node mutation, Approval, or execution.
- Visible executable/terminal node identity is Kanban column/status identity. Join nodes are internal merge plumbing omitted from board read models.
- Workflows can contain start, agent, script, join, and terminal nodes. Approval is an edge property, not a manual-node requirement.
- V1 has exactly one start node. The start node is non-executable and has no inputs.
- For task creation/automation, the start node must have exactly one outgoing transition group containing exactly one edge targeting an executable node.
- Terminal nodes are strict sinks. Manual reopen/rework is a user override execution, not a durable graph transition.
- Draft validation reports semantic errors but does not block save/link/default selection.
- Task creation and execution validation accumulate all safe actionable errors and reject invalid graph/role/input configurations.
- Execution-valid graphs reject detached islands: every node reachable from start, every non-terminal can reach terminal, terminal cannot auto-run.
- Cycles/self-loops are allowed outside restricted fan-out branch paths.
- New draft nodes, node groups, transition groups, and edges receive client-generated UUIDv4 IDs. Preview and save validate and persist those IDs unchanged; no temporary-ID mapping or reservation RPC exists. Human/model-facing keys remain stable semantic references.
- `node_key`, `transition_id`, `edge_key`, output field names, and binding names match `^[a-z][a-z0-9_]{0,63}$`.
- Workflow display names are labels, not references, and are trimmed non-empty strings capped at 120 chars.

## Completion Runtime

- Agent nodes complete by producing structured workflow completion, not by returning natural language.
- Completion chooses an outgoing transition group and supplies derived provision field values required by downstream consuming nodes.
- Runtime failure, unanswered questions, interruption, and validation blockers are orchestration outcomes, not model-selected terminal statuses.
- Completion modes are `structured_output`, dynamic `complete_node` tool, `shell_command`, and `unstructured_output`. Global `[workflow].completion_mode` selects `auto`, `structured_output`, `tool`, `shell_command`, or `unstructured_output`; agent nodes can override it with the same values or inherit the global default.
- Start, join, and terminal nodes reject non-empty completion-mode overrides.
- Node completion-mode override is an agent-node execution property, not a transition-branch property. Edges define possible transition branches and parameter requirements; the source agent node owns the completion contract used to choose among them.
- `auto` resolves when an executable current Node starts or resumes after Session planning and tool availability are known: shell-unavailable agent execution uses `unstructured_output`; Workflows with any literal `continue_session` branch use `shell_command`; all other agent execution uses structured output when provider capabilities support it and dynamic tool mode otherwise. `compact_and_continue_session` does not trigger shell fallback. A Node-level `auto` override applies this policy even when the global config is a fixed mode.
- Resume resolves the latest completion mode and Runtime Parameter Contract from the current Workflow definition. A live Exact Execution Scope keeps the completion contract already advertised for that scope until it stops.
- Forced `structured_output` fails fast with an actionable error when unsupported. Forced `tool` always uses dynamic tool mode. Forced `shell_command` fails execution start when the resolved runtime shell tool is unavailable.
- Model generation turns whose effective completion mode is `shell_command` or `tool` require one or more calls from the complete effective advertised-tool set. The set includes local/custom tools and enabled provider-hosted tools; requiring a call does not remove or reorder declarations.
- Accepted live user steering re-enters the same live completion policy. `structured_output` and `unstructured_output` generation use automatic tool selection.
- Manual interruption releases the specialized workflow execution. A later ordinary interactive activation uses automatic tool selection; Resume resolves the latest Workflow completion policy.
- `complete_node` is workflow-control infrastructure and is available in tool completion mode regardless of subagent role tool config.
- `shell_command` mode keeps dynamic completion contracts out of request metadata and instructs the agent to run `kent task complete` from the shell. The command infers the Task and current Node from `KENT_SESSION_ID` in agent Sessions; outside agent Sessions it requires `--force` plus an unambiguous Task, Session, or current Node selector.
- Agent-Session `kent task complete` submits through Runtime Command. Forced completion outside an agent Session addresses one unambiguous current idle executable Node selected by Workflow Execution; it does not establish a durable execution selector or polling loop.
- `unstructured_output` mode keeps dynamic completion contracts out of request metadata and requires the assistant final answer to be exactly one raw JSON object.
- Any assistant answer that would otherwise complete an active workflow-controlled Node must pass through that Node's current completion contract in every completion mode, whether or not the answer carries an explicit final-phase designation.
- Normal assistant final answers are invalid in tool and shell-command workflow modes. Runtime appends a nudge and continues until valid completion, `ask_question`, interruption, protocol cap, or runtime error.
- Completion payloads expose only optional `transition`, optional `commentary`, and server-derived possible provision fields as top-level properties. They never expose raw `next_node`.
- Provision field outputs are flat strings. Completion payload parsers accept any JSON value for a provision field and serialize non-string values into that flat string slot; downstream input bindings never receive structured values.
- Possible provision fields are optional in generated request metadata where a mode uses request metadata. Selected transition groups impose required provision fields after `transition` is known.
- Required provision fields must be present as trimmed non-empty strings after parser stringification.
- Size limits: output field name `<= 64` chars, output field description `<= 1000`, output value `<= 64 KiB`, commentary `<= 64 KiB`, task comment body `<= 256 KiB`.
- Dynamic request metadata in `structured_output` and `tool` modes can affect prompt-cache continuity when workflow completion contracts change. `shell_command` and `unstructured_output` keep completion contracts in appended prompt text instead of request metadata.
- Workflow Execution accepts one actor-neutral completion intent for an Exact Execution Scope or one unambiguous current idle executable Node. Agent-originated intents are ordered through Runtime Command; script intents address their Exact Execution Scope directly.
- Accepted steering before the completion fence supersedes that intent. Input after the fence is rejected with a typed retryable result and is not transferred to a successor.
- Runtime enforces one protocol cap. Repeated final answers in invalid modes or invalid completion attempts interrupt the current Node after `[workflow].max_invalid_completion_attempts = 5`.
- Every workflow execution-loop exit represents successful completion, a typed resumable blocked state, or a typed non-success terminal outcome. Exact-scope stop, finalization, and release are joined; the loop cannot return while its scope remains live.
- No wall-clock runtime cap is required for v1.

## Script Nodes

- Script nodes are first-class executable workflow nodes. They can be Start targets, fan-out branches, join predecessors/successors, manual automation targets, and board columns anywhere agent nodes are accepted by graph semantics.
- Script nodes store nullable `script_path`. Missing, nonexistent, directory, and non-executable paths do not block graph save or node add/update; they block execution validation, Task Start, or target script execution as appropriate.
- Relative script paths resolve against the task execution root. Absolute paths resolve on the Kent server host.
- Script execution directly `exec`s the resolved file with the task execution root as cwd. It does not use a shell wrapper, retries, or a timeout.
- Script stdin is one JSON object. Incoming Workflow Parameter values are top-level properties. `_kent` is reserved for minimal Task, Node, and parallel Transition Branch identity; Run and Node Placement IDs do not exist.
- Script stdout is parsed as the workflow completion JSON using the same completion contract as agent nodes. Stderr is diagnostics only and is not mixed into completion parsing.
- Invalid stdout, invalid script path, interruption, and execution errors leave the script's current Node interrupted with bounded structured details.
- Script completion applies its selected Transition with actor `script`; it does not create retained execution history.
- Resuming an interrupted Script Node executes the script again with its current materialized inputs, the current Workflow `script_path`, and the latest outgoing completion contract.

## Workflow Prompting

- Workflow-controlled agent Sessions use dedicated workflow-mode developer instructions.
- Prompt explains task identity, node role/assignee, selected completion behavior, question behavior, handoff/transition mechanics, task comments, and why ordinary final answers are invalid when the selected mode does not accept them.
- Workflow runtime builds on reusable headless/session infrastructure for session launch, runtime wiring, logging, progress, subagent role handling, and mode prompts.
- Workflow Execution-created agent sessions begin at subagent depth `0`. Model-originated delegation from a workflow agent follows the global subagent-depth policy.
- `RunPromptService.RunPrompt` final text is not workflow completion authority.
- Existing user goal state is not reused as workflow autonomy state.
- Workflow Task Sessions reject user `/goal` control; the current workflow Node is the Task objective driver. Agents may still set themselves Goals and complete them, per the agent Goal rules in core-runtime-tools.
- Client input accepted by Runtime Command before the Completion Fence supersedes pending completion. Input that reaches the server after the fence is rejected with a typed retryable result, remains unapplied, and is never transferred to a successor current Node or Session execution.
- Task comment bodies are not automatically injected into agent context. When a task has visible comments, workflow-mode instructions include the visible comment count and a `kent task comment list <task>` pull command. Kent re-queries the visible comment count each time the workflow instructions are appended without mutating previously persisted model-visible prompt items.

## Questions And Approvals

- User questions use existing `ask_question` tool-call/session infrastructure.
- A model does not report `needs_user_input` as a completion status; it calls `ask_question`.
- The Session's current executable Node pauses until the Question is answered.
- TUI and GUI prompt/approval state is derived from shared server prompt state. A client marks a question or approval resolved only after server acknowledgement.
- V1 must not introduce a shadow task-question table. If existing ask persistence cannot support workflow asks, upgrade ask persistence as source of truth.
- Runtime Question and runtime Approval waiters exist only inside the Exact Execution Scope. Server restart does not reconstruct them; the affected current Node becomes interrupted, and Resume closes stale active-segment tool operations with a typed restart outcome before continuing.
- A pending Workflow Transition Approval is durable current Task state rather than a live runtime waiter. Server restart leaves it pending with the exact frozen Transition snapshot shown to the operator.
- Edge approval is a boolean edge property.
- When any edge in a selected Transition group requires Approval, the whole group waits for one Approval before any target Nodes become current or execute.
- Pending approvals store resolved transition group, edge set, workflow version, source node snapshot, transition display snapshot, target node snapshots, effective edge config snapshots, and frozen context-source resolution.
- Later graph edits do not change what a user approves.
- Applied and rejected Transitions are not retained as workflow movement history. Pending Approval state is removed when the Approval applies or a manual move supersedes it.
- A Task awaiting Approval remains at the source current Node and exposes `waiting_approval` status; target Nodes are not current yet.
- Manually moving a Task that is awaiting Approval clears the proposed Transition and replaces the source current Node with the chosen target.
- Missing-edge manual overrides cannot target executable Nodes. Manual movement into an agent or Script Node requires a concrete Workflow Edge so the target has a real prompt and completion contract.
- Task start and manual movement into an executable node apply no movement or scheduling when target selection is required. A valid selection retries and applies the original action once; dismissal leaves it unchanged.
- Approvals occur only after a task has reached an executable node and therefore always reuse the task's locked execution target.

## Context Preservation And Bindings

- Per-edge context preservation supports `new_session`, `continue_session`, and `compact_and_continue_session`.
- Workflow-created clones follow the shared derived-session provenance contract, so cloning does not reset delegation depth.
- Continuation modes may select `immediate_source`, `node:<node_key>`, `previous_target`, or `previous_target_or_new` as context source.
- `immediate_source` uses the Session bound to the source current Node.
- `node:<node_key>` selects the latest retained Session associated with the guaranteed-prior agent Node.
- `previous_target` selects the latest retained Session associated with the target agent Node and fails when none exists.
- `previous_target_or_new` selects that Session when one exists and otherwise starts a new Session.
- While the source current Node belongs to active parallel work, every Context Source selection is additionally scoped to that current Node's Transition Branch Key. Kent does not introduce a parallel-batch entity or ID.
- Manual movement through a concrete edge supports `previous_target` and `previous_target_or_new`, resolves the context source when the move is applied, and freezes that resolution before pending approval. Selected prior-node context sources remain unsupported for manual movement.
- Pending Approvals freeze context-source resolution before Approval. A fallback-to-new result remains `new_session` even if another matching Session appears before Approval, and a selected Session remains fixed if a newer matching Session appears.
- Continuation modes apply the target node's subagent role context. `continue_session` preserves the reused session's contract generation. `compact_and_continue_session` compacts the reused session and establishes a fresh target-node contract generation, including model/provider setup, generation parameters, capabilities, enabled tools, native web-search mode, prompt snapshots, context budget, and cache lineage.
- `new_session` uses current role config at its fresh context boundary.
- Consuming agent nodes own required inputs as named top-level string fields with descriptions.
- Prompt placeholders validate against the consuming node's required inputs through `.Inputs.<name>`.
- Prompt templates may reference guaranteed-prior agent node outputs through `.Nodes.<node_key>.<output_name>`.
- `.Nodes` references use stable node keys and declared source-node output fields. The referenced source node must dominate the consuming node in the workflow graph, the source node must not be the consuming node, and unsupported dynamic template access to `.Inputs` or `.Nodes` is invalid.
- Applying a Transition materializes every value needed by each target current Node into that Node's current inputs. Prompt rendering reads those inputs and never performs a historical workflow-execution lookup.
- A Workflow edit that makes an executable current Node require input that was never materialized blocks Start or Resume with a typed validation error; Kent does not reconstruct discarded workflow history.
- The first executable node reached from `start` cannot declare upstream inputs and should use task fields such as `.TaskTitle` and `.TaskBody`.
- Source-Node output fields declare values that the Workflow graph can propagate into later current-Node inputs through `.Nodes.<node_key>.<output_name>`.
- Edge input bindings and edge output requirements are not canonical workflow-editing concepts.
- The server derives provision fields, same-name input bindings, selected-transition output requirements, and possible completion fields from node required inputs, prompt node-output references, graph topology, and join provider selections.

## Parallelism And Joins

- Transition groups model fan-out. Multiple edges in one group add parallel target Nodes to the Task's Current Nodes.
- Branches are ordinary workflow nodes, not subtasks.
- GUI-authored node groups are saved only as execution-shaped parallel groups. A node group contains branch nodes and one join; the fan-out remains canonical workflow graph structure through one transition group with multiple edges.
- A Task may have several Current Nodes only when the graph explicitly fans out.
- Parallel joins always wait for all required inputs in v1. Racing/first-success semantics are out of scope.
- Fan-out topology must have exactly one unambiguous nearest common join reachable from every branch.
- Branch paths before that join may not terminate, enter nested fan-out, or contain cycles.
- Ambiguous/complex fan-out is rejected in v1.
- Current fan-out state retains the expected Transition Branch Keys and materialized branch inputs only until the Join completes.
- Later graph edits do not change an in-flight fan-out's expected branch set.
- Join nodes are non-agent fan-in points that aggregate inbound output values into deterministic results then follow their outgoing transition group.
- Agent synthesis belongs in a normal agent node after the join.
- Orchestrator-workers do not dynamically create workflow nodes or Kanban columns in v1.

## Workflow Execution And Restart

- Workflow Execution is the sole workflow lifecycle control plane. It owns one context-aware global mutation permit, volatile automatic intents, branch admission, configured automatic capacity, task affinity, runtime gates, and immutable live snapshots.
- Automatic intents form one small typed in-memory queue. Membership and order are intentionally lost on restart; no durable claim, queue journal, reconstruction scan, recovery worker, or startup runnable derivation exists.
- Automatic scheduling is work-conserving and task-affine. `[workflow].concurrency` limits automatic starts only.
- Explicit Start, Resume, approval, and executable manual move may exceed the automatic concurrency limit without preempting existing work.
- A live agent or script Exact Execution Scope is the only execution-liveness authority. Durable current-Node state, Session relations, Task status, transcript rows, and Goals do not prove execution.
- Start and Resume admit selected parallel branches independently and return typed per-current-Node outcomes. A failed branch does not roll back, compensate, or block an independently admitted sibling.
- Resume starts a fresh Exact Execution Scope only after the previous scope has fully stopped. Steering remains within the current scope.
- Runtime Question and Approval waiters, Automatic Intents, runtime gates, and Exact Execution Scopes are volatile. Restart reconstructs none of them and marks affected current executable Nodes interrupted with a typed restart reason.
- Persisted workflow transition approvals remain pending across restart with their frozen transition snapshots.
- Resume closes stale bounded active-segment tool operations with a typed restart outcome before continuing. It does not reconstruct waiters, replay answers, re-execute interrupted tool calls, or traverse full transcript history.
- Interrupted current Nodes are never automatically retried.
- Task Interrupt is Task-level with an optional Session-ID selector: no Session interrupts every Exact Execution Scope of the Task; a specific Session interrupts only its agent execution. Script execution is interrupted through the Task-wide operation.
- Manual moves are rejected while a task has an exact live execution or runtime gate that conflicts with movement. The operator must Interrupt or wait for the lifecycle transition to finish.
- Completion and Transition application require either the matching Exact Execution Scope or one unambiguous current idle executable Node selected by Workflow Execution. A stopped scope and a Node that is no longer current cannot mutate Task state.
- Completion and Current Nodes replacement remain one SQLite transaction.
- Runtime failures, crashes, model/runtime interruptions, and fixable admission validation blockers leave the affected current Node interrupted with reason metadata.
- `failed` is reserved for unrecoverable corrupted orchestration state.
- Kent retains no completed execution records to migrate, reconcile, diagnose, or repair.

## Task Status And Listing

- Task detail, workflow board cards, and paginated task lists use one server-authoritative typed Task-status projection derived from Current Nodes plus one Immutable Live Snapshot.
- Workflow attention has two read surfaces: a global paginated Inbox feed and a bounded non-paginated task feed. There is no project-scoped attention feed.
- Core task detail does not embed attention items. It retains an unresolved-attention count for task-show output, and that count plus the rest of core task detail are database-backed without transcript reads.
- Task attention may read the newest active transcript segment to recover unresolved question content. Desktop task detail starts this read independently in parallel so core detail is not blocked.
- Task status is UI-neutral structured data. Clients render and localize status labels.
- One primary status uses this precedence: done, live question, live or persisted workflow approval, running, queued, interrupted, backlog, active.
- Running, queued, and live-Question status require matching Exact Execution Scope evidence. Interruption metadata on a current Node never proves liveness.
- Task read models expose server-authoritative `can_delete` derived from the same snapshot. The Delete mutation treats it as advisory and revalidates quiescence before changing artifacts or persistence.
- The status projection preserves all applicable typed attention kinds and Session/current-Node references when parallel branches have different conditions.
- Workflow validity is workflow-level state and is not a task status.
- Task lists expose typed Task status and attention filters. They expose no separate execution status or execution-count concept.
- Task lists are project-scoped. The CLI defaults to the project attached to the current workspace, including when `--workflow` is supplied.
- A project-only task list spans every workflow linked to that project. An explicit workflow selector narrows the list and must identify an active link in that project.
- CLI `--status` filters typed Task status and `--attention` filters attention kinds across project-wide results. Created, updated, status, and title sorting are Workflow-neutral.
- `--column` and column sorting require an explicit workflow because node keys and column positions are workflow-relative.
- Project-wide human Task rows omit column output, and project-wide JSON Task items omit `column_keys`. Workflow-narrowed lists expose all Current Node keys in board order.
- The first project-wide task-list page derives a filtered matching-workflow cardinality of none, one, or multiple and freezes that first-page display decision in continuation tokens. Task membership remains live, so concurrent mutations may make workflow-name visibility stale on later pages.
- Human task-list rows include workflow names only when the filtered query can return tasks from multiple workflows. JSON task items always include their bare workflow UUID.
- A project with no linked workflows, an explicitly selected workflow that is not linked to the project, and workflow-relative operations without a workflow selector return distinct typed actionable errors.
- No-linked-workflow recovery directs the operator to create and link a workflow or list and link an existing workflow before retrying.
- Explicit not-linked recovery offers project workflow discovery and retry with a linked workflow, or linking the selected workflow to the project.
- Workflow-relative column recovery offers project workflow discovery and a task-list retry with an explicit workflow while preserving the parsed filters.
- Task-list status sorting follows primary typed-status precedence; workflow-narrowed column sorting follows workflow column position.

## Execution Targets And Worktrees

- A workflow execution target policy is evaluated only when an unlocked task first reaches an executable node through task start or manual movement.
- No managed worktree uses the source workspace as the execution root, supports non-Git workspaces, and creates no branch or worktree and runs no worktree setup.
- A no-managed-worktree target follows the task's current source workspace. Changing that workspace intentionally changes later execution roots.
- Source `HEAD`, repository default branch, and custom Git ref resolve server-side to an immutable commit before managed-worktree creation. Custom ref accepts any Git revision that resolves to a commit.
- Repository default branch uses configured local remote-HEAD metadata: `origin` when configured, otherwise one unambiguous configured remote HEAD. Kent does not contact remotes or guess branch names; missing or ambiguous metadata makes the configured target unavailable.
- `ask_on_first_execution` and an unavailable configured target use the same task-local selection flow. They offer no managed worktree, source `HEAD`, repository default branch, and custom Git ref.
- For `ask_on_first_execution`, repository default branch is preselected. For an unavailable configured target, the configured mode and custom-ref input remain selected when useful; otherwise repository default branch is preselected.
- An unresolvable configured target asks the operator to select a concrete target and explains which configured target failed and why.
- Selection-required results distinguish exactly two interaction reasons: workflow policy requires selection or configured target is unavailable. Every selection flow offers all four concrete modes; the wire contract does not carry a dynamic allowed-mode list.
- Failure to resolve an explicitly selected custom ref is a validation failure. It does not recursively request selection or fall back to another target.
- A Task locks target-selection provenance only when the initiating action successfully reaches its first executable current Node. Later Nodes and retries reuse the locked mode and managed requested/resolved facts despite Workflow edits or Git ref movement.
- Every pre-upgrade Task with a recorded managed worktree and usable recorded HEAD metadata continues using that worktree after upgrade. Its observed commit is identified as legacy provenance and is not presented as a known original branch point.
- Pre-upgrade tasks without a managed worktree remain unlocked and inherit their migrated workflow's source-`HEAD` policy.
- Managed targets use the existing task-worktree creation, setup, and collision behavior. Before the first executable current Node is scheduled, Kent loads worktree setup settings from the Task's source workspace; a configured setup script must succeed for the newly created candidate in that request.
- Managed worktree setup failure leaves the initiating action unapplied and unscheduled. Any created worktree remains available for inspection or manual repair.
- Setup runs only when the current request creates or recreates a worktree root. A later retry trusts an already-existing compatible root and does not rerun setup; no durable setup-readiness state exists.
- Setup receives the source workspace root, branch name, and managed worktree root as stable positional inputs.
- Workflow task setup has no session identity: its structured payload represents the session as null and its session environment input is absent. Session-originated setup supplies the requesting session's real identity in both inputs.
- Kent-provided setup inputs are authoritative. Conflicting inherited process values cannot provide or override Kent-reserved setup inputs.
- A managed target remains tied to its original source workspace, but its current root, metadata binding, branch history, and named branch may change.
- Before execution Kent validates that the bound root is the exact worktree root for the source repository. Initial managed-worktree creation and conservative repair establish a named branch; an available locked worktree remains valid at either a named branch or detached `HEAD` for resume and subsequent workflow execution. Kent never compares current history or HEAD with the originally resolved commit.
- When a locked managed root or metadata binding is missing, the initiating action or workflow runner may synchronously invoke the single worktree materializer to conservatively restore an existing named branch at a collision-safe managed root, persist the relation, and run setup for the recreated root.
- Conservative repair never recreates a missing branch from the old base commit, overwrites an existing directory, resets or renames a branch, accepts detached HEAD, repairs another repository, or infers ownership by scanning arbitrary roots. Unsafe or ambiguous states return one typed locked-target error with a small product-level cause.
- There is no target-replacement flow. A locked target is never converted to no managed worktree.
- Task-detail read models always expose the source workspace. After lock they expose durable target provenance plus the recorded managed-worktree path when present. They do not inspect live path availability or the current Git branch: branch discovery is an expensive worktree-owned operation and would duplicate worktree metadata on the hot task-detail read. Desktop task detail presents source identity and path in the Source workspace row and the recorded managed root in the Managed worktree row.
- Human task detail shortens the resolved commit for readability. Structured JSON retains the full commit value.
- Initial managed worktree creation uses the task short ID as the branch name.
- Worktree creation reuses existing worktree branch/root collision handling.
- Worktree deletion/retargeting treats non-terminal tasks referencing a managed worktree as blockers.
- Worktree deletion blocks if another Session targeting the worktree has a live execution, and holds an execution exclusion on all targeting Sessions across the `git` removal so no new execution can start mid-deletion; work submitted during the window is rejected with `ErrSessionWorktreeDeleting` until the exclusion releases.
- Initial materialization and conservative locked-target repair reuse one managed-worktree creation/setup implementation.
- The CLI task-start, task-approve, and task-move commands may select a concrete target for an unlocked task even when the workflow has a fixed policy. Task creation has no target override.
- CLI target selection uses `--execution-target none|head|default-branch|ref:<revision>`; custom Git revisions require the explicit `ref:` namespace.
- CLI task start, approve, and move never prompt interactively. Selection-required output identifies the reason and concrete rerun flags. Task start exposes the same typed outcome in JSON.

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
- Comment rows do not store workflow execution links, deleted tombstones, or opaque metadata.
- Include-deleted comment APIs and read-model state are not product scope.

## Persistence And Schema

- Use SQLite for structured workflow/task state. Keep transcripts and large session artifacts file-backed.
- Production metadata SQL is declared in `server/metadata/queries.sql` and consumed through generated `server/metadata/sqlitegen` APIs. Transaction-scoped SQLite lifecycle SQL that sqlc cannot emit is declared in `server/metadata/lifecycle.sql` and consumed through generated `server/metadata/sqlitelifecyclegen` APIs. Workflow packages do not embed private SQL files or execute raw SQL strings directly.
- Workflow implementation package boundaries are locked:
- `server/workflow`: pure domain types, validation, state-machine logic.
- `server/workflowstore`: metadata persistence adapter.
- `server/workflowsvc`: client-facing workflow use-case adapter.
- `server/workflowexecution`: lifecycle mutations, volatile automatic intent, admission, runtime gates, and immutable live snapshots.
- `server/workflowruntime`: completion/runtime contracts used by runtime.
- `server/workflowrunner`: agent/script exact-scope adapter used by Workflow Execution.
- `server/workflowview`: read models.
- Start node is derived from `workflow_nodes.kind = 'start'` and enforced with a partial unique index; do not store `workflows.start_node_id`.
- Workflow graph storage derives membership from relationships instead of duplicate workflow IDs where practical.
- Workflow definitions do not persist opaque `metadata_json` on workflows, nodes, node groups, transition groups, or edges.
- A Task's Current Nodes are stored as a task-owned collection keyed by Workflow Node and Transition Branch identity where parallel work requires it. Current-node records have no independent product/entity IDs.
- An executable current Node stores only current scheduling/interruption state, materialized inputs, and its optional Session association. Script Nodes have no Session association.
- Applying a Transition atomically replaces source Current Nodes with target Current Nodes and materializes target inputs. Applied/rejected Transition rows, completed current-Node rows, and execution-attempt rows are not retained.
- Pending Approvals are the only retained Transition snapshots. They are deleted when applied or superseded.
- Sessions retained by a Task store the Workflow Node association and optional active-parallel Transition Branch Key required by Context Source selection.
- Project workflow links are active membership rows only. Do not soft-unlink.
- Unlink hard-deletes unused links. If tasks exist, user must move/delete tasks before unlinking.
- Blocked unlink returns typed blockers with counts/references.
- Retiring a workflow means deleting the workflow definition and cascading/deleting tasks through explicit workflow deletion.
- `tasks.project_workflow_link_id` is the source of truth for task project/workflow pairing.
- Direct duplicated `tasks.project_id` and `tasks.workflow_id` columns are removed with a hard cutover.
- Project default pointers use `projects.default_project_workflow_link_id` and `projects.primary_workspace_id`, each constrained to rows owned by the same project.
- Workspace/worktree labels, availability, primary/default status, and main-worktree status are read-model facts derived from canonical roots/pointers.
- Workflow invalidation events are process-local live signals, not durable/replayable sequence state. SQLite does not store `workflow_events`.
- Workflow Project events use typed resource and action enums in the shared contract. Label catalog and task-label changes reuse the existing Project event broker/subscription path; there is no parallel label event channel.
- GUI clients refetch read models after subscription ACK/reconnect/error and treat live events as invalidation hints.
- There is no product archive lifecycle for workflows or nodes.
- Workflow deletion impact previews return counts only.
- Confirmed workflow deletion requires aggregate quiescence across every affected task: no exact live execution, automatic intent, or runtime gate. It also blocks default-without-replacement states.
- After quiescence, workflow deletion atomically removes DB workflow-linked tasks, links, and graph rows.
- Workflow deletion is a distinct DB-only aggregate operation that preserves session, worktree, and other external artifacts. It does not invoke individual Task Delete.
- Individual Task Delete requires no exact live execution, automatic intent, or runtime gate; removes reconstructible managed artifacts idempotently; preserves session artifacts; deletes the task row last; and has no delete journal.
- Batch graph save uses a store-owned transaction with expected workflow `version`, draft validation, process-local edit semantics, typed blockers, and confirmation for unreferenced graph row removals.
- Graph saves never delete or move tasks; whole-workflow deletion is the task-deleting path.
- Current executable-Node context has one store-owned materialization seam. It combines the Task, latest Workflow definition, current Node inputs, execution target, and selected Context Source Session without historical execution lookup.

## Schema Minimization Decisions

- Approved cutover removals include `task_runs`, `task_node_placements`, applied/rejected task-transition history, Run and Node Placement IDs in contracts, Run counts/sorting/selectors, `workflow_events`, `project_workflow_links.unlinked_at_unix_ms`, duplicated task project/workflow columns, workflow graph opaque metadata, the `runtime_leases` table, workspace/worktree display labels, workflow execution links on comments, comment soft-delete, and redundant indexes when equivalent unique/leading-key indexes remain.
- Removing Task Cancel migrates canceled tasks to canonical terminal node key `done` when possible. Canceled tasks in invalid workflows without `done` lose workflow persistence while session, worktree, and other external artifacts remain.
- Keep `tasks.source_url` as a structured task field.
- Keep `tasks.short_id` as stored durable product data.
- Task sequence allocation is transactional behavior, not product state stored as `projects.next_task_seq`.
- Current executable-Node inputs and Context Source Session associations use typed Task-owned state rather than opaque metadata.
- Context Source selection uses retained Session associations directly; no source-execution identity exists.
- Pending Approval snapshots own the exact branch invocation facts the operator is approving. Ordinary applied work uses the latest Workflow definition and current materialized inputs.
- Keep `task_comments.author_id`; future multi-agent/user identity display depends on it.
- Keep `sessions.first_prompt_preview` as stored listing/read-model data.
- Keep `sessions.input_draft` as stored unsent prompt recovery data.

## CLI Surface

- Minimal workflow/task CLI exists to exercise backend behavior and teach agents task usage.
- Agents must be able to build and edit complete workflow definitions through CLI commands; command grouping and syntax are not stable product contracts.
- High-level workflow mutation subcommands are the complete agent editing path; workflow import/export is a separate sharing feature, not the primary edit interface.
- High-level workflow mutation commands use a CLI-local draft-edit module, then persist through batch graph save. The server does not expose row-level or semantic edit RPC routes for workflow graph mutation. Extract the draft-edit module only when a second Go caller exists.
- Row-level workflow graph RPC methods, client methods, protocol constants, and route entries are removed in the graph-save cutover instead of preserved as migration stubs.
- CLI output must include stable IDs needed by later commands. The plain-text `kent task complete` handoff acknowledgement is exempt and omits task, run, transition, node, and other stable IDs; JSON completion output remains machine-readable.
- `kent task list` exposes one typed task status. `--status` filters primary status, `--attention` filters typed attention, and `--column` filters workflow node keys.
- `kent task list` filters and sorts before pagination through server-owned structured request fields. Multiple values for one filter are ORed; different filter types are ANDed. Tasks with several Current Nodes expose all matching column keys in Workflow order.
- `kent task list` default ordering is `status:asc,updated:desc`, where `status` uses primary typed-status precedence and `updated` is newest-first. Custom `--sort` accepts ordered `field:direction` selectors for `created`, `updated`, `status`, `column`, and `title`; selectors can be comma-separated in one flag and may be supplied by repeated flags.
- `kent task complete` accepts dynamic parameter flags, repeatable `--param name=value`, and `--json`/`--json-file` completion payload input. JSON input modes print JSON responses.
- Plain-text `kent task complete` output is a model-facing handoff acknowledgement: `Completion scheduled. The transition <source display name> → <destination display name> will execute now. Your next agent turn will begin with the next workflow instructions.`
- The acknowledgement uses the target node display name for an ordinary transition. A fan-out uses its shared target node-group display name when present and otherwise its transition display name.
- The same acknowledgement is used for agent-session and forced human completion. It always promises a next agent turn regardless of context-preservation mode or whether another turn occurs, and it does not expose approval or transition state.
- JSON completion output keeps its existing field set and does not include the plain-text handoff facts.
- `kent task edit <task>` mutates an existing task's title, body, and source workspace through `UpdateWorkflowTask`. It requires at least one of `--title`/`--body`/`--body-file`/`--source-workspace`, reuses the current title when `--title` is omitted, and is available to agents like `task create` (no human-only gate). `--json` prints the update response.
- `kent task create` and `kent task edit` accept `--source-workspace` as either a workspace id or a path; a path is resolved through its project binding. An omitted source workspace leaves it unchanged on edit.
- Workflow/task CLI commands report remote-close failures to stderr after command work finishes. A close failure does not change a successful exit code, and an operation failure keeps its existing nonzero exit code.
- Unsupported commands may fail loudly before backend semantics land rather than implementing partial behavior.

## Q/A Decisions Preserved

- Q: Should workflow definitions use a stable graph file format in v1? A: No; SQLite/API/CLI are authoritative for v1.
- Q: Is task creation the same as starting automation? A: No; creation makes a backlog task, and task-start is explicit.
- Q: Is completion mode per Workflow/Node? A: A global `[workflow].completion_mode` config provides the default, agent Nodes may override it, and execution resolves the latest effective mode on Start or Resume.
- Q: Should workflow-controlled execution have a wall-clock cap? A: No v1 wall-clock cap.
- Q: Should v1 auto-retry interrupted or runtime-failed current Nodes? A: No; human Resume is required.
- Q: Are racing/first-success parallel branches in scope? A: No; joins wait for all required inputs.
- Q: Can orchestrator-workers dynamically create workflow nodes/columns? A: No in v1.
- Q: Should pending workflow questions get a task-question shadow table? A: No; use `ask_question` source of truth or upgrade ask persistence.
- Q: Does Task Cancel exist? A: No; Interrupt is the resumable stop operation, and Delete removes a quiescent task.
- Q: Does real-provider workflow QA need explicit approval? A: Yes, ask the User before spending provider credits.
- Q: How do agents complete shell-command workflow work? A: They run `kent task complete` from a shell command; `KENT_SESSION_ID` identifies their Task and current Node.
- Q: Must low-level workflow CLI command shape stay stable? A: No; full workflow build/edit capability for agents matters, not the specific command grouping.
- Q: Should full workflow graph files be the primary agent editing interface? A: No; agents edit through high-level CLI mutation commands, while import/export is a separate sharing feature.
- Q: Where should high-level workflow edit intelligence live? A: Start with a CLI-local draft-edit module; extract it only when a second Go caller exists. The server persists graph edits only through batch graph save.
- Q: Should row-level workflow graph RPC methods remain as migration stubs? A: No; remove the protocol methods, clients, routes, service methods, and tests for that external seam.
- Q: Should `tasks.short_id` be stored or derived from `project_key + task_seq`? A: Keep it stored as durable product data.
- Q: Should `projects.next_task_seq` stay stored? A: No; replace it with transactional task sequence allocation.
- Q: Should Run or Node Placement persistence remain? A: No; Tasks own only Current Nodes and pending Approval state, while Sessions remain durable artifacts.
- Q: Should Context Source selection persist execution IDs? A: No; select retained Sessions by their Task and Workflow Node association.
- Q: Where do frozen branch invocation facts live? A: Only pending Approval snapshots retain the exact facts being approved; ordinary execution uses current materialized inputs and the latest Workflow definition.
- Q: Should `task_comments.author_id` stay? A: Yes; keep it for future identity display.
- Q: Should applied Transition snapshots stay? A: No; retain snapshots only while an Approval is pending.
- Q: Should `sessions.first_prompt_preview` stay stored? A: Yes.
- Q: Should `sessions.input_draft` stay stored? A: Yes.
