# Terminology

Use these terms consistently in specs, code names, CLI/API contracts, and implementation work.

## Workflow

### Task

A durable user-facing unit of work. A task owns workflow state, task metadata, node history, transition history, question associations, comments, and execution artifacts. Kent sessions are artifacts under a task.

### Task Short ID

A human-facing, project-scoped task identifier formed from the project key plus a project-local sequence, e.g. `KNT-123`. Assigned at task creation and immutable thereafter.

### Project Key

A short, human-facing, uppercase project prefix applied to new task short IDs. Unique within a persistence root.

### Workflow Version

A monotonic workflow definition counter incremented by persisted definition edits. Metadata-only changes and graph changes each increment it once, combined metadata+graph saves increment it once, and no-op saves do not increment it. It provides traceability for tasks, runs, transitions, approvals, and stale-warning UX without immutable graph versioning.

### Workflow

A durable directed graph that describes how tasks move through work. Workflows are top-level reusable definitions linked into projects.

### Execution Target Policy

A workflow-level rule for choosing where a task's executable nodes run. The five policies are no managed worktree, source `HEAD`, repository default branch, custom Git ref, and `ask_on_first_execution`, which asks the operator when an unlocked task first reaches executable work.

### Execution Target

The target-selection provenance locked to a task when its first executable action succeeds. For a managed target it records the requested Git revision and resolved commit while the current managed-worktree relation, root, and named branch may be conservatively restored or changed by the operator. A no-managed-worktree target follows the task's current source workspace. Later nodes and retries reuse the locked mode and managed selection provenance despite workflow edits or Git ref movement.

### Execution Root

The currently derived directory used as the working directory and relative-path base for a task's executable nodes. It is the task's current source workspace when the task uses no managed worktree and the current managed worktree root otherwise.

### Workflow Draft

A workflow definition that can be saved while semantic validation reports graph or project-context errors. Drafts still satisfy hard storage invariants such as valid identifiers, references, enums, unique keys, and exactly one start node.

### Validation Context

The purpose for validating a workflow graph, such as draft editing, task creation, or execution scheduling. Contexts can report the same errors while choosing different blocking behavior.

### Project Workflow Link

An active project association with a reusable workflow definition. The link lets a project use a workflow without copying the workflow graph and is the task's project/workflow pairing source of truth.

### Assignee

The subagent role associated with an executable node. UI surfaces may present the role as the node's assignee.

### Node

A workflow graph state. Agent, start, and terminal nodes can map to user-visible workflow states or Kanban columns/statuses. Join nodes are internal merge plumbing omitted from board columns and shown in workflow editor visuals as inspectable merge nodes. Node identity is execution identity.

### Node Group

A workflow editor grouping around related graph nodes. GUI-authored node groups are execution-shaped parallel groups: they contain branch nodes and one join, and the fan-out is represented by one fan-out transition. A one-node group may exist only as an unsaved draft editing state.

### Start Node

The node where new tasks enter a workflow. A start node is non-executable and has no authored parameters.

### Task Start

An explicit operation that moves a newly created task from start/backlog into the first executable placement by applying the start node's outgoing transition.

### Terminal Node

A sink node where workflow automation stops.

### Edge

A directed graph primitive from a source node to a target node. Kent UI surfaces call user-facing edges transition branches; graph libraries and persistence may use edge terminology at adapter and storage boundaries.

### Transition

A source-node decision that moves a task toward one or more target nodes. A normal transition has one transition branch. A fan-out transition has multiple branches selected together.

### Transition Branch

One target branch of a transition. The branch owns target-specific invocation behavior such as target node, prompt applicability, parameters, context preservation, context source, routing, and join behavior.

### Fan-Out Transition

A transition with multiple branches that starts parallel target placements from one source-node decision.

### Parameter

A stable-key string fact produced by an agent source when it applies a transition. Parameters are declared on transition branches, are required when declared, and can be used by target prompts, previous-transition prompt references, joins, terminal transition history, and validation.

### Transition Prompt

A prompt template owned by a transition branch into an agent node. Transitions into non-agent nodes do not have prompts.

