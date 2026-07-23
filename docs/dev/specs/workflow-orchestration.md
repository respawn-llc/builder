# Workflow Orchestration Spec

## Purpose And Scope

- Workflow orchestration lets users define reusable, Project-scoped processes for Tasks.
- A Workflow contains Nodes and Transitions.
- Tasks can move through agent work, scripts, review loops, parallel branches, Joins, and terminal states.
- Every Kent client presents the same authoritative Workflow and Task behavior.
- Every incompatible Workflow contract cutover increments the client/server protocol version. Incompatible clients fail with a clear compatibility error, and Kent does not emulate an older Workflow contract.
- The CLI provides complete Workflow and Task control for operators and agents.

## Domain Model

- `Task` is the primary durable work item. Sessions are retained artifacts associated with agent Nodes on the Task.
- A Task directly owns its Current Nodes. It usually has one and may have several only while a fan-out executes parallel branches.
- Current Nodes have no independent entity IDs. Parallel state uses the existing Transition Branch Keys needed by the fan-out and Join.
- An executable current Node owns only its current execution state and optional Session binding. Script Nodes have no Session.
- Leaving a Node removes that current execution state. Kent does not retain completed Node execution, execution-attempt, or workflow-movement records as hidden history.
- Task creation creates a durable Task at the Workflow's Start Node.
- Automation starts only through explicit Task Start, which applies the Start Node's outgoing Transition and adds the first executable current Node.
- Automation continues through automatic Nodes until terminal or blocked by a Question, Approval/manual gate, error, capacity, interruption, or validation.
- Task status combines Current Nodes with current live activity. Kent does not store a second lifecycle status that can disagree with them.
- Running, queued, and waiting require matching Exact Execution Scope evidence. A current Terminal Node makes the Task done.
- A Project owns one shared Label catalog for every linked Workflow board. Tasks can use only Labels from their Project.
- Labels are many-to-many organizational metadata on tasks. They never affect workflow state, scheduling, prompts, task status, or execution.

## Task Labels And Filtering

- Each label has an immutable UUID v4 identity. Its mutable name is trimmed, 1–64 characters, preserves display capitalization, and permits Unicode letters and numbers, spaces, and `: & * % $ # @ ! ? . , / \ + | - _ ~ '`.
- Label names are case-insensitively unique within a Project. Capitalization-only rename is allowed without changing identity or task assignments.
- A Project can have at most 100 Labels. Clients load the complete catalog without pagination.
- A task may use any subset of its Project's catalog; there is no separate per-task label limit.
- Label creation, rename, deletion, assignment, and removal remain available regardless of whether affected Tasks are Backlog, active, running, interrupted, done, or legacy canceled.
- Label changes do not change a Task's update time, reorder Tasks, or move pagination anchors.
- Task creation may atomically assign existing Project labels. Later assignment changes use idempotent add/remove semantics: adding an existing assignment or removing an absent assignment succeeds and returns the authoritative resulting label set.
- Renaming takes effect everywhere without changing assignments. Deletion requires confirmation and atomically removes the label from every task; the confirmation does not require an affected-task count. Desktop deletion uses explicit confirmation; invoking the explicit CLI delete command is sufficient confirmation and does not prompt or require a separate confirmation flag.
- Labels have no color or manual ordering. Label catalogs and assigned chips use case-insensitive alphabetical order. Kent does not sort Tasks by Label.
- Kent applies Label filters before pagination for Workflow boards and Task lists.
- OR matches tasks carrying at least one selected named label. AND matches tasks carrying every selected named label while allowing additional labels. One selected label behaves identically in both modes.
- `No labels` matches tasks with zero label assignments and is mutually exclusive with named-label selection. No selected labels means no label restriction.
- Kent combines the complete Label expression with every other active Task-list filter. Filtering preserves sorting and cursor behavior. A client never loads the complete board or Task list to apply filters.

## Workflow Definitions

- Workflow definitions are globally reusable and linked to projects. Projects do not copy graph definitions.
- Workflow validation uses Project context because available subagent roles and workspace configuration can differ by Project.
- Kent has no stable Workflow graph import or export format.
- Product surfaces and the CLI can create and edit Workflow definitions.
- Workflow definitions may be saved, linked, and made project default while semantic validation fails.
- A saved Workflow Draft must have valid identifiers, valid references, supported values, unique keys, and exactly one Start Node.
- Users can create Backlog Tasks for an invalid linked or default Workflow while they fix it.
- Task Start, Resume, and automatic scheduling of executable work validate the Workflow in Project context and report all safe, actionable issues. The `ask_question` rule for an Assignee is checked when executable work starts.
- A project can link multiple workflows and has one default workflow for task creation.
- Invalid default workflows are allowed. Task creation against an invalid default creates Backlog tasks, while starting/running those tasks fails with accumulated validation errors until the workflow is fixed.
- Every workflow has exactly one execution target policy: no managed worktree, source `HEAD`, repository default branch, custom Git ref, or operator selection when execution first begins.
- New Workflows default to `ask_on_first_execution`.
- A Workflow that predates Execution Target Policy uses source `HEAD` to preserve its established behavior.
- Custom-ref policy accepts one Git revision. Other policies clear and ignore that value.
- A draft may save custom-ref policy without a value and reports a semantic validation issue. Saving validates presence only; Git resolution is authoritative against the task's source workspace when execution first begins.
- Workflow creation auto-creates ordinary editable `backlog` and `done` nodes.
- Each changed save increments the Workflow Version once. A save that changes both details and graph structure also increments it once. A save that changes nothing does not increment it.
- Pending Approvals retain the Workflow Version and branch snapshots the operator is approving.
- Executable Current Nodes resolve the latest Workflow definition and Runtime Parameter Contract when they start or resume. Completed execution contracts are not retained as history.
- Script Nodes use the current script path and completion requirements each time they execute.

