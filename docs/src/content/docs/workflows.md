---
title: Workflows
description: Build reusable agent workflows, edit them in Kent Desktop, and run tasks through them.
---

Kent workflows are reusable process graphs for agent work. A workflow defines where a task starts, which agent roles work on it, which choices those agents can make, when humans approve a step, how parallel branches join, and where automation stops.

Workflows are linked to projects. Tasks live in projects, then move through the linked workflow as Kent starts sessions, manages worktrees, collects transition outputs, asks questions, waits for approvals, and records activity.

```text
Backlog -> Plan -> Implement -> Review -> Done
                         ^          |
                         |          v
                         +----- Needs changes
```

## 1. Choose How To Build

### Ask Kent To Create It

The easiest path is to ask Kent to model your existing work process as a workflow. Kent agents can inspect your repo, agent roles, skills, slash commands, project conventions, and past sessions, then create the reusable workflow definition for you. After that, use Kent Desktop to review and adjust the graph.

Example prompt:

```md
Hey Kent, use the Kent workflows skill. Study my existing Claude Code workflow, agent configurations, slash commands, skills, and session logs that represent my real workflow. Turn that work process into an automated Kent workflow for this project.

Include the roles, nodes, transitions, prompts, parameters, context modes, approvals, and completion modes you recommend. Link it to this project, set it as the default if it is ready, and tell me what I should review in the desktop workflow editor.
```

This path works best when you describe the real decision points in your process: when implementation is done, what a review must return, when QA is required, what counts as shipped, and where you want explicit approval.

### Build Or Edit In Kent Desktop

Use Kent Desktop when you want direct control over the workflow definition. The Workflow Library manages reusable workflow definitions. A project links one or more workflows and can mark one as the default workflow for new tasks.

From a project, create or link a workflow, open the workflow editor, then edit the graph. Agent-generated workflows follow the same path: review the workflow, fix validation issues, adjust prompts, save, and run tasks from the project board.

![Kent Desktop workflow editor showing a workflow graph and transition inspector.](/desktop/desktop-workflow-editor.webp)

## 2. Set Up Agent Roles

Workflow agent nodes run existing Kent subagent roles. Create roles for the specialists you want in your process, then choose those roles in the node's Assignee field.

```toml
[subagents.implementer]
description = "Implements approved tasks and leaves reviewable changes."
model = "gpt-5.6-sol"
thinking_level = "high"
system_prompt_file = "agents/implementer.md"
agent_callable = true

[subagents.reviewer]
description = "Reviews changes and returns actionable findings."
model = "gpt-5.6-sol"
thinking_level = "xhigh"
system_prompt_file = "agents/reviewer.md"
agent_callable = true
```

