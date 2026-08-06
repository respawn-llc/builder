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

## Task Mutation Authority

- Users may Start, Interrupt, Resume, approve, or manually move any otherwise
  eligible Task.
- A Session associated with a Task may Start, Interrupt, Resume, approve, or
  manually move a different Task.
- A Session associated with a Task must not Start, Interrupt, Resume, approve,
  or manually move its own Task.
- Kent derives the invoking Session's Task from the Session's direct durable
  Task ownership.
- The server trusts the optional invoking-Session identity supplied by the
  client. A request that omits it is treated as user-originated.
- This restriction is a cooperative agent policy, not an authentication
  boundary against a modified client or direct RPC caller.
- The restriction does not depend on the invoking Session's Current Node or
  live execution state.
- A persisted Session with no Task ownership may Start, Interrupt, Resume,
  approve, or manually move any otherwise eligible Task.
- If the supplied invoking Session does not exist, Kent rejects the operation
  and changes nothing.
- A self-target rejection changes no Task, Approval, execution-target, live
  execution, or scheduling state.

## Task Labels And Filtering

- Each label has an immutable UUID v4 identity. Its mutable name is trimmed, 1–64 characters, preserves display capitalization, and permits Unicode letters and numbers, spaces, and `: & * % $ # @ ! ? . , / \ + | - _ ~ '`.
- Label names are case-insensitively unique within a Project. Capitalization-only rename is allowed without changing identity or task assignments.
- A Project can have at most 100 Labels. Clients load the complete catalog without pagination.
- A task may use any subset of its Project's catalog; there is no separate per-task label limit.
- Label creation, rename, deletion, assignment, and removal remain available regardless of whether affected Tasks are Backlog, active, running, interrupted, or done.
- Label changes do not change a Task's update time. Outside Labels sorting, they do not change Task order between board page requests. While a Workflow board sorts by Labels, assignment, deletion, or Project Label reorder can reposition Tasks relative to subsequent offset requests.
- Task creation may atomically assign existing Project labels. Later assignment changes use idempotent add/remove semantics: adding an existing assignment or removing an absent assignment succeeds and returns the authoritative resulting label set.
- Renaming takes effect everywhere without changing assignments. Deletion requires confirmation and atomically removes the label from every task; the confirmation does not require an affected-task count. Desktop deletion uses explicit confirmation; invoking the explicit CLI delete command is sufficient confirmation and does not prompt or require a separate confirmation flag.
- Labels have no color. A Project owns one durable manual Label sequence.
- Creating a Label places it at the beginning of the sequence. Renaming a Label preserves its position. Deleting a Label preserves the relative order of every surviving Label.
- Every Label catalog and assigned-Label projection uses the Project sequence, including `kent task label list`. The catalog sequence is authoritative; clients do not receive a separate position value.
- A reorder applies to one Project and must identify every current Label in that Project exactly once. Kent rejects missing, duplicate, unknown, and cross-Project Label identities without changing the sequence.
- Reordering to the current sequence succeeds without a durable change or Project event. Changing the sequence persists atomically and emits one Project event.
- Kent does not guarantee the relative outcome of concurrent reorder requests.
- Concurrent Label catalog mutations may fail. A failed mutation leaves the catalog unchanged, and Kent does not retry it automatically.
- The Desktop board filter chooser is the Label reorder surface. `kent task label` has no reorder command.
- Kent does not promise the initial relative sequence of Labels that predate manual ordering.
- Task-list `labels` sorting remains available when explicitly requested.
- Kent applies Label filters before pagination for Workflow boards and Task lists.
- An included Label condition is true when a Task has that Label. An excluded Label condition is true when a Task does not have that Label.
- OR matches a Task when at least one included or excluded Label condition is true. AND matches a Task when every included and excluded Label condition is true. A named filter may consist entirely of excluded Label conditions. One condition behaves identically in both modes.
- `No labels` matches Tasks with zero Label assignments and is mutually exclusive with named Label conditions. No named Label conditions means no Label restriction.
- Kent combines the complete Label expression with every other active Task-list filter. Filtering preserves sorting and pagination behavior. A client never loads the complete board or Task list to apply filters.

## Workflow Board Ordering

- A Workflow board applies one selected order independently inside every column.
- Board sorting offers `Updated`, `Created`, `Labels`, and `Short ID`, in that order.
- The default board order is `Updated Desc`.
- `Created Asc` and `Updated Asc` show the oldest Tasks first. Their descending orders show the newest Tasks first.
- `Short ID Asc` shows the lowest Task Short ID first. `Short ID Desc` shows the highest Task Short ID first.
- Labels sorting arranges each Task's complete assigned Label sequence in the Project's manual Label order and compares the sequences from left to right.
- In ascending Labels order, the first differing Label decides the order, and a shorter otherwise-identical sequence comes first.
- Descending Labels order reverses the comparison among labeled Tasks.
- Tasks with no Labels follow every labeled Task in both Labels directions.
- When Tasks have equal values for any selected field, including equal Label sequences, Kent must use Task Short ID as the final tie-breaker in the selected direction.
- Kent combines every active board filter with logical AND and applies the complete filter before sorting and pagination.
- The Kent server owns board ordering and pagination. Clients preserve the returned order and do not sort board cards.
- Each returned board page is bounded. Clients never load a complete board or column to sort it.
- Done has no Task-count bound. Board sorting may evaluate all matching Tasks before applying an offset, so Kent makes no board-sort latency guarantee for very large matching columns.
- Board pagination uses a zero-based offset. Changing the complete filter or selected sort restarts pagination at offset zero.
- A Task or Project Label mutation can change filtered-set membership for any board sort when a Label filter is active; outside `Labels` sorting it preserves the relative order of Tasks that remain members. Desktop invalidates and refetches active board cards after a committed local or subscribed Label mutation, preserving the retained offsets while adopting the current server order. A Task or Project Label order change between page requests may therefore transiently repeat or skip a Task in the loaded view, and Kent does not guarantee a stable snapshot across page requests.

## Task Dependencies

- A Task Dependency is one directed relationship from a Blocker Task to a
  Blocked Task.
- The ordered Blocker Task and Blocked Task identities identify the
  relationship. A Task Dependency has no separate product identifier.
- Both Tasks in a Task Dependency must belong to the same Project. They may
  belong to different linked Workflows or source workspaces in that Project.
- Kent stores one relationship direction. It derives `Blocked by` and `Blocks`
  views and never stores a reverse copy.
- A Task may have at most 50 direct Blocker Tasks.
- A Task may directly block at most 50 Tasks.
- Kent returns each complete direct relationship direction without pagination.
- A Task cannot depend on itself.
- Kent rejects a relationship when the reverse direct relationship already
  exists.