## CLI Workflow Selection And Discovery

- CLI Workflow selectors are bare canonical UUID v4 values. Workflow display names and prefixed identifiers are not selectors.
- Every CLI command that accepts a Workflow selector uses the same syntax and emits copyable bare UUIDs in human and JSON output.
- Long flags are rendered with double dashes in help, examples, and Kent-authored diagnostics. Single-dash long flags remain accepted for compatibility; standard-library parser failures retain their native formatting.
- `kent workflow list` is paginated. `--project <path-or-id>` filters the existing workflow list to workflows linked to the resolved project.
- Project-filtered Workflow discovery is paginated and does not validate each Workflow graph.
- Project-filtered workflow results are ordered with the project default first, then by project-local task activity, then by workflow name.
- Project-filtered human results preserve the global one-line format and append default/link status plus execution-target policy. JSON includes the resolved project ID once and adds only the default-link fact to each workflow record.
- `kent workflow inspect <uuid> --summary` returns only global workflow metadata: name, bare UUID, description, version, and execution-target policy. It does not accept project context; full inspect retains the workflow graph.
- `kent task show` does not accept a workflow selector. It reports the selected task's actual workflow with a bare UUID.
- Task creation without an explicit workflow uses the project default. If no default exists and exactly one workflow is linked, Kent uses that workflow.
- Task creation with no linked workflows explains that a workflow must be created or linked before retrying.
- Task creation with several linked workflows and no default does not enumerate candidates. It directs the operator to paginated project workflow discovery, an explicit `--workflow <uuid>` retry, or default selection.

## Nodes, Transitions, And Validation

- Nodes define Workflow states and executable behavior. Agent Nodes configure Assignee, completion mode, and worktree and Session policy. Script Nodes configure an executable path.
- Transitions define target Nodes, Approval, Context-Preservation Mode, Context Source, Parameters, routing, and Join behavior.
- The Assignee is the subagent role for an executable Node. There is no separate assignment.
- Workflow Nodes select existing subagent roles. They cannot override model, provider, tools, or authentication.
- Agent Nodes do not own invocation prompts. Each incoming Transition Branch owns its Transition Prompt. Kent provides no Node-level prompt fallback.
- Every Agent Node's effective Assignee, including default and built-in roles, must have `ask_question` enabled. Validation reports every affected Node and does not change role configuration. Workflow Drafts and Backlog Tasks remain allowed. Task Start, Resume, and manual movement to an executable target validate the latest Workflow before target selection, Task movement, Approval, or execution.
- Each visible executable or terminal Node is also a Kanban column and status. Join Nodes are omitted from boards.
- Workflows can contain Start, Agent, Script, Join, and Terminal Nodes. Approval is a Transition Branch property.
- Each Workflow has exactly one Start Node. It is non-executable and has no inputs.
- For Task Start, the Start Node must have exactly one outgoing Transition with exactly one branch that targets an executable Node.
- Terminal Nodes are strict sinks. Manual reopen or rework is a user override, not retained Workflow history.
- Draft validation reports semantic errors but does not block save/link/default selection.
- Task creation and execution validation accumulate all safe actionable errors and reject invalid graph/role/input configurations.
- Execution-valid graphs reject detached islands: every node reachable from start, every non-terminal can reach terminal, terminal cannot auto-run.
- Cycles/self-loops are allowed outside restricted fan-out branch paths.
- New draft Nodes, Node Groups, Transitions, and Transition Branches receive UUID v4 identifiers. Preview and Save preserve those identifiers unchanged. Product-facing keys remain stable semantic references.
- `node_key`, `transition_id`, `edge_key`, output field names, and binding names match `^[a-z][a-z0-9_]{0,63}$`.
- Workflow display names are labels, not references, and are trimmed non-empty strings capped at 120 chars.

## Node Completion

