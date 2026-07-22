# Terminology

Use these terms consistently in specs, code names, CLI/API contracts, and implementation work.

## Workflow

### Task

A durable user-facing unit of work. A task owns its current workflow Nodes, task metadata, unresolved workflow interactions, comments, and Session associations. A Task may have several current Nodes while parallel work is active. Completed Node execution and workflow movement are not retained as separate history.

### Task Short ID

A human-facing, project-scoped task identifier formed from the project key plus a project-local sequence, e.g. `KNT-123`. Assigned at task creation and immutable thereafter.

### Project Key

A short, human-facing, uppercase project prefix applied to new task short IDs. Unique within a persistence root.

### Workflow Version

A monotonic workflow definition counter incremented by persisted definition edits. Metadata-only changes and graph changes each increment it once, combined metadata+graph saves increment it once, and no-op saves do not increment it. It provides current-definition and pending-approval stale-warning data without immutable graph versioning.

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

### Label

A Project-owned user-defined organizational tag with an immutable UUID v4 identity and a mutable display name. A label is reusable across every workflow board linked to its Project, and tasks may use any subset of labels owned by their own Project. Labels do not affect workflow state, scheduling, prompts, or execution.

### Assignee

The subagent role associated with an executable node. UI surfaces may present the role as the node's assignee.

### Node

A workflow graph state. Agent, start, and terminal nodes can map to user-visible workflow states or Kanban columns/statuses. Join nodes are internal merge plumbing omitted from board columns and shown in workflow editor visuals as inspectable merge nodes.

### Current Nodes

The Node or Nodes a Task is in at this moment. A Task usually has one current Node and may have several only while parallel branches are active. Current Nodes are task-owned state with no independent entity IDs. An executable current Node stores only the state needed to execute or resume that work, including its optional Session binding; leaving the Node removes that current state.

### Node Group

A workflow editor grouping around related graph nodes. GUI-authored node groups are execution-shaped parallel groups: they contain branch nodes and one join, and the fan-out is represented by one fan-out transition. A one-node group may exist only as an unsaved draft editing state.

### Start Node

The node where new tasks enter a workflow. A start node is non-executable and has no authored parameters.

### Task Start

An explicit operation that moves a newly created Task from start/backlog into its first executable current Node by applying the Start Node's outgoing Transition.

### Terminal Node

A sink node where workflow automation stops.

### Edge

A directed graph primitive from a source node to a target node. Kent UI surfaces call user-facing edges transition branches; graph libraries and persistence may use edge terminology at adapter and storage boundaries.

### Transition

A source-node decision that moves a task toward one or more target nodes. A normal transition has one transition branch. A fan-out transition has multiple branches selected together.

### Transition Branch

One target branch of a transition. The branch owns target-specific invocation behavior such as target node, prompt applicability, parameters, context preservation, context source, routing, and join behavior.

### Fan-Out Transition

A Transition with multiple branches that adds several parallel target Nodes to a Task's Current Nodes from one source-node decision.

### Parameter

A stable-key string fact produced by an agent source when it applies a Transition. Parameters are declared on Transition Branches, are required when declared, and can be materialized into target prompts, later-node prompt references, joins, and validation.

### Transition Prompt

A prompt template owned by a transition branch into an agent node. Transitions into non-agent nodes do not have prompts.

### Transition Result

Structured data produced when an executable current Node completes and selects a Transition. It includes the selected Transition Key when the source Node has multiple outgoing Transitions, optional `commentary`, and top-level Parameter values required by the selected Transition.

### Parameter Requirements

Requirements for Transition Parameters that must be present before a current Node can complete. They are derived from the selected Transition and its fan-out branches.

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

Per-Transition-Branch policy deciding which retained Session supplies context for continuation modes. `immediate_source` uses the Session bound to the source current Node. `node:<node_key>` selects the latest retained Session associated with a guaranteed-prior agent Node. `previous_target` selects the latest retained Session associated with the Transition Branch target and requires that one exists. `previous_target_or_new` selects that Session when one exists and otherwise starts a new Session. While parallel work is active, every selection is scoped to the same Transition Branch Key as the source current Node.

### Exact Execution Scope

The opaque immutable identity of one live agent or script execution for a Task's current Node and parallel branch when applicable. Resume creates a new scope after the previous scope has fully stopped. An Exact Execution Scope is the only execution-liveness authority; durable state, transcript events, timestamps, goals, and client state cannot prove that execution is live.

### Resource Generation

The immutable generation of the live runtime resources used by an exact execution scope. Replacing or recreating those resources advances the resource generation so stale handles cannot operate on the replacement.

### Script Node

A workflow executable node that runs a local executable on the Kent server instead of starting an agent session. It reads incoming workflow parameter values from JSON stdin, writes workflow completion JSON to stdout, and uses stderr only for diagnostics.

### Session Contract