- Kent permits directed dependency cycles of three or more Tasks.
- Kent never traverses transitive dependencies for attachment, display, start
  confirmation, or agent context.
- Task Dependencies are advisory planning metadata. They never pause, move,
  resume, interrupt, or otherwise mutate work already underway.
- Users may add or remove Task Dependencies in every Task state.
- Adding an existing relationship succeeds without changing state.
- Removing an absent relationship succeeds without changing state.
- An actual relationship addition or removal changes the update time of both
  affected Tasks.
- An idempotent no-op does not change either Task's update time.
- A change in dependency satisfaction updates authoritative dependency
  projections without changing the Blocked Task's update time.
- Kent validates Task existence, Project scope, self-dependency, reciprocal
  dependency, and both cardinality limits before it adds a relationship.
- Kent validates and applies one relationship mutation atomically.
- A relationship validation failure changes neither Task nor relationship
  state.
- Concurrent relationship additions cannot exceed either cardinality limit.
- Typed relationship errors identify the violated rule and the affected Tasks.
- A Task Dependency is satisfied if and only if its Blocker Task has
  authoritative `done` status.
- The shared Workflow-board and Task-list dependency filter is nullable. `null` applies no dependency restriction. `true` includes Tasks with zero unsatisfied direct Task Dependencies. `false` includes Tasks with one or more unsatisfied direct Task Dependencies.
- A Task with no direct Task Dependencies matches the `true` dependency filter.
- Kent combines the dependency filter with every other active filter using AND semantics.
- Kent applies the dependency filter before pagination for Workflow boards and Task lists. Clients never load a complete board or Task list to apply it.
- Every Terminal Node satisfies a Task Dependency, including a Terminal Node
  reached through Manual Move.
- Reopening a done Blocker Task makes its Task Dependencies unsatisfied again.
- Task title changes and Project Key changes preserve Task Dependencies and
  immediately change their displayed Task metadata.
- Task Delete atomically removes every incoming and outgoing Task Dependency.
- Deleting a Workflow or Project removes every Task Dependency that touches a
  deleted Task.
- Deletion-induced relationship removal changes each surviving related Task's
  update time once.
- Task Delete confirmation does not add dependency-specific copy or another
  confirmation step.
- `Blocked by` and `Blocks` lists put Tasks whose status is not `done` first,
  then order each group by Task Short ID.
- Task detail exposes both complete directions, canonical related-Task status,
  Blocker satisfaction, and direct aggregate counts from one authoritative
  server projection.
- Task Start and Manual Move to an executable Node check the Blocked
  Task's current direct unsatisfied dependencies.
- Kent performs the dependency check after it validates that the requested
  action is otherwise available and before Execution Target selection or
  another continuation dialog.
- When unsatisfied dependencies exist and the request has no explicit proceed
  intent, Kent returns a typed dependency-confirmation-required outcome with the
  unsatisfied count and changes nothing.
- Proceeding despite dependencies acknowledges one initiating operation. Kent
  does not retain that acknowledgement on the Task.
- Proceeding despite dependencies does not acknowledge a relationship snapshot.
  A concurrent dependency change does not require another confirmation within
  that same initiating operation.
- If a later continuation is dismissed, Kent leaves the Task unchanged and
  discards the proceed intent.
- A later independent Start or Manual Move checks dependencies again.
- Resume, Approval, and automatic Workflow transitions do not request
  dependency acknowledgement.
- Dependencies never become a scheduler gate. An explicitly acknowledged Start
  or Manual Move remains allowed.

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