- Agent Nodes complete by producing a Transition Result, not by returning ordinary natural language.
- A Transition Result selects an outgoing Transition and supplies the Transition Parameters that its targets require.
- Runtime failure, unanswered questions, interruption, and validation blockers are orchestration outcomes, not model-selected terminal statuses.
- Completion modes are `structured_output`, dynamic `complete_node` tool, `shell_command`, and `unstructured_output`. Global `[workflow].completion_mode` selects `auto`, `structured_output`, `tool`, `shell_command`, or `unstructured_output`; agent nodes can override it with the same values or inherit the global default.
- Start, join, and terminal nodes reject non-empty completion-mode overrides.
- A completion-mode override belongs to the source Agent Node, not to a Transition Branch. Transitions define the possible branches and Parameter Requirements.
- `auto` resolves when an executable current Node starts or resumes after Session planning and tool availability are known: shell-unavailable agent execution uses `unstructured_output`; Workflows with any literal `continue_session` branch use `shell_command`; all other agent execution uses structured output when provider capabilities support it and dynamic tool mode otherwise. `compact_and_continue_session` does not trigger shell fallback. A Node-level `auto` override applies this policy even when the global config is a fixed mode.
- Resume resolves the latest completion mode and Runtime Parameter Contract from the current Workflow definition. A live Exact Execution Scope keeps the completion contract already advertised for that scope until it stops.
- Forced `structured_output` fails fast with an actionable error when unsupported. Forced `tool` always uses dynamic tool mode. Forced `shell_command` fails execution start when the resolved runtime shell tool is unavailable.
- `[workflow].use_required_tool_calls` defaults to `true`. When enabled, each model response in `shell_command` and `tool` modes must call at least one available tool. This requirement does not add, remove, or reorder tools.
- When `[workflow].use_required_tool_calls` is `false`, model requests in `shell_command` and `tool` modes use automatic tool selection. The selected completion mode still rejects ordinary assistant final answers.
- Accepted live user steering re-enters the same live completion policy. `structured_output` and `unstructured_output` generation use automatic tool selection.
- Manual interruption releases the specialized Exact Execution Scope.
- If the retained Workflow Session still belongs to the interrupted Current Node, a later ordinary interactive activation uses automatic tool selection and remains eligible to complete that Current Node.
- Kent resolves workflow-started and ordinary interactive completion from that retained Session to the same current Run. The interactive activation does not create a second Transition authority.
- Resume starts a fresh Exact Execution Scope and resolves the latest Workflow completion policy.
- `complete_node` is always available in tool completion mode, regardless of the Assignee's configured tools.
- `shell_command` mode instructs the agent to run `kent task complete`. In an agent Session, `KENT_SESSION_ID` identifies the Task and Current Node. Outside an agent Session, the command requires `--force` and one unambiguous Task, Session, or Current Node.
- Forced completion outside an agent Session applies only to one unambiguous idle executable Current Node. It does not create a lasting execution selection.
- `unstructured_output` mode requires the assistant's final answer to be exactly one raw JSON object.
- Any assistant answer that would otherwise complete an active workflow-controlled Node must pass through that Node's current completion contract in every completion mode, whether or not the answer carries an explicit final-phase designation.
- Normal assistant final answers are invalid in `tool` and `shell_command` modes. Kent explains the invalid completion and continues until the agent completes correctly, asks a Question, is interrupted, reaches the invalid-attempt limit, or encounters an error.
- A completion payload contains only optional `transition`, optional `commentary`, and possible Transition Parameters as top-level properties. It never exposes `next_node`.
- Transition Parameter outputs are strings. Kent converts non-string JSON values to strings before binding them. A later Node never receives a structured Parameter value.
- Possible Transition Parameters are optional until Kent knows which Transition the agent selected. The selected Transition then determines which Parameters are required.
- Each required Transition Parameter must become a non-empty string after leading and trailing whitespace is removed.
- Size limits: output field name `<= 64` chars, output field description `<= 1000`, output value `<= 64 KiB`, commentary `<= 64 KiB`, task comment body `<= 256 KiB`.
- Completion-contract changes in `structured_output` and `tool` modes can change prompt-cache continuity. `shell_command` and `unstructured_output` preserve the completion contract in appended instructions instead.
- Kent accepts completion only from the matching Exact Execution Scope or for one unambiguous idle executable Current Node.
- Completion from a retained Workflow Session may target its interrupted idle Agent Node when the Session is still bound to that Current Node. Completion atomically supersedes the interruption and applies the selected Transition.
- Human input accepted before the Completion Fence replaces pending completion. Input after the fence is rejected and returns to the user's draft. Kent never transfers it to a successor Node or Session.
- After five invalid completion attempts, Kent interrupts the Current Node. `[workflow].max_invalid_completion_attempts` configures this limit and defaults to `5`.
- Workflow execution ends only after the Exact Execution Scope stops. The outcome is successful completion, a resumable blocked state, or a non-success terminal outcome.
- Workflow-controlled execution has no wall-clock time limit.

## Script Nodes

- Script nodes are first-class executable workflow nodes. They can be Start targets, fan-out branches, join predecessors/successors, manual automation targets, and board columns anywhere agent nodes are accepted by graph semantics.
- A Script Node can omit its script path in a Workflow Draft. A missing path, nonexistent path, directory, or non-executable file blocks execution, not draft editing.
- Relative script paths resolve against the task execution root. Absolute paths resolve on the Kent server host.
- Kent launches the resolved executable directly with the Execution Root as its working directory. It does not use a shell wrapper, retry the script, or impose a timeout.
- Script input is one JSON object on standard input. Incoming Workflow Parameters are top-level properties. `_kent` is reserved for Task, Node, and parallel Transition Branch identity.
- Script stdout is parsed as the workflow completion JSON using the same completion contract as agent nodes. Stderr is diagnostics only and is not mixed into completion parsing.
- Invalid stdout, invalid script path, interruption, and execution errors leave the script's current Node interrupted with bounded structured details.
- Script completion applies the selected Transition and creates no retained execution history.
- Resuming an interrupted Script Node runs the script again with its current inputs, the current script path, and the latest outgoing completion requirements.

## Workflow Prompting

- Workflow-controlled agent Sessions use dedicated workflow-mode developer instructions.
- When a Session's model context has no prior executable Node assignment, Kent uses the initial-assignment instructions.
- When a Session's model context already contains another executable Node assignment, Kent uses the reassignment instructions.
- Full-history fan-out clones use the reassignment instructions because they inherit the source Session's prior assignment context.
- `compact_and_continue_session` uses the reassignment instructions because it delivers another executable Node assignment.
- When Kent compacts the current Node assignment's context, it reinjects the compaction-reminder instructions for that same assignment.
- Prompt explains task identity, node role/assignee, selected completion behavior, question behavior, handoff/transition mechanics, task comments, and why ordinary final answers are invalid when the selected mode does not accept them.
- Agent Sessions created by Workflow Execution begin at subagent depth `0`. Delegation from a Workflow agent follows the global subagent-depth policy.
- Ordinary final response text cannot bypass the selected completion mode.
- Existing user goal state is not reused as workflow autonomy state.
- Workflow Task Sessions reject user `/goal` control; the current workflow Node is the Task objective driver. Agents may set themselves Goals and complete them, per the agent Goal rules in core-runtime-tools.
- Client input accepted by Runtime Command before the Completion Fence supersedes pending completion. Input that reaches the server after the fence is rejected with a typed retryable result, remains unapplied, and is never transferred to a successor current Node or Session execution.
- Task Comment bodies are not added automatically to agent context. New Workflow instructions include the current visible Comment count and `kent task comment list <task>` when Comments exist. Kent never rewrites older model-visible instructions to update that count.