### Transition Result

Structured data produced by a node run for a selected transition. It includes the selected transition key when the source node has multiple outgoing transitions, optional `commentary`, and top-level parameter values required by the selected transition.

### Parameter Requirements

Runtime requirements for transition parameters that must be present before the run can continue. Parameter requirements are derived from the selected transition and its fan-out branches.

### Parameter Binding

A runtime mapping from a transition parameter key to the value made available to a target prompt or join aggregate.

### Transition Key

A stable workflow-wide key for a transition. Agent nodes emit transition keys when more than one outgoing transition is available. Prompt templates use transition keys to reference previous-transition parameters.

### Transition Label

The human-facing label for a transition. Labels are display text; transition keys are stable contract identifiers.

### Transition Branch Key

A stable key for one branch inside a fan-out transition. Branch keys distinguish target branches in editor visuals, routing, and join aggregation.

### Context-Preservation Mode

Per-transition-branch policy for the next node's execution context:

- `new_session`: start a blank Kent session and inject the previous transition result plus task metadata.
- `continue_session`: continue a selected previous Kent session with a new prompt/goal and bound metadata.
- `compact_and_continue_session`: compact the selected previous session first, then continue with a new prompt/goal and bound metadata.

### Context Source

Per-transition-branch policy deciding which earlier run supplies the source session for continuation modes. `immediate_source` uses the run that produced the selected transition. `node:<node_key>` selects the latest completed run for a guaranteed-prior agent node. `previous_target` selects the latest completed run of the transition branch target and requires that a matching run exists. `previous_target_or_new` selects the latest completed target run when one exists and otherwise starts the target with an effective new session.

### Run

One durable execution attempt for an executable node on a task. Agent runs create or continue a Kent session. Script runs execute a local server-side executable. A run may call tools, ask questions, produce a transition result, and terminate with a structured outcome.

### Script Node

A workflow executable node that runs a local executable on the Kent server instead of starting an agent session. It reads incoming workflow parameter values from JSON stdin, writes workflow completion JSON to stdout, and uses stderr only for diagnostics.

### Interrupted Run

A run stopped before producing a valid transition result. Its session and worktree state remain available so execution can continue from the interruption point.

### Session Contract

The execution setup captured by a Kent session for one contract generation. Model/provider setup, generation parameters, active enabled tool IDs, and native web-search mode stay locked within that generation. `compact_and_continue_session` starts a fresh target-node contract generation; ordinary compaction can lazily refresh system and reviewer prompt snapshots from current config/source truth within its generation. Developer meta context messages are transcript entries, not lazy-refreshed session-contract snapshots. Tool declarations for locked tool IDs are runtime-defined and are not persisted as session snapshots.

### Runtime Parameter Contract

The run-start snapshot of possible and required transition parameters for a node run. Runtime parameter contracts are derived from outgoing transitions, fan-out branch unions, previous-transition references, and join aggregates, then frozen for in-flight work.

### Run Start Context

The typed aggregate materialized for the runner before a workflow run starts. It combines task, run, node, workspace/worktree, run-start snapshot, accepted transition branch invocation facts, parameter values, context-preservation mode, and context source provenance. It is a store materialization interface, not an opaque persisted JSON envelope.

### Session

Kent transcript/runtime artifact used by a run. A task may have many sessions because of loops, branches, retries, or context-preservation choices.

### Session Run

One live execution of a session through the runtime loop. A session can have multiple runtime activations over time through resume, queued user submissions, goal turns, compaction, or background continuation.

### Node Transition

A task movement from one node to another through a transition branch. It evaluates transition conditions, applies parameter bindings, applies context preservation, and schedules or blocks the next run.

### Node Placement

An occurrence of a task in a node. A task can have multiple active placements only when a workflow explicitly runs parallel branches.

### Parallel Batch

The branch placements created by one fan-out transition for one task. The batch gives joins a correlation identity.

### Join

A non-agent fan-in node that waits for required inbound branches before continuing. The join exposes a read-only aggregate of incoming branch parameters. Same-key incoming parameter collisions are invalid.