- Nodes define Workflow states and executable behavior. Agent Nodes configure a required fallback Assignee, completion mode, and worktree and Session policy. Script Nodes configure an executable path.
- Transitions define target Nodes, optional effective-Assignee and thinking selection, Approval, Context-Preservation Mode, Context Source, Parameters, routing, and Join behavior.
- The Assignee is the subagent role materialized for an Agent Current Node. There is no separate product Assignment entity.
- Workflow definitions select existing subagent role identities. They do not directly override model, provider, authentication, or tools. The sole tool exception is the approved Transition-selected Assignee path, where Kent force-enables Questions; every other tool follows the selected role or retained Session contract.
- Agent Nodes do not own invocation prompts. Each incoming Transition Branch owns its Transition Prompt. Kent provides no Node-level prompt fallback.
- Every Agent Node's configured fallback Assignee, including default and built-in roles, must have `ask_question` enabled. A Transition-selected Assignee is exempt because Kent force-enables Questions only for that execution path. Validation reports affected fallback Nodes and does not change role configuration. Workflow Drafts and Backlog Tasks remain allowed. Task Start, Resume, and manual movement to an executable target validate the latest Workflow before target selection, Task movement, or execution. Pending Approval creation validates before freezing its target; Approval does not repeat that graph or configuration validation.
- Every Agent Node has a required configured Assignee.
- Each eligible incoming serial Transition Branch independently may override that fallback by selecting the effective Assignee through a protected required Transition Parameter.
- An eligible Transition without the override uses the target Agent Node's configured Assignee, so separate incoming Transitions may mix override-enabled and fallback-only behavior.
- Separate serial Transitions that converge on one Agent Node retain independent selector state, protected Parameter configuration, and selected values.
- Fan-Out Transitions cannot select target Assignees or thinking levels.
- Assignee and thinking selection is eligible only from Agent and Script Nodes into Agent Nodes.
- New Session and Compact and Continue Session entries expose and honor enabled Assignee selection.
- Continue Session exposes and honors Assignee selection only when **Previous session from this target, or new session** resolves to a new Session.
- A Continue Session entry that reuses a retained Session hides and ignores a supplied Assignee and preserves the retained Session's materialized Assignee.
- Other Continue Session Context Sources do not expose Assignee selection.
- Every eligible serial Transition may expose and honor enabled thinking selection regardless of Context-Preservation Mode or retained-Session reuse.
- Transition-selected Assignees must be nonblank roles explicitly configured with `agent_callable = true`.
- `workflow_subagent` does not restrict Transition-selected Assignees.
- Unknown roles, roles without explicit `agent_callable = true`, and unavailable roles use one model-facing unavailable-role error category.
- Workflow execution force-enables Questions for a Transition-selected Assignee even when that role's configuration disables Questions.
- When no role is explicitly configured with `agent_callable = true`, Assignee selection is unavailable.
- When exactly one role is explicitly configured with `agent_callable = true`, Kent hides the protected Assignee Parameter on an override-enabled Edge, materializes that role automatically, and ignores any supplied Assignee value.
- When several roles are available, the protected Assignee Parameter is model-facing as an ordinary required string Parameter.
- The protected Assignee Parameter's default Key is `agent_role`.
- Assignee and thinking selectors own Protected Parameters with the lifecycle and edit behavior defined in the terminology specification.
- Ordinary and protected Parameter Keys remain unique across one Transition contract even while a protected Parameter is dormant.
- A supplied protected value is ignored only for retained-Session Assignee reuse, sole-role automation, or finite zero/one thinking automation. A value for a selector that is disabled, unavailable, or topology-inapplicable remains an unknown Transition Result output.
- A blank protected Assignee description derives `Override the subagent role for the next node, available roles: <CSV of roles in config.toml that have agent_callable=true>` when Workflow completion instructions are prepared.
- The derived Assignee list uses the server's loaded configuration and deterministic alphabetical order.
- A nonblank authored Assignee description replaces the derived description.
- Thinking selection uses a protected ordinary required string Parameter whose default Key is `thinking_level`.
- A blank protected thinking description derives `Override the thinking level for the next node, available levels: <CSV of levels supported by the target model>`.
- Thinking selection on a Transition without an Assignee override uses the target Node's configured Assignee provider and model.
- Thinking selection on a Transition with an Assignee override uses the union of finite catalog levels supported by the models of all explicitly agent-callable roles.
- Thinking selection is available when at least one applicable role resolves to a provider and model that support thinking.
- If any applicable model has no enumerable catalog contract, a Workflow Draft may retain a blank protected thinking description, but execution and Manual Move require a nonblank custom description.
- A selected role and thinking-level pair must be supported by that role's finite model-catalog contract when the protected thinking Parameter is exposed.
- When the selected role's model has no catalog contract, Kent accepts any nonblank thinking value after the required custom description is authored, and model execution may reject that value.
- When the finite thinking union is empty, Kent hides the protected thinking Parameter and preserves the selected role's ordinary configured thinking behavior.
- When the finite thinking union contains one level, Kent hides the protected thinking Parameter and applies that level only when the selected role's model supports it.
- A supplied thinking value is ignored whenever the protected thinking Parameter is hidden because the finite union contains zero or one level.
- Workflow definition validation checks each Edge selector's topology and configuration availability, protected Parameter identity, and every Agent Node's required fallback Assignee.
- Invalid selector modes, Parameter purposes, and malformed or colliding Parameter Keys are hard graph-shape errors. Selector topology and role/thinking catalog applicability are semantic validation errors: Drafts may save and reload them with diagnostics, while task creation and execution reject them.
- Kent validates supplied Assignee and thinking values when a Transition Result or Manual Move is applied and before creating a target Current Node or pending Approval.
- Each visible executable or terminal Node is also a Kanban column and status. Join Nodes are omitted from boards.
- Workflows can contain Start, Agent, Script, Join, and Terminal Nodes. Approval is a Transition Branch property.
- Each Workflow has exactly one Start Node. It is non-executable.
- For Task Start, the Start Node must have exactly one outgoing Transition with exactly one branch that targets an executable Node.
- Terminal Nodes are strict sinks. Manual reopen or rework is an explicit override, not retained Workflow history.
- Draft validation reports semantic errors but does not block save/link/default selection.
- Task creation and execution validation accumulate all safe actionable errors and reject invalid graph, role, and Parameter configurations.
- Execution-valid graphs reject detached islands: every node reachable from start, every non-terminal can reach terminal, terminal cannot auto-run.
- Cycles/self-loops are allowed outside restricted fan-out branch paths.
- New draft Nodes, Node Groups, Transitions, and Transition Branches receive UUID v4 identifiers. Preview and Save preserve those identifiers unchanged. Product-facing keys remain stable semantic references.
- `node_key`, `transition_id`, `edge_key`, Parameter Keys, and binding names match `^[a-z][a-z0-9_]{0,63}$`.
- Workflow display names are labels, not references, and are trimmed non-empty strings capped at 120 chars.

## Node Completion

- Agent Nodes complete by producing a Transition Result, not by returning ordinary natural language.
- A Transition Result selects an outgoing Transition and supplies the Transition Parameters that its targets require.
- Runtime failure, unanswered questions, interruption, and validation blockers are orchestration outcomes, not model-selected terminal statuses.
- Completion modes are `structured_output`, dynamic `complete_node` tool, `shell_command`, and `unstructured_output`. Global `[workflow].completion_mode` selects `auto`, `structured_output`, `tool`, `shell_command`, or `unstructured_output`; agent nodes can override it with the same values or inherit the global default.
- Start, join, and terminal nodes reject non-empty completion-mode overrides.
- A completion-mode override belongs to the source Agent Node, not to a Transition Branch. Transitions define the possible branches and Parameter Requirements.
- `auto` resolves when a Session Contract generation prepares its first model request after Session planning and tool availability are known: shell-unavailable agent execution uses `unstructured_output`; Workflows with any literal `continue_session` branch use `shell_command`; all other agent execution uses structured output when provider capabilities support it and dynamic tool mode otherwise. `compact_and_continue_session` does not trigger shell fallback. A Node-level `auto` override applies this policy even when the global config is a fixed mode.
- The first model request snapshots the effective completion mode into the Session Contract generation.
- Resume and `continue_session` reuse the retained Session Contract generation's effective completion mode even when the latest global setting or Agent Node override would select another mode. Full-history fan-out clones inherit the same effective completion mode.
- `new_session` creates a new Session, and `compact_and_continue_session` creates a new Session Contract generation. Only these Context-Preservation Modes may select a different effective completion mode for the target Agent Node.
- Resume resolves the latest Runtime Parameter Contract from the current Workflow definition. A live Exact Execution Scope keeps the completion contract already advertised for that scope until it stops.
- Forced `structured_output` fails fast with an actionable error when unsupported. Forced `tool` always uses dynamic tool mode. Forced `shell_command` fails execution start when the resolved runtime shell tool is unavailable.
- `[workflow].use_required_tool_calls` defaults to `true`. When enabled, each model response in `shell_command` and `tool` modes must call at least one available tool. This requirement does not add, remove, or reorder tools.
- When `[workflow].use_required_tool_calls` is `false`, model requests in `shell_command` and `tool` modes use automatic tool selection. The selected completion mode still rejects ordinary assistant final answers.
- Accepted live user steering re-enters the same live completion policy. `structured_output` and `unstructured_output` generation use automatic tool selection.
- Manual interruption releases the specialized Exact Execution Scope.
- If the retained Workflow Session still belongs to the interrupted Current Node, a later ordinary interactive activation uses automatic tool selection and remains eligible to complete that Current Node.
- Kent resolves workflow-started and ordinary interactive completion from that retained Session to the same Current Node. The interactive activation does not create a second Transition authority.
- Resume starts a fresh Exact Execution Scope while retaining the Session Contract generation's effective completion mode.
- `complete_node` is always available in tool completion mode, regardless of the Assignee's configured tools.
- `shell_command` mode instructs the agent to run `kent task complete`. In an agent Session, the command resolves the assigned Task and Current Node from the current Session. Outside an agent Session, the command requires `--force` and a Session selector or a Task selector that matches exactly one idle executable Current Node.
- Forced completion outside an agent Session applies only to one unambiguous idle executable Current Node. It does not create a lasting execution selection.
- `unstructured_output` mode requires the assistant's final answer to be exactly one raw JSON object.
- Any assistant answer that would otherwise complete an active workflow-controlled Node must pass through that Node's current completion contract in every completion mode, whether or not the answer carries an explicit final-phase designation.
- Normal assistant final answers are invalid in `tool` and `shell_command` modes. Kent explains the invalid completion and continues until the agent completes correctly, asks a Question, is interrupted, reaches the invalid-attempt limit, or encounters an error.
- A completion payload contains only optional `transition`, optional `commentary`, and possible Transition Parameters as top-level properties. It never exposes `next_node`.
- Transition Parameter outputs are strings. Kent converts non-string JSON values to strings before binding them. A later Node never receives a structured Parameter value.
- Possible Transition Parameters are optional until Kent knows which Transition the agent selected. The selected Transition then determines which Parameters are required.
- Each required Transition Parameter must become a non-empty string after leading and trailing whitespace is removed.
- Size limits: Parameter Key `<= 64` chars, Parameter description `<= 1000`, Parameter value `<= 64 KiB`, commentary `<= 64 KiB`, task comment body `<= 256 KiB`.
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
- Kent launches the resolved executable directly with the Execution Root as its working directory. It does not use a shell wrapper, retry the script, or impose a timeout, CPU quota, or memory quota.
- Kent bounds captured stdout and stderr independently.
- When interruption stops a Script Node process, Kent requests graceful termination and force-kills the process after a bounded grace period if it remains running.
- Script input is one JSON object on standard input. Incoming Workflow Parameters are top-level properties. `_kent` is reserved for Task, Node, and parallel Transition Branch identity.
- Script stdout is parsed as the workflow completion JSON using the same completion contract as agent nodes. Stderr is diagnostics only and is not mixed into completion parsing.
- Invalid stdout, invalid script path, interruption, and execution errors leave the script's current Node interrupted with bounded structured details.
- Script completion applies the selected Transition and creates no retained execution history.
- Resuming an interrupted Script Node runs the script again with its current inputs, the current script path, and the latest outgoing completion requirements.