## Questions And Approvals

- A Workflow Question is a Session Question created through `ask_question`.
- A model does not report `needs_user_input` as a completion status; it calls `ask_question`.
- The Session's current executable Node pauses until the Question is answered.
- All clients use the same authoritative Question and Approval state. A client marks an interaction resolved only after Kent accepts the answer.
- A Question belongs to its Session. Workflow attention refers to that Question and does not create a second Task-owned copy.
- Live Questions and live Approvals exist only within their Exact Execution Scope. A restart does not restore them. Kent interrupts the affected Current Node. Resume closes stale tool operations with a restart outcome before it continues.
- A pending Workflow Transition Approval belongs to the current Task and survives restart.
- Approval is a Transition Branch property.
- If any branch of a selected Transition requires Approval, the complete Transition waits for one Approval before any target becomes current or begins work.
- A pending Approval freezes the selected Transition, its branches, Workflow Version, source and target Nodes, effective branch configuration, display details, and Context Source.
- Later graph edits do not change what a user approves.
- Applied and rejected Transitions are not retained as workflow movement history. Pending Approval state is removed when the Approval applies or a manual move supersedes it.
- A Task awaiting Approval remains at the source current Node and exposes `waiting_approval` status; target Nodes are not current.
- Manually moving a Task that is awaiting Approval clears the proposed Transition and replaces the source current Node with the chosen target.
- Manually moving a Task that is waiting on a Question stops that Exact Execution Scope before applying the move. The Question remains in the Session transcript. Its unresolved attention does not move to the target. The selected Transition Branch determines whether the target reuses the Session.
- A manual override without a Transition Branch cannot target an executable Node. Movement into an Agent or Script Node requires a concrete branch with a prompt and completion requirements.
- Task Start and manual movement make no change when Execution Target selection is required. A valid selection applies the original action once. Dismissal leaves the Task unchanged.
- Approvals occur only after a task has reached an executable node and therefore always reuse the task's locked execution target.

## Context Preservation And Bindings

- Each Transition Branch supports `new_session`, `continue_session`, or `compact_and_continue_session`.
- Workflow-created Session copies preserve delegation ancestry and do not reset delegation depth.
- Continuation modes may select `immediate_source`, `node:<node_key>`, `previous_target`, or `previous_target_or_new` as context source.
- `immediate_source` uses the Session bound to the source current Node.
- `node:<node_key>` selects the latest retained Session associated with the guaranteed-prior agent Node.
- `previous_target` selects the latest retained Session associated with the target agent Node and fails when none exists.
- `previous_target_or_new` selects that Session when one exists and otherwise starts a new Session.
- During parallel work, each Context Source selection stays within the source Current Node's Transition Branch Key.
- Manual movement through a concrete Transition Branch supports `previous_target` and `previous_target_or_new`. Kent resolves the Context Source when it applies the move and freezes that choice before pending Approval. Manual movement does not support a selected prior-Node Context Source.
- Pending Approvals freeze context-source resolution before Approval. A fallback-to-new result remains `new_session` even if another matching Session appears before Approval, and a selected Session remains fixed if a newer matching Session appears.
- Continuation modes apply the target node's subagent role context. `continue_session` preserves the reused session's contract generation. `compact_and_continue_session` compacts the reused session and establishes a fresh target-node contract generation, including model/provider setup, generation parameters, capabilities, enabled tools, native web-search mode, prompt snapshots, context budget, and cache lineage.
- `new_session` uses current role config at its fresh context boundary.
- Consuming agent nodes own required inputs as named top-level string fields with descriptions.
- Prompt placeholders validate against the consuming node's required inputs through `.Inputs.<name>`.
- Prompt templates may reference guaranteed-prior agent node outputs through `.Nodes.<node_key>.<output_name>`.
- `.Nodes` references use stable node keys and declared source-node output fields. The referenced source node must dominate the consuming node in the workflow graph, the source node must not be the consuming node, and unsupported dynamic template access to `.Inputs` or `.Nodes` is invalid.
- Applying a Transition gives each target Current Node every value that it needs. Prompt rendering uses those values and never searches discarded execution history.
- A Workflow edit that makes an executable current Node require input that was never materialized blocks Start or Resume with a typed validation error; Kent does not reconstruct discarded workflow history.
- The first executable node reached from `start` cannot declare upstream inputs and should use task fields such as `.TaskTitle` and `.TaskBody`.
- Source-Node output fields declare values that the Workflow graph can propagate into later current-Node inputs through `.Nodes.<node_key>.<output_name>`.
- Kent derives Parameter flow and completion requirements from required inputs, prompt references, Workflow structure, and Join sources.

## Parallelism And Joins

- A Fan-Out Transition adds several parallel target Nodes to the Task's Current Nodes.
- Branches are ordinary workflow nodes, not subtasks.
- The GUI saves Node Groups only as execution-shaped parallel groups. A Node Group contains branch Nodes and one Join. One Fan-Out Transition targets its branches.
- A Task may have several Current Nodes only when the graph explicitly fans out.
- Joins wait for all required inputs. Kent does not support racing or first-success parallel branches.
- Fan-out topology must have exactly one unambiguous nearest common join reachable from every branch.
- Branch paths before that join may not terminate, enter nested fan-out, or contain cycles.
- Kent rejects ambiguous or complex fan-out.
- Current fan-out state retains the expected Transition Branch Keys and materialized branch inputs only until the Join completes.
- Later graph edits do not change an in-flight fan-out's expected branch set.
- Join Nodes are non-agent fan-in points. They combine inbound Parameter values deterministically and then apply their outgoing Transition.
- Agent synthesis belongs in a normal Agent Node after the Join.
- Orchestrators cannot create Workflow Nodes or Kanban columns dynamically.