### Task Cancellation

A task-level stop operation that prevents further automation, interrupts active runs with cancellation metadata, and archives the task to terminal/Done for board visibility.

### Question

A user-blocking ask emitted by a run through `ask_question`. Questions carry prompt text, optional suggestions/options, optional recommended option index, and schema-backed answer expectations.

### Orchestrator

An agent node whose transition prompts ask it to coordinate work. Orchestration may happen inside one agent run or through workflow graph branches.

### Operational Stop State

A workflow/task state where auto-execution stops because the task is done, interrupted, blocked, or awaiting manual/user action.

### Scheduler

Server-owned automation scheduler. Runnable work is derived from durable task/run state; pending-work ordering and active runtime ownership are live scheduler/runtime state.

### Task Comment

A durable task-local note. Task comments are hard-deleted notes, not source-run artifacts, tombstones, or opaque metadata containers.

## GUI

### Toast

A transient or persistent global notification surfaced by the desktop app. Toast and snackbar are equivalent terms in Kent GUI docs and code.

## TUI And Transcript

### Streaming Message

The in-progress assistant turn while the model is generating, modeled server-side as a single provisional message (`chatStore.streaming`) that grows as deltas arrive. It is held outside provider history and is never persisted; it is exposed to clients as a sibling of the committed transcript (the trailing message), not as a committed entry. Each completed model response resolves its active streaming message exactly once. An authoritative committed assistant-text row finalizes the streaming message and receives its stream UUID. A completed response without such a row discards an active streaming message with the typed `superseded` reason before the next model generation or before the response exits; this includes accepted tool-call-only ask, local-tool, and hosted-tool continuations, plus responses omitted from persistence by workflow preflight, external durable workflow completion, reasoning or no-op retry disposition, or final-answer-with-tools terminalization before final-text persistence. Established response side effects may precede supersession, but a subsequent assistant delta may not. A completed response with no active streaming message emits no stream terminal. Interrupts and abnormal run termination also discard an active streaming message. "Ongoing"/"detail" are TUI render postures only and must never appear in server/shared domain naming; the server has no knowledge of how clients render the streaming message.

### Ongoing Mode

Primary long-running TUI mode backed by normal-buffer terminal scrollback. Ongoing mode appends committed transcript history and live overlays without owning a scrollable viewport or rewriting emitted lines. This is a client-only render posture; the server is unaware of it.

### Detail Mode

Transcript inspection mode with UI-local selection, expansion, and line-oriented viewport scrolling over stale bounded cursor pages. Server-backed page membership changes through initial mode-entry hydration, session-target replacement, and user-triggered adjacent-page loads. Runtime events never append to or reconcile the current detail page membership while it is open.

### Transcript Mode

The rendering posture for transcript entries. Current modes are ongoing and detail.

### Alternate Screen

Terminal buffer separate from normal scrollback. Kent avoids alternate screen for ongoing mode so persistent history remains in native terminal scrollback.

### Alternate Scroll

Terminal mode `?1007`, which converts wheel input into cursor-key style events in alternate-screen contexts. Every alternate-screen surface enables alternate scroll while active and disables it on exit, so wheel input scrolls the surface through its cursor-key handlers. The only exceptions are ongoing mode, which never enables it, and the rollback/edit picker, which renders inside alt-screen but ignores mouse and keeps alternate scroll off.

### Mouse Capture

Terminal mode where the app receives mouse events instead of leaving them to native terminal selection. Kent keeps mouse capture disabled in ongoing and detail modes.

### Normal Buffer

Terminal buffer with native scrollback. Ongoing mode renders committed history here and treats emitted lines as immutable.

### Scrollback

Terminal-owned history of normal-buffer output. Kent does not replay, clear, or restyle committed ongoing scrollback after startup.

## Runtime Steering And Goals

### Active Session Runtime

The single shared live runtime (engine) a session registers while active. There is exactly one engine per session; every interactive client and any headless or workflow run resolves and drives that same shared engine through its queue/steer/exclusive-step boundary. It exists independently of any particular client and may be registered but idle between activations.

### RuntimeActivity