## Workflow Prompting

- Workflow-controlled agent Sessions use dedicated workflow-mode developer instructions.
- Every Node Transition into an Agent Current Node must steer exactly one target assignment into the target Session before its Exact Execution Scope begins.
- Context-Preservation Mode selects the target Session and assignment template. It does not change the Transition's ownership of assignment delivery.
- When a Node Transition continues a Session during an active model or tool turn, the target assignment must follow the source turn's durable tool result.
- Resume must not steer or append a Current Node assignment.
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
- Unsatisfied Task Dependencies use the same instruction lifecycle as Task
  Comment awareness.
- When one or more direct unsatisfied dependencies exist, new Workflow
  instructions include the current count and `kent task show <task-short-id>`.
- Workflow instructions do not embed related Task bodies or relationship lists.
- Kent never rewrites older model-visible instructions when relationships or
  dependency satisfaction changes.

## Questions And Approvals

- A Workflow Question is a Session Question created through `ask_question`.
- A model does not report `needs_user_input` as a completion status; it calls `ask_question`.
- The Session's current executable Node pauses until the Question is answered.
- All clients use the same authoritative Question and Approval state. A client marks an interaction resolved only after Kent accepts the answer.
- A Question belongs to its Session. Workflow attention refers to that Question and does not create a second Task-owned copy.
- Live Questions and live Approvals exist only within their Exact Execution Scope. A restart does not restore them. Kent interrupts the affected Current Node. Resume closes stale tool operations with a restart outcome before it continues.
- The bounded active Session transcript supplies Question content to read surfaces. An unfinished transcript operation without a matching Exact Execution Scope does not create a live Question or `waiting_question` Task status.
- A pending Workflow Transition Approval belongs to the current Task and survives restart.
- Approval is a Transition Branch property.
- If any branch of a selected Transition requires Approval, the complete Transition waits for one Approval before any target becomes current or begins work.
- A pending Approval freezes the selected Transition, its branches, Workflow Version, source and target Nodes, effective branch configuration, display details, and Context Source.
- A pending Approval also freezes the selected target Assignee and thinking values.
- Kent validates and materializes the selected target Assignee and thinking before it creates the pending Approval.
- Approval inserts the frozen target without consulting the current Workflow graph or role/thinking configuration.
- If later configuration cannot instantiate the already approved Assignee or thinking value, the resulting Current Node reports an ordinary start failure; that failure does not change Approval semantics.
- Later graph edits do not change what the approving caller approves.
- Applied and rejected Transitions are not retained as workflow movement history. Pending Approval state is removed when the Approval applies or a manual move supersedes it.
- A Task awaiting Approval remains at the source current Node and exposes `waiting_approval` status; target Nodes are not current.
- Pending Approvals occur only after a Task has reached an executable Node and therefore always reuse the Task's locked Execution Target.

## Manual Movement