## Workflow Execution And Restart

- Workflow Execution is the single authority for Workflow lifecycle changes. It sequences conflicting operations and reports authoritative live state.
- Requests to start eligible work automatically are temporary. Kent loses them on restart and does not reconstruct them from saved Task state.
- Automatic starts use available capacity and prefer to continue related work on the same Task. `[workflow].concurrency` limits automatic starts only.
- Explicit Start, Resume, approval, and executable manual move may exceed the automatic concurrency limit without preempting existing work.
- Only an actively executing Exact Execution Scope proves that an agent or Script is live and interruptible. Current Nodes, Automatic Intents, Session relations, waiting Questions, Task status, transcript entries, and Goals do not prove liveness.
- Start and Resume admit selected parallel branches independently. A failed branch does not undo or block a sibling that started successfully.
- Resume starts a fresh Exact Execution Scope only after the previous scope has fully stopped. Steering remains within the current scope.
- Restart does not restore live Questions, live Approvals, Automatic Intents, Runtime Gates, or Exact Execution Scopes. Kent marks each affected executable Current Node interrupted with a restart reason.
- A pending Transition Approval survives restart with the exact frozen Transition that the operator saw.
- Before Resume continues, it closes unfinished tool operations in the bounded active transcript segment with a restart outcome. Resume does not replay answers, run interrupted tools again, or scan full transcript history.
- Kent never retries an interrupted Current Node automatically.
- Task Interrupt can target one Session or every actively executing agent and Script on the Task. A waiting Question and any state without active execution are not interruptible.
- Clients offer Interrupt only while Kent reports matching active execution. Kent checks again before interrupting and makes no change if execution has already stopped.
- Saved state without matching live execution never becomes interruptible as a fallback. Kent must prevent the mismatch, surface the lifecycle failure, or convert the affected Current Node to interrupted during restart recovery.
- Kent rejects a manual move while an agent or Script is executing or while another lifecycle operation conflicts with movement.
- A manual move stops a waiting-Question scope before it changes the Task.
- Completion can change a Task only from the matching Exact Execution Scope or from one unambiguous idle executable Current Node. A stopped scope and a non-current Node cannot change Task state.
- Completion replaces source Current Nodes, materializes target inputs, and adds target Current Nodes as one atomic change.
- Runtime failures, crashes, interruptions, and fixable start-validation blockers leave the affected Current Node interrupted with a reason.
- `failed` is reserved for unrecoverable corrupted Workflow state.
- Kent retains no completed execution or Workflow-movement history.
- Tasks support Interrupt and Delete. They do not have a separate Cancel operation.

## Task Status And Listing

- Task detail, Workflow boards, and Task lists use one authoritative Task status derived from Current Nodes and current live activity.
- Workflow attention has a global paginated Inbox and a bounded Task-specific feed. It has no Project-specific feed.
- Workflow attention is task-scoped. It includes only unresolved Questions, unresolved Workflow Approvals, and executable Nodes interrupted by errors.
- Workflow validity is not attention and does not create Inbox items.
- Core Task detail includes an unresolved-attention count but not the attention items. It does not scan transcript history.
- The Task-specific attention feed can read the newest active transcript segment to recover unresolved Question content. Desktop Task detail loads this feed independently so it does not delay core Task detail.
- Task status is structured and independent of a specific client. Each client renders and localizes it.
- One primary status uses this precedence: done, live question, live or persisted workflow approval, running, queued, interrupted, backlog, active.
- Running, queued, and live-Question status require matching Exact Execution Scope evidence. `running` means an agent loop or Script process is actively executing; `waiting_question` is not running and is not interruptible. Interruption metadata on a current Node never proves liveness.
- Task information exposes `can_delete` from the same live state. Delete treats this as a hint and checks Quiescence again before making changes.
- Task status preserves every applicable attention kind and its Session and Current Node references when parallel branches differ.
- Workflow validity is workflow-level state and is not a task status.
- Task lists expose typed Task status and attention filters. They expose no separate execution status or execution-count concept.
- Task lists are project-scoped. The CLI defaults to the project attached to the current workspace, including when `--workflow` is supplied.
- A project-only task list spans every workflow linked to that project. An explicit workflow selector narrows the list and must identify an active link in that project.
- CLI `--status` filters typed Task status and `--attention` filters attention kinds across project-wide results. Created, updated, status, and title sorting are Workflow-neutral.
- `--column` and column sorting require an explicit workflow because node keys and column positions are workflow-relative.
- Project-wide human Task rows omit column output, and project-wide JSON Task items omit `column_keys`. Workflow-narrowed lists expose all Current Node keys in board order.
- The first Project-wide Task-list page decides whether rows need Workflow names. The continuation token preserves that display decision. Task membership remains live, so later pages can retain a stale name-visibility decision after concurrent changes.
- Human task-list rows include workflow names only when the filtered query can return tasks from multiple workflows. JSON task items always include their bare workflow UUID.
- A project with no linked workflows, an explicitly selected workflow that is not linked to the project, and workflow-relative operations without a workflow selector return distinct typed actionable errors.
- No-linked-workflow recovery directs the operator to create and link a workflow or list and link an existing workflow before retrying.
- Explicit not-linked recovery offers project workflow discovery and retry with a linked workflow, or linking the selected workflow to the project.
- Workflow-relative column recovery offers project workflow discovery and a task-list retry with an explicit workflow while preserving the parsed filters.
- Task-list status sorting follows primary typed-status precedence; workflow-narrowed column sorting follows workflow column position.

## Task Search