The execution setup captured by a Kent session for one contract generation. Model/provider setup, generation parameters, active enabled tool IDs, and native web-search mode stay locked within that generation. `compact_and_continue_session` starts a fresh target-node contract generation; ordinary compaction can lazily refresh system and reviewer prompt snapshots from current config/source truth within its generation. Developer meta context messages are transcript entries, not lazy-refreshed session-contract snapshots. Tool declarations for locked tool IDs are runtime-defined and are not persisted as session snapshots.

### Runtime Parameter Contract

The current possible and required Transition Parameters for an executable current Node. It is derived from the latest Workflow definition when execution starts or resumes rather than retained as historical execution state.

### Session

Kent transcript/runtime artifact associated with an agent Node on a Task and, while parallel work is active, its Transition Branch Key. A Task may retain many Sessions because of loops, parallel branches, retries, or context-preservation choices. Script Nodes have no Session.

### Node Transition

An atomic Task state change through a Transition Branch. It removes completed source Nodes from Current Nodes, applies Parameter bindings and context preservation, and adds or blocks the target Nodes. Applied transitions are not retained as workflow movement history.

### Join

A non-agent fan-in Node that waits for the required current parallel branches before continuing. It exposes a read-only aggregate of incoming branch Parameters. Same-key incoming Parameter collisions are invalid.

### Task Interrupt

A resumable stop operation for one Session or every Exact Execution Scope on a Task. It joins scope finalization and records interruption state on the affected current Nodes without deleting the Task, Session, or worktree.

### Task Delete

A server-authoritative operation exposed through a `can_delete` read-model fact. It revalidates task quiescence, removes reconstructible managed artifacts idempotently, preserves session artifacts, and deletes the durable task row last. Task Delete has no journal and does not substitute for whole-workflow deletion.

### Quiescence

The task lifecycle condition in which no exact live execution, automatic intent, or runtime gate exists. Task Delete and whole-workflow deletion revalidate quiescence before mutation.

### Question

A user-blocking ask emitted by a Session through `ask_question`. Questions carry prompt text, optional suggestions/options, optional recommended option index, and schema-backed answer expectations.

### Orchestrator

An agent Node whose Transition Prompts ask it to coordinate work. Orchestration may happen inside one Session or through Workflow graph branches.

### Operational Stop State

A workflow/task state where auto-execution stops because the task is done, interrupted, blocked, or awaiting manual/user action.

### Workflow Execution

The server control plane that owns workflow lifecycle mutations, the context-aware global mutation permit, volatile automatic intent, admission, configured automatic capacity, task affinity, and immutable live snapshots. It is the only workflow lifecycle orchestration authority.

### Automatic Intent

A typed in-memory request for Workflow Execution to start eligible workflow work automatically. Its membership and ordering are volatile and intentionally lost on restart; it is never reconstructed from durable state.

### Immutable Live Snapshot

One read-only, point-in-time projection of Exact Execution Scopes, Automatic Intents, and runtime gates. Workflow reads may combine it with durable facts and may become stale; mutations revalidate authoritative state before commit.

### Scheduler

An informal label for the automatic-admission responsibility inside Workflow Execution. Scheduler is not a separate lifecycle authority, durable queue, reconstruction worker, or owner of live execution.

### Task Comment

A durable task-local note. Task comments are hard-deleted notes, not source-run artifacts, tombstones, or opaque metadata containers.

## GUI

### Toast

A transient or persistent global notification surfaced by the desktop app. Toast and snackbar are equivalent terms in Kent GUI docs and code.

## TUI And Transcript

### Streaming Message

The in-progress assistant turn while the model is generating, modeled server-side as a single provisional message (`chatStore.streaming`) that grows as deltas arrive. It is held outside provider history and is never persisted; it is exposed to clients as a sibling of the committed transcript (the trailing message), not as a committed entry. Each completed model response resolves its active streaming message exactly once. An authoritative committed assistant-text row finalizes the streaming message and receives its stream UUID. A completed response without such a row discards an active streaming message with the typed `superseded` reason before the next model generation or before the response exits; this includes accepted tool-call-only ask, local-tool, and hosted-tool continuations, plus responses omitted from persistence by workflow preflight, external durable workflow completion, reasoning or no-op retry disposition, or final-answer-with-tools terminalization before final-text persistence. Established response side effects may precede supersession, but a subsequent assistant delta may not. A completed response with no active streaming message emits no stream terminal. Interrupts and abnormal execution termination also discard an active streaming message. "Ongoing"/"detail" are TUI render postures only and must never appear in server/shared domain naming; the server has no knowledge of how clients render the streaming message.

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

The single shared runtime resource a Session registers while available. Every interactive client and any headless or workflow-controlled execution resolves the same resource as an equal control surface. Live execution exists only while the exact execution authority exposes a matching scope; a registered idle runtime is not a live execution.

