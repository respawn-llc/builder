---
name: kent-workflows
description: How to use the Kent CLI to author and edit workflow graphs — nodes, edges, transitions, parameters, context modes, and project links. Use when the user asks to define, build, inspect, or modify Kent workflows.
---

For creating, inspecting, and managing tasks, comments, and task automation against an authored workflow, use the `kent-tasks` skill.

## Workflow Concepts

- A workflow is a graph of node states connected by transition branches.
- A task is the durable user-facing unit of work moving through one workflow.
- A node is a visible workflow state. `start` is where tasks are created, `agent` node runs Kent sessions, `join` waits for parallel branches of a group, and `terminal` is a sink where automation stops.
- An edge connects a transition group to a target node. A transition group is selected by a `transition_id`; if the group has multiple edges, it fans out to parallel target nodes.
- A graph revision increments when graph-affecting edits are made.
- Model-facing keys such as node keys, edge keys, transition IDs, and parameter keys must be stable lower-case keys matching Kent's model-key rules. Prefer `implement`, `review`, `done`, `needs_changes` over display labels with spaces.

Authoritative command details are the live CLI:

```bash
kent workflow --help
```

## Build A Workflow

The CLI authoring path is: create a workflow, add or update nodes, add or update edges, link it to a project, then validate it.

```bash
kent workflow create --description "Implement and review changes" "Implementation Review"
kent workflow inspect "Implementation Review"

kent workflow node add "Implementation Review" --key implement --kind agent --agent <implementer-role>
kent workflow node add "Implementation Review" --key review --kind agent --agent <reviewer-role>

kent workflow edge add "Implementation Review" --from backlog --transition start --edge-key start --to implement --context new_session --prompt "Implement the task."
kent workflow edge add "Implementation Review" --from implement --transition review --edge-key review --to review --context new_session --prompt "Review the implementation."
kent workflow edge add "Implementation Review" --from review --transition done --edge-key done --to done --context new_session

kent workflow link . "Implementation Review" --default
kent workflow validate "Implementation Review" --mode execution
```

To understand what `--agent` roles are available (the reminder in your context might not list all of them), inspect the repo-local and user global `config.toml` files. More info in the `kent-dogfooding` skill.

Important CLI behavior:

- Every `workflow` command prints readable plaintext by default and accepts `--json` for machine-readable output. Use `--json` in scripts to read ids and versions; use the default output interactively.
- `workflow create` auto-creates the initial backlog/start and done/terminal shape; inspect after create before adding duplicate start or terminal nodes.
- Workflow references can be exact workflow IDs or exact workflow names. Prefer IDs in scripts and exact names in interactive work.
- Agent roles are stored exactly as provided. Replace role placeholders with configured subagent role names instead of assuming `default` is normalized.
- `workflow node add` returns the generated node id. Edges are authored with node keys via `--from` and `--to`.
- `workflow edge add` creates or reuses the transition group for the given source node and transition ID, then adds an edge to that group.
- Agent-targeting edges require `--prompt <text>`. Omit `--prompt` for edges targeting `start`, `join`, or `terminal` nodes.
- Script nodes use `--kind script --script-path <path>` and can appear anywhere an agent node can appear. Absolute paths resolve on the Kent server; relative paths resolve against the task managed worktree when the script runs.
- Link, unlink, and set project defaults with `kent workflow link`, `kent workflow unlink`, and `kent workflow default`. This sets up bindings between a project (repo, workspace) and a workflow, and enables sharing of workflows.
- `--transition-description <text>` sets the model-facing transition description on the edge's transition group. The agent will see it when it completes the task. Use it to tell the agent when to pick this transition over its siblings.
- `--param <key>=<description>` (repeatable on `edge add` and `edge update`) declares a transition parameter the source agent must produce when taking the transition. On `edge update`, passing `--param` replaces the full parameter set; `--clear-params` removes all parameters. Omitting both preserves existing parameters.
- Draft workflows can be saved and linked while semantic validation fails. Validate before task creation as a best practice; workflow automation requires execution validation before it starts.

## Script Nodes

Use a script node when a workflow step should run a deterministic executable on the Kent server instead of starting an agent session.

```bash
kent workflow node add "Implementation Review" --key release_notes --kind script --script-path scripts/release-notes
kent workflow edge add "Implementation Review" --from implement --transition release_notes --edge-key release_notes --to release_notes --context new_session
```