- Task search covers every Task in one persistence root. With no Project filter, search is global. Repeated Project selectors search the union of those Projects.
- Task titles and complete bodies are searchable. Comments are excluded by default and `--include-comments` adds them.
- Search includes every Task lifecycle status by default. Repeatable/comma-separated typed `--status` filters use the same primary Task-status values as task listing.
- The default positional query is one literal substring. Search operators and punctuation have no special meaning in this mode. Literal hits are non-overlapping occurrences ordered from left to right in their source.
- Default literal matching ignores case and removable diacritics according to the FTS5 trigram search contract. It does not promise universal Unicode case folding or universal diacritic equivalence. `--case-sensitive` requires exact original case and diacritics.
- `--fts5` interprets the positional query as a raw FTS5 expression with public `title`, `body`, and `comment` columns. One raw-mode hit represents one matching title, Task-body, or Comment-body source document with one selected snippet, not one phrase occurrence. `--case-sensitive` is invalid with `--fts5`.
- Raw boolean terms must match within one source document; terms split between a Task title and body do not jointly satisfy one expression.
- Literal search requires at least one trigram after search normalization. Kent provides no one-character or two-character fallback.
- `--context` defaults to `20` and accepts `1..64`. Literal mode returns that many Unicode grapheme clusters on each side of an occurrence. Raw FTS5 mode uses it as the snippet token budget.
- Literal results separate original `before`, `match`, and `after` text and identify truncation on each side. Clients add one `…` on each side where text was omitted.
- A normalized literal match includes every complete original grapheme cluster that contributed to the match, including removed combining marks. `match` never splits an original grapheme. Case-sensitive mode requires exact original code points and does not equate NFC and NFD spellings.
- A successful Task or Comment creation, edit, or deletion is reflected in search immediately. If Kent cannot update search, the source change fails and remains unapplied.
- Search ranks each Task by its strongest matching title, body, or Comment under the requested mode. A case-insensitive candidate without a case-sensitive occurrence cannot retain or rank a Task in case-sensitive mode.
- Ranking weights title, body, and Comment sources. A strong body match can outrank another Task's weaker title match. Title-before-body-before-Comment is absolute only for hit order within one Task.
- Within one Task, title occurrences precede body occurrences. Task title and body results precede Comment results. Remaining ties are deterministic.
- Search pagination is breadth-first by per-Task hit ordinal: every matching Task's first hit precedes any Task's second hit, and so on. Later pages may repeat a Task with later hits.
- Each page selects hits in that breadth-first order and then groups the selected hits by Task. Task groups follow the order of their first selected hit. Hits inside a group retain their absolute per-Task order.
- `--page-size` defaults to `100` and cannot exceed `100` hits. `--page-token` is an opaque cursor tied to every search choice and to the matching, status, source, and ranking contracts.
- Plain output renders `SHORT-ID: title`, unlabeled title/body hit lines, a lowercase `comments:` heading only when the page contains Comment hits for that Task, Comment hit lines, and `[N more hits]` when later hits remain. It exposes no line numbers, persistent Comment IDs, status, workflow, score, author, or date.
- Plain literal hits use `…` only on a side with omitted source text. Plain output folds a hit to one physical terminal line; structured output preserves the original segments.
- A valid empty search prints `No matches.` and succeeds.
- JSON output is grouped by Task and distinguishes literal mode from raw FTS5 mode. It includes Project, Task, and Workflow identity, title, canonical Task status, total hit count, absolute per-Task hit positions, title/body/Comment source identity, complete Comment ID for Comment hits, mode-specific context or snippet data, and an optional continuation token. Empty results use `groups: []`; an absent continuation token is omitted. It does not expose ranking scores.
- Query validation trims one positional argument and limits it to `4096` Unicode code points. A literal query must contain at least one searchable trigram after search normalization. A raw expression follows FTS5 behavior for shorter terms. Kent reports distinct too-short, malformed-expression, and invalid-cursor errors.
- Search uses the same authoritative Task status as Task lists and Task detail.
- Each response is point-in-time consistent for matching text, counts, filters, and Task metadata, and combines that data with one current view of live Task activity. A concurrent change appears wholly before or after the response, never as mixed state.
- Search does not retain all matching Tasks, sources, or occurrences in memory.

## Execution Targets And Worktrees