### RuntimeActivity

The server-owned live read model derived from the exact execution authority for whether a Session runtime is unavailable, establishing, registered idle, running, awaiting a live prompt/approval, draining, or closing, including the active kind for exclusive work such as a user turn, goal loop, workflow-controlled execution, compaction, shell, or background step. Running and waiting require matching Exact Execution Scope evidence. Clients project `RuntimeActivity`; durable state and client-local fallback booleans are not liveness sources.

### Runtime Command

The sole typed ordering authority for model-visible human input, agent-originated workflow completion intent, goals, and technical input within one exact agent execution scope. It owns ordering, acceptance, supersession, persistence effects, and retryable restoration. Prompt answers resolve their exact live waiter directly.

### Completion Fence

The Workflow Execution boundary after which an actor-neutral completion intent may commit for an Exact Execution Scope or one unambiguous current idle executable Node on a Task. For an agent scope, accepted human steering before the fence supersedes completion and continues the same live execution; input arriving after the fence is rejected with a typed retryable result so the client restores its draft.

### Runtime Gate

A volatile exact-scope guard held while Workflow Execution performs an execution-affecting lifecycle transition. It blocks conflicting mutation, is included in immutable live snapshots, and is never persisted or reconstructed after restart.

### Append Certainty

The Session Store result `{committed, error}` for a mutation. `committed=false` leaves retry ownership with the caller and applies no projection; `committed=true` consumes retry ownership and applies state exactly once even when a post-commit observer error is returned.

### ReadModelVersion

The single per-session epoch/generation/sequence for server-produced runtime UI facts. Runtime activity, main-view snapshots, interrupt responses, versioned runtime activity events carried on `SessionActivity`, and migrated prompt read-model stream events all use this version so clients can ignore stale payloads with one ordering rule. It is not the raw `SessionActivity` replay cursor, and response-only versions are ordering points rather than replayable session-stream positions.

### PendingModelRecovery

A non-liveness session recovery marker used to repair model context after an interrupted or crashed provider-visible step. It may describe the step and outstanding tool-call IDs needed for reopen recovery, but it never marks the runtime active, blocks release, or drives UI busy state.

### DraftRecoveryBuffer

An ordered persisted collection of inert local input entries recoverable after an early TUI exit. Each entry carries only its recovery category and original text; the collection carries no delivery state. Opening a session restores eligible entry text to the editable composer and never resumes or automatically replays prior work.

### DraftInputBuffer

A typed inert category-and-text entry inside `DraftRecoveryBuffer`, such as active submitted text, queued message, pending injected input, locked injected input, or reviewer buffer. Its category explains why the text was retained but carries no delivery state. The visible prompt text remains separate from these entries.

### Forced Local Detach

The second-Ctrl+C exit path for a TUI client while interrupt is pending. The client exits locally and detaches its client attachment without releasing or force-closing a shared daemon runtime; embedded process exit still cleans up its attachment before shutdown.

### Agent Step

One provider request/response iteration in the runtime loop, including returned tool calls and their committed results. A user steer ends the current Agent Step and starts a new Agent Step within the same Agent Turn.

### Agent Turn

A complete agent run from a user submission until the runtime returns to idle. An Agent Turn is composed of one or more Agent Steps.

### Step Boundary

The interval after the current Agent Step commits tool-result handling and before the next provider request or steered Agent Step begins. Transitions that affect the next Agent Step become authoritative at this boundary.

### Queue

The user-facing TUI action that holds user messages until the current turn ends. Queued messages wait for the runtime to go idle, then drain into the next turn.

### Steer

The user-facing TUI action that injects a message to take effect after the current step ends, mid-turn between steps, rather than waiting for the turn to finish.

### Steer Queue

The internal queue that holds step-end-drained submissions until the current step completes. It is the single submission path for (almost) every message that lands in the transcript — user steering, queued-message flushes, worktree reminders, workflow-step output, mode-change notices, and error messages — built from typed steering intents rather than ad-hoc appenders or direct transcript writes.

### Equal Full-Control Attach

Every client attached to a session is an equal, full-control surface over the shared runtime: there is no ownership, no leases, no controller/limited-control distinction, no read-only attach, and no per-operation gating. The server owns runtime orchestration only (the single shared engine, safe-point application, and persistence), not client authorization.

### Goal

A persistent self/user-declared objective with a continuation loop (nudges, suspend/resume, premature-stop reminders) that drives turns until the goal is completed, paused, or cleared. A goal may be set by the user or by the model itself, including inside a workflow-controlled Session.

### Goal Continuation Loop

The driver that repeats the step loop to keep working a goal across Agent Turns, injecting goal reminders. It does not operate while an Exact Execution Scope is driving the Session for Workflow Execution—the workflow turn loop is the single continuation driver there, and the Goal stays a passive objective folded into the workflow's continuation nudge.