- Manual movement acts on the Task, never on one Current Node. A successful move replaces every origin Current Node.
- Moving a Backlog Task through its Start Node's outgoing Transition is Task Start, not a manual override.
- Dropping a Task onto any Node that is already Current is a no-op, including a drop from one parallel card copy onto another Current Node in the same parallel group.
- A manual move to an Agent or Script Node selects a usable incoming Transition that contains the destination. The Transition does not need to originate from a Current Node.
- Kent selects the Transition automatically when exactly one usable incoming Transition contains the destination. The operator chooses when several are usable, and Kent rejects the move when none are usable.
- The selected Transition is the unit of movement. A serial Transition creates its single target Current Node; a Fan-Out Transition creates every target Current Node and cannot be used to start only one branch.
- A serial incoming Transition inside a Fan-Out branch path is not usable for Manual Move because it cannot recreate the sibling branch positions required by the Join. The operator must select the Fan-Out Transition that starts the complete parallel group or choose a destination outside that parallel section.
- Manual movement applies every selected Transition Branch's Parameters, context behavior, and target requirements as normal Workflow entry would.
- Kent presents every required Parameter value before movement. Values already available from the Task are prefilled and remain editable; the operator supplies unresolved values and may override prefilled values.
- Manual Move presents exposed protected Assignee and thinking Parameters through the ordinary required-value fields and applies the same selection validation.
- Manual Move hides and ignores the protected Assignee value for retained-Session reuse and hides either protected value when its selector resolves automatically from zero or one option.
- A protected value hidden because its selector is disabled, unavailable, or topology-inapplicable is not part of the completion contract and remains an unknown extra value.
- Deliberately selecting an Approval-gated Transition counts as its Approval. Kent applies the move without creating another Approval and clears any older pending Approval.
- Task Start and manual movement into executable work make no Task change while Execution Target selection is required. Dismissal leaves the Task unchanged.
- After required selection, Task Start durably places the Task and acknowledges that placement before Execution Target resolution, filesystem work, setup, Session creation, or runtime startup. Those operations run asynchronously without holding the shared Workflow mutation permit; failure interrupts the placed Current Node through the ordinary runtime-start error path.
- Once a Manual Move is ready to apply and any required Execution Target selection has succeeded, Kent automatically interrupts all live Agent and Script work on the Task, waits for it to stop, revalidates the move, and applies it. A separate Interrupt action is not required.
- Manual Move does not cancel or join a waiting Question scope. The operator must answer the Question or wait for scope retirement before moving the Task.
- Other conflicting lifecycle operations block Manual Move.
- If revalidation or movement fails after live work has been interrupted, the origin Current Nodes remain interrupted and Kent surfaces the move failure instead of resuming them.
- `kent task move` accepts an optional Transition key plus structured values keyed by Node key and output name through inline JSON or a JSON file. Kent selects the Transition automatically when exactly one is usable. Flat `name=value` Manual Move input is unavailable because it cannot distinguish same-named outputs from different Nodes.

## Context Preservation And Bindings

- Each Transition Branch supports `new_session`, `continue_session`, or `compact_and_continue_session`.
- Workflow-created Session copies preserve delegation ancestry and do not reset delegation depth.
- Continuation modes may select `immediate_source`, `node:<node_key>`, `previous_target`, or `previous_target_or_new` as context source.
- `immediate_source` uses the Session bound to the source Current Node during normal completion. During Manual Move, it uses that Session when the source is Current, otherwise the latest retained unscoped Session associated with the selected Transition's source Node.
- `node:<node_key>` selects the latest retained Session associated with the guaranteed-prior agent Node.
- `previous_target` selects the latest retained Session associated with the target agent Node and fails when none exists.
- `previous_target_or_new` selects that Session when one exists and otherwise starts a new Session.
- During parallel work, each Context Source selection stays within the source Current Node's Transition Branch Key.
- Manual movement supports every Context Source. An incoming Transition is usable only when every selected branch can resolve any Session required by its Context Source.
- Manual movement never infers one origin branch from a parallel Task or from the dragged card. During parallel work it does not preserve branch-scoped Session context. When the selected Transition source is not the Task's sole unscoped Current Node, required retained-Session context resolves only from serial associations; branch-scoped-only context makes the Transition unusable. A selected Fan-Out Transition resolves context before its targets receive their new Transition Branch Keys.
- Pending Approvals freeze context-source resolution before Approval. A fallback-to-new result remains `new_session` even if another matching Session appears before Approval, and a selected Session remains fixed if a newer matching Session appears.
- `continue_session` may reuse only a Session whose persisted Assignee identity matches the target Current Node's materialized Assignee.
- Workflow validation rejects statically known Assignee incompatibility, runtime rejects retained-Session Assignee incompatibility, and valid direct continuation preserves the reused Session's Assignee, contract generation, and cache lineage.
- Transition-selected Assignees never rotate or invalidate an established Session's prompt-cache lineage.
- A retained Session may adopt the target Current Node's materialized thinking without rotating or invalidating its prompt-cache lineage.
- `compact_and_continue_session` compacts the reused Session and establishes the target Current Node's materialized Assignee and thinking in a fresh contract generation, resolving current role configuration for model/provider setup, generation parameters, capabilities, enabled tools, native web-search mode, prompt snapshots, context budget, and cache lineage.
- `new_session` establishes the target Current Node's materialized Assignee and thinking at its fresh context boundary and resolves current role configuration for that Assignee.
- Nodes own no agent input or output contract. Transition Branches exclusively declare the Parameters they provide to their targets.
- Prompt placeholders validate against the prompt-owning Transition Branch's Parameters through `.Params.<parameter_key>`.
- Applying a Transition materializes each branch's declared Parameters for its target. Prompt rendering uses those values and never searches discarded execution history.
- If the latest Workflow definition requires an already-current executable Node to have a Transition-owned Parameter that was not materialized when the Node was entered, that Current Node cannot Resume and reports a typed validation error. Kent does not reconstruct discarded Workflow history, and another selected parallel Current Node remains independently admissible.
- The Start Node's outgoing Transition cannot declare Parameters and should use task fields such as `.TaskTitle` and `.TaskBody`.
- Kent derives Parameter flow and completion requirements from Transition Branch declarations, Workflow structure, and Join sources.

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
- Workflow automation starts Agent Nodes within available agent capacity and prefers to continue related work on the same Task. `[workflow].concurrency` limits these agent runs only.
- Script Nodes do not wait for or consume agent-run capacity. Kent does not limit concurrent Script Node runs.
- When Agent and Script Nodes are both eligible within their applicable capacity, Kent gives neither kind priority. The same-Task continuation preference applies across both kinds.
- Explicit Start, Resume, approval, and executable manual move may exceed the agent concurrency limit without preempting existing work.
- Resume returns after it durably requeues the interrupted Current Nodes and queues their explicit starts.
- Resume does not wait for Execution Target restoration, Session setup, or agent or Script startup.
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
- Completion can change a Task only from the matching Exact Execution Scope or from one unambiguous idle executable Current Node. A stopped scope and a non-current Node cannot change Task state.
- Completion replaces source Current Nodes, materializes target inputs, and adds target Current Nodes as one atomic change.
- Runtime failures, crashes, interruptions, and fixable start-validation blockers leave the affected Current Node interrupted with a reason.
- `failed` is reserved for unrecoverable corrupted Workflow state.
- Kent retains no completed execution or Workflow-movement history.
- Tasks support Interrupt and Delete. They do not have a separate Cancel operation.

## Task Status And Listing