- A workflow execution target policy is evaluated only when an unlocked task first reaches an executable node through task start or manual movement.
- No managed worktree uses the source workspace as the execution root, supports non-Git workspaces, and creates no branch or worktree and runs no worktree setup.
- A no-managed-worktree target follows the task's current source workspace. Changing that workspace intentionally changes later execution roots.
- Source `HEAD`, repository default branch, and custom Git ref resolve to an immutable commit before managed-worktree creation. A custom ref accepts any Git revision that resolves to a commit.
- Repository default branch uses configured local remote-HEAD metadata: `origin` when configured, otherwise one unambiguous configured remote HEAD. Kent does not contact remotes or guess branch names; missing or ambiguous metadata makes the configured target unavailable.
- `ask_on_first_execution` and an unavailable configured target use the same task-local selection flow. They offer no managed worktree, source `HEAD`, repository default branch, and custom Git ref.
- For `ask_on_first_execution`, repository default branch is preselected. For an unavailable configured target, the configured mode and custom-ref input remain selected when useful; otherwise repository default branch is preselected.
- An unresolvable configured target asks the operator to select a concrete target and explains which configured target failed and why.
- Selection-required results distinguish two reasons: the Workflow requires selection, or the configured target is unavailable. Every selection flow offers all four concrete modes.
- Failure to resolve an explicitly selected custom ref is a validation failure. It does not recursively request selection or fall back to another target.
- A Task locks target-selection provenance only when the initiating action successfully reaches its first executable current Node. Later Nodes and retries reuse the locked mode and managed requested/resolved facts despite Workflow edits or Git ref movement.
- A Task with a legacy managed worktree and usable recorded `HEAD` continues to use that worktree. Kent identifies its observed commit as legacy provenance and does not present it as a known original branch point.
- A legacy Task without a managed worktree remains unlocked and uses its Workflow's source-`HEAD` policy.
- Managed targets use the same creation, setup, and collision behavior as other Kent-managed worktrees. Before Kent schedules the first executable Current Node, it loads worktree setup settings from the Task's source workspace. A configured setup script must succeed for a worktree created by that operation.
- Managed worktree setup failure leaves the initiating action unapplied and unscheduled. Any created worktree remains available for inspection or manual repair.
- Setup runs only when an operation creates or recreates a worktree root. A later retry does not rerun setup for an existing compatible root.
- Setup receives the source workspace root, branch name, and managed worktree root as stable positional inputs.
- Workflow Task setup has no Session identity. Its JSON input represents the Session as `null`, and its Session environment value is absent. Session-originated setup supplies the requesting Session identity in both inputs.
- Kent-provided setup inputs are authoritative. Conflicting inherited process values cannot provide or override Kent-reserved setup inputs.
- A managed target remains tied to its original source workspace, but its current root, metadata binding, branch history, and named branch may change.
- Before execution Kent validates that the bound root is the exact worktree root for the source repository. Initial managed-worktree creation and conservative repair establish a named branch; an available locked worktree remains valid at either a named branch or detached `HEAD` for resume and subsequent workflow execution. Kent never compares current history or HEAD with the originally resolved commit.
- When a locked managed root or its Kent association is missing, the initiating operation can restore an existing named branch at an available managed root and run setup for the recreated root.
- Conservative repair never recreates a missing branch from the old base commit, overwrites an existing directory, resets or renames a branch, accepts detached HEAD, repairs another repository, or infers ownership by scanning arbitrary roots. Unsafe or ambiguous states return one typed locked-target error with a small product-level cause.
- There is no target-replacement flow. A locked target is never converted to no managed worktree.
- Task detail always shows the source workspace. After target lock, it also shows the recorded target provenance and managed-worktree path when present. Task detail does not inspect live path availability or the current Git branch; Worktree status owns those live facts.
- Human task detail shortens the resolved commit for readability. Structured JSON retains the full commit value.
- Initial managed worktree creation uses the task short ID as the branch name.
- Task worktree creation uses the same branch and root collision behavior as ordinary worktree creation.
- Worktree deletion/retargeting treats non-terminal tasks referencing a managed worktree as blockers.
- Worktree deletion fails immediately if another Session targeting the worktree is running or has begun to start. It does not wait for that work.
- After deletion starts, new work for every Session that targets the worktree is rejected until retargeting and Git removal finish.
- A rejected deletion leaves Session targets, worktree information, Git state, and branch state unchanged.
- Task worktree creation and conservative restoration have the same setup and collision behavior.
- The CLI task-start, task-approve, and task-move commands may select a concrete target for an unlocked task even when the workflow has a fixed policy. Task creation has no target override.
- CLI target selection uses `--execution-target none|head|default-branch|ref:<revision>`; custom Git revisions require the explicit `ref:` namespace.
- CLI task start, approve, and move never prompt interactively. Selection-required output identifies the reason and concrete rerun flags. Task start exposes the same typed outcome in JSON.

## Project Keys And Task IDs

- Project keys are uppercase, globally unique within a persistence root, 2-8 chars, and match `^[A-Z][A-Z0-9]{1,7}$`.
- Project creation chooses a key explicitly; default suggestion can use the first three letters of project name.
- A Project without a Project Key receives one before Kent creates its first Task Short ID. Kent derives the suggestion from the Project name and resolves collisions.
- Project keys are editable at any time, including after a project has tasks. A key change only sets the prefix for tasks created afterward and never rewrites existing task short IDs, so a project's history can contain mixed prefixes. The change is rejected only for format violations or a collision with another project's key.
- Existing task short IDs keep their historical key forever; a rename does not cascade to them.
- Task short IDs are stored durable product identifiers, not derived display strings.
- Task required fields are title, short ID, and body.
- A Task can include an optional structured `source_url`.

## Comments

- Agents can add, replace, and delete Task Comments through Task-management commands and APIs.
- There are no model-callable comment tools.
- Comments record the author or source agent when available.
- Comments belong to the Task and are not files in its worktree.
- Deleting a Task Comment removes it completely. Kent cannot list or restore deleted Comments.
- CLI task comment management accepts both `kent task comment ...` and `kent task comments ...`.

## Durable Workflow State

- A Task owns its Current Nodes, their current inputs, optional Session associations, and pending Approval.
- Current Nodes have no independent product identity.
- An executable Current Node retains only the state needed to execute or resume. A Script Node has no Session.
- Applying a Transition replaces the source Current Nodes, supplies target inputs, and adds target Current Nodes as one atomic change.
- Kent does not retain applied or rejected Transition history, completed Current Nodes, or execution-attempt records.
- A pending Approval is the only retained frozen Transition. Kent removes it after approval, rejection, or a superseding manual move.
- A retained Session identifies its Workflow Node and, during parallel work, its Transition Branch Key.
- Project Workflow Links represent active membership only. Unlinking removes an unused link completely.
- A Project cannot unlink a Workflow while Tasks use that link. The error reports blockers with counts and references.
- Deleting a Workflow is the only way to retire it. Workflows and Nodes have no archive state.
- Each Task belongs to one Project through one Project Workflow Link.
- A Project's default Workflow link and primary workspace must belong to that Project.
- Kent derives workspace and worktree display facts from their authoritative roots and Project choices. It does not maintain duplicate editable copies.
- After reconnect or a live-update error, clients refresh authoritative Workflow and Task information.
- A Workflow deletion preview reports counts only.
- Workflow deletion requires Quiescence across every affected Task. It also requires a replacement when deleting the Project's default Workflow would leave an invalid default state.
- Confirmed Workflow deletion removes the Workflow, its Project Workflow Links, its Tasks, and its graph as one atomic change.
- Workflow deletion preserves Sessions, worktrees, and other external artifacts. It does not perform individual Task Delete cleanup.
- Task Delete requires Quiescence. It safely removes reconstructible managed artifacts, preserves Session artifacts, and removes the Task only after cleanup succeeds.
- Repeating Task cleanup for an artifact that is already absent succeeds.
- Saving a Workflow checks the expected Workflow Version, validates the Workflow Draft, reports active blockers, and requires confirmation for destructive removals.
- A successful Workflow save applies details and graph changes as one atomic change.
- Saving a Workflow graph never deletes or moves Tasks.
- Executable context uses the Task, the latest Workflow definition, current inputs, Execution Target, and selected Context Source Session. It does not search discarded execution history.