The server-owned live read model for whether a session runtime is unavailable, starting/reserved, registered idle, running, awaiting prompt/approval, draining, or closing, including the active kind for exclusive work such as a user turn, goal loop, compaction, shell, or background step. Clients use `RuntimeActivity` as the active/idle authority; session database run rows, transcript rows, goal status, and client-local booleans are not liveness sources.

### ReadModelVersion

The single per-session epoch/generation/sequence for server-produced runtime UI facts. Runtime activity, input reconciliation, main-view snapshots, interrupt responses, versioned runtime activity/reconciliation events carried on `SessionActivity`, and migrated prompt read-model stream events all use this version so clients can ignore stale payloads with one ordering rule. It is not the raw `SessionActivity` replay cursor, and response-only versions are ordering points rather than replayable session-stream positions.

### RuntimeOperationRef

A client-created typed identity for an input-bearing runtime operation before dispatch. It names the operation kind and the matching request, queue item, shell, or compact identifier so runtime-control, interrupt, and reconciliation paths never infer input ownership from transcript text.

### RuntimeInputReconciliation

The server-owned read model that tells a client whether a `RuntimeOperationRef` was accepted, committed/submitted, canceled/not committed, failed with restore, or is unknown/evicted. It is delivered under `ReadModelVersion`; input recovery can change while runtime activity does not, but both facts share one runtime UI ordering clock.

### PendingModelRecovery

A non-liveness session recovery marker used to repair model context after an interrupted or crashed provider-visible step. It may describe the step and outstanding tool-call IDs needed for reopen recovery, but it never marks the runtime active, blocks release, or drives UI busy state.

### DraftRecoveryBuffer

Structured persisted local input that should be recoverable after an early TUI exit, including active submitted text, queued messages, pending injected input, reviewer buffers, and related operation refs. It is a retry/recovery payload, not an instruction to auto-submit.

### DraftInputBuffer

A typed hidden/recoverable buffer entry inside `DraftRecoveryBuffer`, such as active submitted text, queued message, pending injected input, locked injected input, or reviewer buffer. The visible prompt text is the `VisibleInput` field on `DraftRecoveryBuffer`, not a `DraftInputBuffer`.

### Forced Local Detach

The second-Ctrl+C exit path for a TUI client while interrupt is pending. The client exits locally and detaches its runtime owner reference without releasing or force-closing a shared daemon runtime; embedded process exit still cleans up local owner state before shutdown.

### Step

One model request/response iteration in the runtime loop, including any tool calls it triggers. Steps run back-to-back to form a turn.

### Turn

A full agent run from a user submission until the runtime returns to idle: the agent produces its final message and no further step is scheduled. A turn is composed of one or more steps.

### Queue

The user-facing TUI action that holds user messages until the current turn ends. Queued messages wait for the runtime to go idle, then drain into the next turn.

### Steer

The user-facing TUI action that injects a message to take effect after the current step ends, mid-turn between steps, rather than waiting for the turn to finish.

### Steer Queue

The internal queue that holds step-end-drained submissions until the current step completes. It is the single submission path for (almost) every message that lands in the transcript — user steering, queued-message flushes, worktree reminders, workflow-step output, mode-change notices, and error messages — built from typed steering intents rather than ad-hoc appenders or direct transcript writes.

### Equal Full-Control Attach

Every client attached to a session is an equal, full-control surface over the shared runtime: there is no ownership, no leases, no controller/limited-control distinction, no read-only attach, and no per-operation gating. The server owns runtime orchestration only (the single shared engine, safe-point application, and persistence), not client authorization.

### Goal

A persistent self/user-declared objective with a continuation loop (nudges, suspend/resume, premature-stop reminders) that drives turns until the goal is completed, paused, or cleared. A goal may be set by the user or by the model itself, including inside a workflow run.

### Goal Continuation Loop

The driver that re-runs the step loop to keep working a goal across runs, injecting goal reminders. It does not run while a workflow run owns the session — the workflow turn loop is the single continuation driver there, and the goal stays a passive objective folded into the workflow's continuation nudge.