- Task Search, Task detail, Workflow boards, and Task lists use one server-owned authoritative Task-status projection derived from Current Nodes, pending Workflow Transition Approvals, and current live activity.
- The projection supplies primary status, every applicable attention kind and reference, available Task actions, and the exact Current Node, Session, or Script targets for those actions. A surface may omit fields it does not expose, but does not independently recompute lifecycle-sensitive facts.
- Each request combines one durable Task-state view with one Immutable Live Snapshot, then derives the complete projection from those views. A Workflow lifecycle change between the views may briefly combine durable and live facts from different moments.
- Each request is independent. Kent does not synchronize separate Search, List, Board, and Detail requests, so they may observe different lifecycle moments.
- Agreement among List/Search status filtering, status sorting, and returned status is not a product invariant. Each evaluation still follows the authoritative Task-status semantics.
- Projected actions and targets are hints that may become stale after the response. Each Task-changing operation checks its authoritative state again before changing the Task.
- If required durable or live facts cannot be read, the request fails instead of returning a partial projection or using a surface-specific fallback.
- Workflow attention has a global paginated Inbox and a bounded Task-specific feed. It has no Project-specific feed.
- Workflow attention is task-scoped. It includes only unresolved Questions, unresolved Workflow Approvals, and executable Nodes interrupted by errors.
- Workflow validity is not attention and does not create Inbox items.
- Core Task detail includes an unresolved-attention count but not the attention items. It does not scan transcript history.
- The Task-specific attention feed can read the newest active transcript segment to recover unresolved Question content. Desktop Task detail loads this feed independently so it does not delay core Task detail.
- Task Activity is a server-paginated projection of durable Comments and retained Session creation. It contains no workflow movement, Node completion, interruption, attempt, or diagnostic history. Clients render Session creation as localized `Session started` activity.
- Task detail reports the total retained Session count. It provides direct Open and Interrupt actions only for agent Sessions with live Exact Execution Scopes; non-live Sessions remain available through the Session picker.
- Desktop shows Task-wide Interrupt when several Exact Execution Scopes are live or a Script Node is live.
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
- Each Project-wide Task-list request decides whether rows need Workflow names from the complete current filtered result set. Workflow-name visibility may change between offset requests when the matching set changes.
- Human task-list rows include workflow names only when the filtered query can return tasks from multiple workflows. JSON task items always include their bare workflow UUID.
- A project with no linked workflows, an explicitly selected workflow that is not linked to the project, and workflow-relative operations without a workflow selector return distinct typed actionable errors.
- No-linked-workflow recovery directs the operator to create and link a workflow or list and link an existing workflow before retrying.
- Explicit not-linked recovery offers project workflow discovery and retry with a linked workflow, or linking the selected workflow to the project.
- Workflow-relative column recovery offers project workflow discovery and a task-list retry with an explicit workflow while preserving the parsed filters.
- Task-list status sorting follows primary typed-status precedence; workflow-narrowed column sorting follows workflow column position.

## Task Search

- Task search reuses the authoritative Task status defined above.
- [Task Search](task-search.md) owns the complete query, matching, ranking, pagination, output, consistency, and compatibility contract.
- Search returns Task status from the server-owned Task-status projection.
- Each response is point-in-time consistent for matching text, counts, filters, and Task metadata. It combines that durable view with one separately captured Immutable Live Snapshot. A Workflow lifecycle change between the views may briefly combine durable and live facts from different moments.

## Execution Targets And Worktrees

- A workflow execution target policy is evaluated only when an unlocked task first reaches an executable node through task start or manual movement.
- No managed worktree uses the source workspace as the execution root, supports non-Git workspaces, and creates no branch or worktree and runs no worktree setup.
- A no-managed-worktree target follows the task's current source workspace. Changing that workspace intentionally changes later execution roots.
- Source `HEAD`, repository default branch, and custom Git ref resolve to an immutable commit before managed-worktree creation. A custom ref accepts any Git revision that resolves to a commit.
- Repository default branch uses configured local remote-HEAD metadata: `origin` when configured, otherwise one unambiguous configured remote HEAD. Kent does not contact remotes or guess branch names; missing or ambiguous metadata makes the configured target unavailable.
- `ask_on_first_execution` and an unavailable configured target use the same task-local selection flow. They offer no managed worktree, source `HEAD`, repository default branch, and custom Git ref.
- For `ask_on_first_execution`, repository default branch is preselected. For an unavailable configured target, the configured mode and custom-ref input remain selected when useful; otherwise repository default branch is preselected.
- An unresolvable configured target asks the operator to select a concrete target and explains which configured target failed and why, except during Task Start where resolution occurs after placement and failure interrupts the placed Current Node. Resuming that unlocked Current Node requests a concrete target before it requeues.
- Selection-required results distinguish two reasons: the Workflow requires selection, or the configured target is unavailable. Every selection flow offers all four concrete modes.
- Failure to resolve an explicitly selected custom ref is a validation failure. During Task Start that failure occurs asynchronously after placement; it does not recursively request selection or fall back to another target. A later Resume may select another concrete target while the Task remains unlocked.
- A Task locks target-selection provenance when preparation establishes the first Execution Root. Later Nodes and retries reuse the locked mode and managed requested/resolved facts despite Workflow edits or Git ref movement.
- A Task with historical managed-worktree facts but no locked execution-target provenance does not infer an execution root from its recorded `HEAD`; it remains readable but requires an explicit target selection before execution.
- An unlocked Task follows its configured Workflow execution-target policy. Kent does not use historical managed-worktree facts as a source-`HEAD` fallback.
- Managed targets use the same creation, setup, and collision behavior as other Kent-managed worktrees. Before Kent schedules the first executable Current Node, it loads worktree setup settings from the Task's source workspace. A configured setup script must succeed for a worktree created by that operation.
- Every Kent-managed Worktree root must remain inside the server-configured Worktree base namespace and outside its source Workspace. An explicit managed Worktree root that violates either condition is rejected before Worktree creation.
- A persisted managed Worktree root outside the server-configured namespace causes Session activation and Worktree restoration to fail. Kent does not migrate that root automatically.
- Managed worktree setup failure during Task Start leaves placement applied and interrupts the placed Current Node. For other initiating actions it leaves the action unapplied and unscheduled. Any created worktree remains available for inspection or manual repair.
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