Workflow nodes do not define their own model, provider, tool, or auth overrides. Those belong to subagent roles. See [Headless runs](../headless/#subagent-roles) for the role configuration reference.

## 3. Understand The Graph

### Workflow, Project, And Task

- A workflow is the reusable graph definition.
- A project links workflows, provides workspaces, and owns the task board.
- A task is the durable unit of work that moves through one workflow.
- A run is one execution attempt for one executable node. Agent runs start or continue Kent sessions. Script runs execute a local server-side script.

Creating a task puts it in Backlog. Starting the task applies the workflow's start transition and begins automation. This makes it safe to collect work in Backlog before the workflow is fully executable.

### Nodes

Nodes are workflow states. Visible executable and terminal nodes become board columns.

| Node kind | Use |
| --- | --- |
| Start / Backlog | Where tasks rest after creation. Each workflow has one start node. |
| Agent | Runs a Kent agent using the selected subagent role. |
| Script | Executes a local script on the Kent server and parses stdout as workflow completion JSON. |
| Join | Waits for parallel branches and aggregates their parameters. Joins are graph plumbing, not board columns. |
| Terminal | A sink where automation stops, commonly Done. |

Keep node keys stable and machine-friendly, such as `plan`, `implement`, `review`, `needs_changes`, and `done`. Keys are used by agents, prompts, and validation, so prefer lower-case letters, numbers, and underscores over display labels with spaces.

### Transitions

A transition is a choice an agent can make when it completes a node. A transition has a human label, a stable key, and a model-facing description that tells the source agent when to choose it.

In graph terms, one selectable transition is a transition group, and each branch is an edge to a target node. The editor uses transition and branch language so you can focus on the process.

Each transition contains one or more branches:

- A normal transition has one branch to one target node.
- A fan-out transition has multiple branches and starts parallel work.
- A branch into an agent node carries that target agent's prompt, context mode, approval setting, and parameters.

Use transition descriptions for choice criteria. For example, a Review node might offer `done` with "Choose when the implementation is correct and ready to ship" and `needs_changes` with "Choose when implementation changes are required."

### Parallelism, Node Groups, And Joins

Use a node group when one source agent should fan out into parallel branches. Parallel branches are ordinary workflow nodes, not subtasks: one task temporarily has multiple active placements, one per branch, until the branches reach the group's join.

For example, a Main SWE workflow can send implementation output to Code Review and QA at the same time, join both results, then continue to an Approval Gate agent that decides whether to ship or send the task back for changes.

```text
Implement
   |
   +--> Code Review --+
   |                  |
   +--> QA -----------+--> Join -> Approval Gate -> Done
```

To create a new parallel group, right-click the node and select "Group". Drag additional agent nodes into the group to add branches.

Wire the group as one fan-out transition from the upstream source to every grouped branch. Each branch then routes to the group's Join node, and the Join routes to the next node in the workflow. When the editor can infer this topology, it creates or preserves the fan-out and join wiring for you; if it cannot, validation shows the missing wiring before save.

Joins wait for all required branches. Use the Join to aggregate branch parameters, then put synthesis, release-note writing, approval, or final decision-making in a normal agent node after the join. Do not fan out directly from Backlog; insert a split agent after Backlog, then fan out from that agent into the grouped branches.

## 4. Configure Agent Work

### Prompts

Agent prompts live on transitions into agent nodes. The transition prompt is the work order the target agent receives when that branch starts.

Prompts can use task fields:

```md
Implement {{.TaskShortId}}: {{.TaskTitle}}

Task details:
{{.TaskBody}}
```

Prompts can also use parameter values produced by earlier transitions:

```md
Address these review findings:
{{.Params.findings}}
```

To reference a guaranteed earlier transition, qualify the parameter with that transition key:

```md
Use the approved plan:
{{.Params.planning.plan_file_path}}
```

A prompt can reference the previous transition commentary:

```md
Source transition notes:
{{.Params.commentary}}
```

If the previous transition has no commentary, the placeholder renders as an empty string.

A previous-transition parameter is valid only when every path to the prompt passes through that transition. If a value might not exist because of branching, declare a local parameter on the transition that needs it.

![Kent Desktop workflow transition inspector showing a prompt with task and parameter placeholders.](/desktop/desktop-workflow-prompt-editor.webp)

### Script Nodes

Use a Script node when a workflow step should run a deterministic local executable instead of an agent. Script nodes can be used anywhere an agent node can be used in the workflow graph, including the first node after Backlog and branches inside parallel groups.

Set the script path on the script node. Absolute paths are resolved on the Kent server. Relative paths resolve against the task's managed worktree. Workflow graph saves allow empty or invalid paths so you can draft the graph, but execution validation and task start require the selected script to exist, be a file, and be executable.

Kent executes the script directly, without a shell wrapper. Stdin is JSON:

```json
{
  "plan_file": "docs/plan.md",
  "_kent": {
    "run_id": "run_123",
    "placement_id": "placement_123"
  }
}
```

Top-level properties are the incoming workflow parameter values. `_kent` is reserved for Kent runtime identifiers.

Stdout must be the workflow completion JSON. Stderr is diagnostics only. For example:

```json
{
  "transition": "done",
  "commentary": "Generated release notes.",
  "release_notes_path": "docs/release-notes.md"
}
```

If the script exits non-zero, writes invalid completion JSON, omits required parameters, or becomes unavailable, Kent interrupts the run. Resume reruns the script with the same incoming parameter values and the current workflow script path and transition contract.

### Parameters

Parameters are required string outputs from the source agent. They are how one agent hands structured facts to the next branch.

For example, a Review to Needs Changes transition can require:

| Parameter | Description |
| --- | --- |
| `findings` | Concrete required implementation changes, including file paths when useful. |
| `verification` | Checks the reviewer ran and the results. |

Declare parameters on the transition whose source agent can produce them. In fan-out transitions, matching parameter keys must have matching descriptions because they represent one shared output contract.

For each transition, the source agent must provide the declared parameters before it can complete that branch. The target agent receives those values where the transition prompt references them with placeholders such as `{{.Params.findings}}`.

### Context Modes

Context mode controls how the target agent starts its session.
It applies to transitions into agent nodes; transitions into joins or terminal nodes do not start agent sessions.

| Mode | Best for | Trade-offs |
| --- | --- | --- |
| New session | Independent work, QA, code review, security review, release note drafting. | Lowest starting context and cleanest role boundary. The prompt and parameters must contain the context the target needs. |
| Compact and continue session | A large phase handing off to another role or another direction. | Adds a handoff step and starts a new session from a summary. Good when full conversation history is unnecessary but a clean summary matters. |
| Continue session | Tight loops and direct follow-up work by the same role. | Preserves conversation history and prompt-cache continuity. The target keeps the same subagent role, and large histories can keep growing until compaction. |

Continuation modes also have a context source:

- Immediate source uses the session from the node that just completed.
- Selected node uses a previous node that is guaranteed to have run before this transition.
- Previous run of this target is for loops where the workflow returns to a node and should continue that node's prior session.
- Previous run of this target, or new session is for re-review loops where the first pass starts fresh and later passes continue the target's prior session.

Use `new_session` or `compact_and_continue_session` when changing subagent roles. Use `continue_session` when preserving the exact working context matters more than changing roles.

### Human Approval

A transition can require approval. When the source agent chooses that transition, the task waits before target branches start. Use approvals for plan acceptance, destructive operations, release steps, or any point where you want to inspect the agent's proposed direction.

For fan-out transitions, approval gates the whole selected transition before any branch starts.

If a task waits for approval, manually moving the task rejects the pending transition and routes the task to your selected node.

### Completion Modes

Completion mode controls how an agent node reports that it has finished and which transition it selected.
Only agent nodes have completion modes; Start, Join, and Terminal nodes do not execute agent loops.

| Mode | Use | Cache and cost notes |
| --- | --- | --- |
| Inherit global default | Use the workflow completion mode from [configuration](../config/#workflow). | Same behavior as the resolved configured mode. |
| Auto | Best default for most nodes. Kent picks the effective mode from the workflow shape, provider support, and shell availability. | Usually gives the safest cache/cost trade-off automatically. |
| Structured output | Provider-native structured output. Use it when the provider supports strict structured responses and the node is not part of a `continue_session` chain. | Lowest-friction on capable providers, but fails run start when unsupported and fully invalidates cache on continued sessions. |
| Tool call | Dedicated completion tool. Use it for providers without structured-output support. | Reliable tool-driven completion, but fully invalidates cache on continued sessions. |
| Shell command | Completion through the agent's shell environment. Prefer this for `continue_session` chains. | Requires the shell tool for the target role and gives the agent shell access, but avoids completion-contract cache invalidation. |
| Unstructured output | Best-effort raw JSON final answer. Use only when you need `continue_session` and cannot use shell command. | Most fragile mode. It avoids dynamic completion metadata, but depends on the model following exact final-answer instructions. |

`Auto` chooses unstructured output if the runtime has no shell available; otherwise it chooses shell command when the workflow contains a `continue_session` transition, structured output on capable providers, and tool call as the remaining fallback.

### Cache And Cost Behavior

Workflow design affects prompt-cache continuity and token spend:

- `continue_session` gives the strongest cache continuity because it keeps the same session, role, tools, conversation history, and provider cache.
- `new_session` starts clean and does not invalidate another session's cache. It gives the target agent the most free context, but the prompt and parameters must carry enough information because the agent may spend tokens re-orienting in the workspace.
- `compact_and_continue_session` asks the previous agent for a handoff, then starts a new session from that summary. It frees context, but adds handoff cost and leaves the previous session cache behind.
- Changing prompts, parameters, tools, subagent roles, completion modes, or provider settings affects future runs and may change cache behavior.

## 5. Validate, Link, And Save

The editor shows draft validation and execution validation. Draft validation catches graph-shape problems such as duplicate keys, invalid prompt placeholders, bad parameter contracts, and incomplete node groups. Execution validation catches automation blockers such as missing prompts, missing roles, invalid start shape, unreachable nodes, and non-terminal nodes that cannot reach a terminal node.

A workflow can remain linked to a project while execution validation fails. Backlog tasks and comments remain available, but starting automation requires a valid executable workflow.

Graph edits are blocked when active tasks would be affected. Active tasks include tasks in executable nodes with running or runnable work, waiting for approval, waiting on questions, or otherwise not safely parked in Backlog or a terminal node. Interrupted runs do not block workflow edits until they are resumed. Destructive saves that affect only Backlog or terminal task references require confirmation.

## 6. Manage Tasks

Create tasks from a project board. Each task belongs to one project and one linked workflow. The project supplies workspaces and execution environment; the workflow supplies the board shape and automation path.

Each task has a title, body, source workspace, and optional source URL. New tasks start in Backlog and follow the project's default workflow unless you choose another linked workflow.

Choose the source workspace before starting automation. Agents run in the environment where the Kent server runs, so that environment must have the repository, toolchains, credentials, and local files the workflow needs.

### Keep Tasks Shippable

Starting a task creates a managed git worktree for that task and schedules the first agent node. Kent reuses the same task worktree across downstream agent nodes, so implementation, review fixes, QA, and shipping steps happen against one checkout.

Because each started task has its own worktree, structure tasks as independently shippable branches. Do not split one feature into separate tasks that must share unmerged code in one checkout. If the work cannot ship independently, keep it as one task; if it can be sliced, make each slice feature-gated or isolated enough to merge separately before the next dependent task starts.

![Kent Desktop task board and task detail view showing task actions, comments, and a pending question.](/desktop/desktop-workflow-tasks.webp)

## 7. Manage And Ship

Design workflows around the shipping decisions you actually make. A typical implementation workflow has a planning node, an implementation node, a review node, a `needs_changes` loop back to implementation, and a terminal `done` node. A release workflow might add QA, docs, changelog, approval, and publish branches.

Keep prompts small but complete. Use parameters for facts the next agent must know, approvals for human gates, and context modes for memory boundaries. Prefer clean role boundaries for independent review and QA; prefer continuation for tight fix loops where the agent should retain exact local context.

When a task reaches a terminal node, inspect the task worktree, verify the expected checks, and ship from the branch or worktree according to your project's normal release process. The workflow keeps the task history, run history, transition outputs, approvals, and comments attached to the task for audit and follow-up.