Kent executes the script directly, without a shell wrapper. Stdin is one JSON object: incoming workflow parameter values are top-level properties, and `_kent` contains runtime identifiers such as `run_id` and `placement_id`.

Stdout must be workflow completion JSON. Stderr is diagnostics only. A typical stdout payload is:

```json
{
  "transition": "done",
  "commentary": "Generated release notes.",
  "release_notes_path": "docs/release-notes.md"
}
```

If there is only one outgoing transition, `transition` can be omitted. Include declared transition parameters as top-level string fields. For multiple outgoing transitions, fields for unselected transitions should be `null`.

Validate script workflows in execution mode before using them:

```bash
kent workflow validate "Implementation Review" --mode execution
```

Execution validation and task start require the selected script path to exist on the Kent server, be a file, and be executable. Relative script paths cannot be fully checked until the task managed worktree exists.

## Edit Existing Workflows

Use the edge/node CRUD commands to manage the workflow:

```
kent workflow node update "Implementation Review" implement --agent <implementer-role>
kent workflow edge update "Implementation Review" edge-abc123 --transition needs_review --transition-display-name "Needs Review" --edge-key review --to review --context compact_and_continue_session --prompt "Review the implementation."
```

Node update flags are partial: omitted scalar fields keep current values. Edge update flags are partial; provided `--prompt` replaces the branch prompt.

Validate after each meaningful graph change:

```bash
kent workflow validate <workflow> --mode draft
kent workflow validate <workflow> --mode task_creation
kent workflow validate <workflow> --mode execution
```

Use `draft` while authoring, `task_creation` before creating tasks, and `execution` for final handoff to the user.

## Context And Approval

Each edge requires a context mode:

- `new_session`: start a fresh Kent session and inject task metadata plus outputs from the previous node. This is a double-edged sword: The new session starts with the lowest token count, giving the agent the most memory space for their task, keeps it free from bias, and does not invalidate caches, keeping costs low, but the agent must receive **all** the necessary context to effectively complete their task in the node prompt and input parameters, and they will have to gather context from the workspace **from scratch** to orient themselves (possibly negating token and cost benefits gained). This means `new_session` is a good fit for isolated tasks, verification, self-contained units of work, e.g. code review, QA runs, requirement verification; and a poor fit for continuation of existing work, next-phase of a plan, or highly contextual tasks that need carry-over of information.

- `compact_and_continue_session`: ask the previous agent for a handoff, then start a new session with the next node prompt, their handoff, and task metadata. This is the middle ground - it frees context, but keeps only the important details about the previous agent's work state and actions in context, then starts a new session. It does not invalidate caches, allows changing the subagent role, and still keeps a lot of free memory space available. However, this incurs additional costs to **handoff the previous session**, leaves the previous session cache abandoned, and the context preservation is imperfect. Use this when source node is a large chunk of work (such as feature implementation or research task) and the next one continues their work in another direction (does not benefit much from the full context).

- `continue_session`: directly continue one of the previous Kent sessions, keeping the **same subagent role**, conversation history, state, and cache. This mode directly gives the agent the next task as a message and runs it. This is best when the task that the source node completed was relatively small (fitting in roughly one agent memory window) and the next node directly continues it or some other previous node. For example, an `investigation` phase that continues into `planning` phase, or `implementation` node that teleports context into another `implementation` node as a loop after code review findings were posted.

Note that to prevent high costs due to cache invalidation, only compact and new_session context modes allow changing the subagent role of the target node when transitioning. For example, you cannot continue from `coding` to `code_review` roles unless you compact or start a new session.

Continuation modes also have a context source. Use `--context-source <source>` on `workflow edge add` or `workflow edge update`.

- `immediate_source`: continue or compact the session from the node that selected the transition. This is the default.
- `node:<node-key>`: continue or compact the latest completed run of a specific guaranteed-prior agent node. Use this when the target needs context from an earlier node rather than the immediate source.
- `previous_target`: continue or compact the latest completed run of this edge's target node. Use this only in guaranteed loops where the target always ran before the source. If no completed prior target run exists, the transition fails.
- `previous_target_or_new`: continue or compact the latest completed run of this edge's target node when one exists; otherwise start the target as an effective `new_session`. Use this for optional re-review loops where the first pass should start fresh and later passes should continue the prior review session.

Examples:

```bash
kent workflow edge update "Implementation Review" edge-review \
  --context continue_session \
  --context-source previous_target_or_new

kent workflow edge update "Implementation Review" edge-rework \
  --context compact_and_continue_session \
  --context-source previous_target
```

Context source constraints:

- `new_session` uses only `immediate_source`; source choices are meaningful only for continuation modes.
- Start/backlog transitions use `immediate_source`.
- `previous_target` requires an agent target that dominates the source node in the workflow graph.
- `previous_target_or_new` requires an agent target but does not require dominance.
- `node:<node-key>` requires an agent node that is guaranteed to run before the transition source and is not the edge target.
- Manual task moves are blocked for `node:<node-key>`, `previous_target`, and `previous_target_or_new` continuation sources.

Use `--requires-approval` when a transition must stop for human review before the target node starts. Pending approvals freeze the selected target branches and their context-source resolution, so later workflow edits or later completed runs do not change what approval starts. This can happen asynchronously thus causing cache invalidation, so approvals across `continue_session` are suboptimal.

## Completion modes

The completion mode controls the technicality of how an agent node signals task completion.

Set it per node with `--completion-mode <mode>` on `node add` or `node update`. Omit the flag to default to user-configured default or resolve automatically based on surrounding configuration.

- `auto`: resolve the effective mode based on configuration. This is the default and the right choice for most nodes, because it dynamically applies the rules below.
- `structured_output`: provider-native structured output. Lowest-friction on capable providers, but fails run start when the provider does not support it, and **causes full cache invalidation** when applied to continued sessions.
- `tool`: completion via the dynamic `complete_node` tool. Use it for tool-driven completion on providers without structured-output support. Also causes **full cache invalidation** for continued sessions.
- `shell_command`: the agent runs `kent task complete` from its shell. Requires a runtime `shell` tool to be enabled in the subagent role config, but the shell tool gives the agent full access to the host system. Prefer this when the workflow contains `continue_session` transitions, because this mode does not cause cache invalidation.
- `unstructured_output`: the agent tries a best-effort JSON submission as its answer. This is the most fragile configuration, use it only if you must use `continue_session` for the node chain and the agents there do not have the `shell` tool that'd have enabled the `shell_command` mode.

## Transition Prompts And Parameters

The transition prompt is the task the target agent runs; parameters are the typed outputs the source agent must produce to carry context across the edge.

```bash
kent workflow edge update "Implementation Review" edge-abc123 \
  --transition-description "Implementation is complete; send for review." \
  --param "plan_file_path=Path to the plan document the implementer followed." \
  --prompt "Review the changes against the plan at {{.Params.plan_file_path}}."
```

Prompt placeholders are Go template fields:

- Built-in task and node fields: `{{.TaskId}}`, `{{.TaskShortId}}`, `{{.TaskTitle}}`, `{{.TaskBody}}`, `{{.NodeId}}`, `{{.NodeKey}}`, `{{.NodeDisplayName}}`.
- This transition's own parameters: `{{.Params.<parameter_key>}}`. Join-to-agent prompts read aggregated branch parameters the same way.
- A guaranteed-prior transition's parameter: `{{.Params.<transition_id>.<parameter_key>}}`. A transition is guaranteed-prior only when every path from `start` to the prompt-owning edge's source passes through it. Use this to reference an earlier output instead of re-declaring the same parameter.

Use parameters together with prompt placeholders to build dynamic task prompts, supplying the context for the next agent node to effectively complete its job. More guidance in the `prompting` skill.

- Parameters are string-only and required once declared.
- Every parameter the source agent provides forms its provision contract: the same parameter key declared on more than one transition out of one source node, or on more than one branch of one fan-out transition, must use an identical description, otherwise the provision fields conflict.
- Declare a parameter on the transition whose source agent can produce it. Prefer referencing a guaranteed-prior transition over re-threading the same value, but re-declare locally where the graph converges or loops and no single upstream transition dominates.

## Practical Authoring

For workflow authoring requests:

1. Inspect existing workflows and project links with `kent workflow list`, `kent workflow inspect`, and the current project context.
2. Create or add graph pieces using stable keys. Keep display names human-readable and keys machine-stable.
3. Validate in the strictest mode that matches the user's intent.
4. Link the workflow to the project and set it as default when the user wants new tasks to use it.

Once the workflow validates, create a smoke-test task and inspect it via the `kent-tasks` skill.