- A Task owns its Current Nodes, any entering Transition Branch, current inputs, optional Session associations, and pending Approval.
- Current Nodes have no independent product identity.
- Every Agent Current Node materializes and persists its effective Assignee and thinking when it is created.
- During upgrade, the Session resolved as continuation context does not by itself determine the target Assignee.
- An unstarted target that will establish a fresh or compacted Session contract, or that requires retained-context Assignee compatibility, materializes the required target Agent Node fallback. An unstarted target-owned retained continuation preserves the retained Session Assignee, treating an absent retained role as Kent's canonical default role.
- A target execution already bound to a Session preserves that Session's Assignee. A pending Approval target has not started and therefore derives its Assignee only from its frozen Context-Preservation Mode and Context Source resolution.
- Pre-feature state migrates with absent/no-thinking because it had no frozen thinking value. Migration does not validate preserved role availability against current configuration; an unavailable role becomes an ordinary Current Node start failure after startup.
- Later Workflow edits do not change an admitted Agent Current Node's materialized Assignee or thinking.
- Current Node read models expose the materialized Assignee and thinking.
- Desktop Task detail renders the materialized Assignee and thinking for Agent Current Nodes and omits them for non-Agent Current Nodes.
- An executable Current Node retains only the state needed to execute or resume. A Script Node has no Session.
- Applying a Transition replaces the source Current Nodes, supplies target inputs, and adds target Current Nodes as one atomic change.
- Kent does not retain applied or rejected Transition history, completed Current Nodes, or execution-attempt records.
- A pending Approval is the only retained frozen Transition. Kent removes it
  after approval or a superseding manual move. Kent has no public Transition
  Approval Reject action.
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
- Task Delete removes Task Dependencies according to the Task Dependencies
  contract.
- Repeating Task cleanup for an artifact that is already absent succeeds.
- Saving a Workflow checks the expected Workflow Version, validates the Workflow Draft, reports active blockers, and requires confirmation for destructive removals.
- A successful Workflow save applies details and graph changes as one atomic change.
- Saving a Workflow graph never deletes or moves Tasks.
- Executable context uses the Task, the latest Workflow definition, current inputs, Execution Target, and selected Context Source Session. It does not search discarded execution history.

## Compatibility Data

- A legacy canceled Task moves to terminal Node `done` when that Node exists.
- If that Workflow has terminal Nodes but no `done` Node, Kent preserves the
  Task's unique valid active terminal when one exists. Otherwise Kent chooses
  one terminal Node deterministically.
- If its Workflow has no terminal Node, Kent removes the Task after making its
  Sessions workflow-neutral and preserves its worktrees and other external
  artifacts.
- `source_url` remains an optional structured Task field.
- Task Short ID remains durable product data.
- Context Source selection uses retained Sessions and does not depend on discarded execution records.
- Pending Approval retains the exact Transition facts that the operator is approving. Ordinary work uses current inputs and the latest Workflow definition.
- When legacy serial state contains a persisted pending Approval and a conflicting serial current position, Kent retains the Approval source as the Task's only Current Node.
- When legacy serial state has no pending Approval and contains several active Start or Terminal Nodes, Kent retains the placement with the latest update time, then latest creation time, then greatest identifier.
- Task Comments retain their author identity when available.
- Session listings retain the first prompt preview.
- Sessions retain unsent input drafts for recovery.

## CLI Surface

- The Workflow and Task CLI provides complete control for operators and agents.
- Agents can build and edit complete Workflow definitions with high-level commands. Import and export are separate sharing features.
- CLI command grouping is not a compatibility contract. The documented behavior, accepted data, and machine-readable output are compatibility contracts.
- CLI output includes stable identifiers needed by later commands.
- `kent task wait <task>` and `kent task watch <task>` resolve a Project-scoped Task and observe it through server-owned event notification. They never poll read commands or mutate the Task.
- Task wait ignores Questions, access requests, transition approvals, and successful intermediate node completion. It returns for a current-work interruption, a current Session or Script execution error, or Task `done`.
- Task watch returns for a Task Question or access request, current-work interruption, current Session or Script execution error, or Task `done`. It ignores Workflow Transition Approvals and successful intermediate completion.
- Task Search pagination is defined exclusively by the owning Task Search specification.
- Every other paginated Workflow and Task CLI command uses zero-based `--offset` and `--limit`. It exposes neither page tokens nor page numbers.
- An omitted offset starts at the beginning. Any non-negative offset is accepted. A negative offset is invalid.
- `--limit` defaults to `100` and accepts values from `1` through `100`.
- Callers may change the limit between requests. An offset at or beyond the current end succeeds with the command's existing empty-result output and no next offset.
- When more results exist, `next_offset` equals the request offset plus the number of results returned.
- Human output writes ``Next offset: `<n>` `` to stderr only when more results exist. Machine-readable request contracts use `offset` and `limit`; responses use optional `next_offset`. An absent next offset is omitted or null and is never zero.
- Affected shared API requests may omit both `offset` and `limit`. An omitted offset starts at the beginning, an explicit offset of zero is valid, and an omitted limit defaults to `100`. Supplied limits must be from `1` through `100`. The API represents omitted numeric values as absent or null rather than as zero.
- Each request applies its offset to the current results for that request's Project, Workflow, selectors, filters, and sorting. Callers repeat those query choices when continuing. Kent does not bind an offset to a previous query.
- If items are inserted, removed, or reordered between offset requests, later results may repeat or skip items.
- The server keeps pagination memory bounded by the requested limit. It does not retain page contents or pagination state between requests and does not persist pagination state.
- `kent workflow delete <workflow>` reports the deletion impact and makes no changes unless `--confirm` is present. A confirmed deletion submits the previewed Workflow Version and affected Project, Project Workflow Link, and Task counts; if the impact changes or deletion has blockers, Kent deletes nothing and reports the blockers.
- The plain-text `kent task complete` acknowledgement omits identifiers. JSON completion output remains machine-readable.
- Project label catalog and task-assignment commands live under `kent task label`; there is no top-level label command. Catalog commands create, list, rename, and delete labels in the selected Project. Human catalog output includes readable names and stable UUIDs.
- Label selectors use repeatable `--label <name-or-uuid>`. Task-list negative Label conditions use repeatable `--not-label <name-or-uuid>` with the same selector resolution. Canonical UUID v4 text selects by identity; every other value is trimmed and matched against the complete Project Label name with the Label catalog's case-insensitive Unicode comparison. Label selector values are literal and are never comma-split.
- `kent task label add <task>` and `kent task label remove <task>` require one or more label selectors and apply all resolved membership changes atomically with idempotent add/remove behavior. Label names resolve against the Task's actual Project; `--project` scopes project-short-ID lookup. Task creation accepts the same repeatable selector and atomically assigns existing labels.
- Every catalog and assignment command accepts `--json`. Catalog JSON returns label records for create, rename, and list, and the deleted label ID for delete. Assignment JSON returns the task ID and authoritative resulting label IDs. Human assignment output is a short acknowledgement.
- `kent workflow edge add|update` accepts `--assignee-selection configured|previous_node` and `--thinking-selection configured|previous_node` for one Edge. Omission on add means `configured`; `previous_node` initializes a missing default protected Parameter, while `configured` retains an existing protected Parameter dormant.
- `kent workflow edge add|update` accepts `--target-assignee-param <key>=<description>` and `--target-thinking-param <key>=<description>` to create or edit the corresponding protected Parameter while enabling it in the same command or while it is already enabled. An empty description after `=` is valid.
- Repeatable `--param <key>=<description>` and `--clear-params` mutate ordinary Parameters only and never delete or convert protected Parameters.
- Workflow Node CLI mutation keeps the Agent Node's configured Assignee required, uses the existing `--agent` flag, and does not enable selection for incoming Edges.
- Workflow inspection identifies protected Parameter purposes. Human and JSON `kent task show` expose effective Assignee and thinking for Agent Current Nodes and omit them for non-Agent Current Nodes.
- Human task show/list output adds one `Labels:` line only for assigned labels and quotes every name. Task show/list JSON exposes one `label_ids` field and does not duplicate assignments as named objects.
- Task-list Label filtering uses repeatable literal `--label` selectors for included conditions and repeatable literal `--not-label` selectors for excluded conditions. `--label-match any|all` combines every included and excluded condition and defaults to `any`. `--unlabeled` selects Tasks with no assignments and is mutually exclusive with both selector flags and an explicitly supplied match mode. An explicit match mode without either selector flag is invalid.
- Every selector in one command must resolve before task creation, assignment, or listing proceeds. Selector-resolution failure reports every unresolved selector and never ignores or partially applies the input.
- `kent task list` exposes one typed task status. `--status` filters primary status, `--attention` filters typed attention, and `--column` filters workflow node keys.
- `kent task list --unblocked` includes Tasks with zero unsatisfied direct Task Dependencies. `kent task list --blocked` includes Tasks with one or more unsatisfied direct Task Dependencies. The two flags are mutually exclusive.
- `kent task list` filters and sorts before pagination. Multiple values for one filter are ORed. Different filter types are ANDed. A Task with several Current Nodes exposes all matching column keys in Workflow order.
- `kent task list` default ordering is `status:asc,updated:desc`, where `status` uses primary typed-status precedence and `updated` is newest-first. Custom `--sort` accepts up to seven ordered `field:direction` selectors for `created`, `updated`, `status`, `column`, `title`, `labels`, and `short_id`; selectors can be comma-separated in one flag and may be supplied by repeated flags.
- `kent task complete` accepts dynamic parameter flags, repeatable `--param name=value`, and `--json`/`--json-file` completion payload input. JSON input modes print JSON responses.
- Plain-text `kent task complete` output is a model-facing handoff acknowledgement: `Completion scheduled. The transition <source display name> → <destination display name> will execute now. Your next agent turn will begin with the next workflow instructions.`
- The acknowledgement uses the target node display name for an ordinary transition. A fan-out uses its shared target node-group display name when present and otherwise its transition display name.
- The same acknowledgement is used for agent-session and forced human completion. It always promises a next agent turn regardless of context-preservation mode or whether another turn occurs, and it does not expose approval or transition state.
- JSON completion output retains its existing field set and does not include the plain-text acknowledgement.
- `kent task edit <task>` changes a Task's title, body, or source workspace. It requires at least one of `--title`, `--body`, `--body-file`, or `--source-workspace`. It preserves the current title when `--title` is absent. Agents can use it. `--json` prints the result.
- `kent task create` and `kent task edit` accept `--source-workspace` as either a workspace id or a path; a path is resolved through its project binding. An omitted source workspace leaves it unchanged on edit.
- Workflow/task CLI commands report remote-close failures to stderr after command work finishes. A close failure does not change a successful exit code, and an operation failure keeps its existing nonzero exit code.
- Task dependency commands use canonical group `kent task dep`.
- `kent task deps`, `kent task dependency`, and `kent task dependencies` invoke
  the same command group but are not shown in help or documentation.