## Compatibility Data

- A legacy canceled Task moves to terminal Node `done` when that Node exists.
- If its invalid Workflow has no `done` Node, Kent removes only the Task's Workflow state and preserves its Sessions, worktrees, and other external artifacts.
- `source_url` remains an optional structured Task field.
- Task Short ID remains durable product data.
- Context Source selection uses retained Sessions and does not depend on discarded execution records.
- Pending Approval retains the exact Transition facts that the operator is approving. Ordinary work uses current inputs and the latest Workflow definition.
- Task Comments retain their author identity when available.
- Session listings retain the first prompt preview.
- Sessions retain unsent input drafts for recovery.

## CLI Surface

- The Workflow and Task CLI provides complete control for operators and agents.
- Agents can build and edit complete Workflow definitions with high-level commands. Import and export are separate sharing features.
- CLI command grouping is not a compatibility contract. The documented behavior, accepted data, and machine-readable output are compatibility contracts.
- CLI output includes stable identifiers needed by later commands.
- `kent workflow delete <workflow>` reports the deletion impact and makes no changes unless `--confirm` is present. A confirmed deletion submits the previewed Workflow Version and affected Project, Project Workflow Link, and Task counts; if the impact changes or deletion has blockers, Kent deletes nothing and reports the blockers.
- The plain-text `kent task complete` acknowledgement omits identifiers. JSON completion output remains machine-readable.
- Project label catalog and task-assignment commands live under `kent task label`; there is no top-level label command. Catalog commands create, list, rename, and delete labels in the selected Project. Human catalog output includes readable names and stable UUIDs.
- Label selectors use repeatable `--label <name-or-uuid>`. Canonical UUID v4 text selects by identity; every other value is trimmed and matched against the complete Project label name with the label catalog's case-insensitive Unicode comparison. Label selector values are literal and are never comma-split.
- `kent task label add <task>` and `kent task label remove <task>` require one or more label selectors and apply all resolved membership changes atomically with idempotent add/remove behavior. Label names resolve against the Task's actual Project; `--project` scopes project-short-ID lookup. Task creation accepts the same repeatable selector and atomically assigns existing labels.
- Every catalog and assignment command accepts `--json`. Catalog JSON returns label records for create, rename, and list, and the deleted label ID for delete. Assignment JSON returns the task ID and authoritative resulting label IDs. Human assignment output is a short acknowledgement.
- Human task show/list output adds one `Labels:` line only for assigned labels and quotes every name. Task show/list JSON exposes one `label_ids` field and does not duplicate assignments as named objects.
- Task-list label filtering uses repeatable literal `--label` selectors plus `--label-match any|all`, defaulting to `any`. `--unlabeled` selects tasks with no assignments and is mutually exclusive with label selectors and an explicitly supplied match mode; an explicit match mode without a label selector is invalid.
- Every selector in one command must resolve before task creation, assignment, or listing proceeds. Selector-resolution failure reports every unresolved selector and never ignores or partially applies the input.
- `kent task list` exposes one typed task status. `--status` filters primary status, `--attention` filters typed attention, and `--column` filters workflow node keys.
- `kent task list` filters and sorts before pagination. Multiple values for one filter are ORed. Different filter types are ANDed. A Task with several Current Nodes exposes all matching column keys in Workflow order.
- `kent task list` default ordering is `status:asc,updated:desc`, where `status` uses primary typed-status precedence and `updated` is newest-first. Custom `--sort` accepts ordered `field:direction` selectors for `created`, `updated`, `status`, `column`, and `title`; selectors can be comma-separated in one flag and may be supplied by repeated flags.
- `kent task complete` accepts dynamic parameter flags, repeatable `--param name=value`, and `--json`/`--json-file` completion payload input. JSON input modes print JSON responses.
- Plain-text `kent task complete` output is a model-facing handoff acknowledgement: `Completion scheduled. The transition <source display name> → <destination display name> will execute now. Your next agent turn will begin with the next workflow instructions.`
- The acknowledgement uses the target node display name for an ordinary transition. A fan-out uses its shared target node-group display name when present and otherwise its transition display name.
- The same acknowledgement is used for agent-session and forced human completion. It always promises a next agent turn regardless of context-preservation mode or whether another turn occurs, and it does not expose approval or transition state.
- JSON completion output retains its existing field set and does not include the plain-text acknowledgement.
- `kent task edit <task>` changes a Task's title, body, or source workspace. It requires at least one of `--title`, `--body`, `--body-file`, or `--source-workspace`. It preserves the current title when `--title` is absent. Agents can use it. `--json` prints the result.
- `kent task create` and `kent task edit` accept `--source-workspace` as either a workspace id or a path; a path is resolved through its project binding. An omitted source workspace leaves it unchanged on edit.
- Workflow/task CLI commands report remote-close failures to stderr after command work finishes. A close failure does not change a successful exit code, and an operation failure keeps its existing nonzero exit code.