- `kent task dep add --blocker <task> --blocked <task>` adds one directed
  relationship.
- `kent task dep remove --blocker <task> --blocked <task>` removes one directed
  relationship.
- `kent task dep list <task>` inspects both direct relationship directions.
- `kent task dep list <task> --direction blocks|blocked-by` inspects one
  direction.
- Every dependency command accepts `--project` for Task Short ID resolution and
  `--json` for machine-readable output.
- Dependency add and remove resolve both Task selectors before mutation.
- Plain dependency add and remove output is exactly `done`.
- Dependency add JSON returns Blocker Task ID and Short ID, Blocked Task ID and
  Short ID, and typed outcome `added` or `already_present`.
- Dependency remove JSON returns the same identities and typed outcome `removed`
  or `already_absent`.
- Dependency mutation JSON uses `outcome`, `blocker_task_id`,
  `blocker_short_id`, `blocked_task_id`, and `blocked_short_id`.
- Dependency list JSON uses top-level `task_id`, `short_id`, and `directions`.
- Each dependency direction object uses `direction`, `total_count`, and
  `items`.
- A compact dependency Task item uses `task_id`, `short_id`, `title`,
  `workflow_id`, and canonical typed `status`.
- A `blocked-by` direction also uses `unsatisfied_count`.
- Each `blocked-by` item also uses typed `satisfaction`.
- Empty directions are omitted. A Task with no relationships returns
  `directions: []`.
- The cardinality limit makes every returned direction complete. Dependency
  list output has no continuation token and the command accepts no page token.
- Human dependency-list output omits empty directions and uses this shape:

  ```text
  Blocks <count> tasks:
  <short-id>: <title> (<status>)
  ...
  Blocked by:
  <short-id>: <title> (<status>)
  ...
  ```

- Human `kent task show` uses the same dependency sections and shows every
  relationship permitted by the cardinality limits.
- Human dependency sections order unfinished related Tasks first and then Task
  Short ID.
- Human `kent task show` omits dependency output when both directions are empty.
- `kent task show --json` never embeds dependency Task items.
- When at least one relationship exists, `kent task show --json` includes only
  direct Blocker Task count, direct unsatisfied Blocker Task count, and directly
  blocked Task count.
- The JSON field is `dependencies` with `blocker_count`,
  `unsatisfied_blocker_count`, and `blocked_task_count`.
- `kent task show --json` omits dependency summary when all three counts are
  zero.
- `kent task start` and `kent task move` accept `--ignore-dependencies`.
- Without that flag, an otherwise valid Start or executable Manual Move with
  unsatisfied dependencies returns a typed `dependency_confirmation_required`
  outcome containing only the unsatisfied count.
- Dependency-confirmation JSON uses `outcome` and
  `unsatisfied_dependency_count`.
- Human dependency-confirmation output identifies the count, directs the
  operator to `kent task show <task>`, and gives the
  `--ignore-dependencies` rerun.
- Human and JSON dependency-confirmation outcomes exit nonzero because the
  requested action was not applied.
- `--ignore-dependencies` applies only to that command invocation and does not
  remove relationships or suppress later Workflow dependency awareness.
