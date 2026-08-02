# Terminology

Use these terms consistently in specs and product surfaces. These terms extend common English for Kent's domain.

## Workflow

### Task

A durable user-facing unit of work. A task owns its current workflow Nodes, task metadata, unresolved workflow interactions, comments, and Session associations. A Task may have several current Nodes while parallel work is active. Completed Node execution and workflow movement are not retained as separate history.

### Task Dependency

A Project-scoped directed planning relationship from one Blocker Task to one
Blocked Task. The ordered Blocker Task and Blocked Task identities identify the
relationship. A Task Dependency has no separate product identity. It is
satisfied when its Blocker Task is done and is unsatisfied otherwise.

### Blocker Task

The source Task in a Task Dependency. A Blocker Task must be done for that
dependency to be satisfied.

### Blocked Task

The target Task in a Task Dependency. Unsatisfied Task Dependencies warn before
Kent starts or manually moves the Blocked Task into executable work, but they do
not prohibit that work.

### Task Short ID

A human-facing, project-scoped task identifier formed from the project key plus a project-local sequence, e.g. `KNT-123`. Assigned at task creation and immutable thereafter.

### Project Key

A short, human-facing, uppercase project prefix applied to new task short IDs. Unique within a persistence root.

### Workflow Version

A monotonic counter for changes to a Workflow definition. A save that changes metadata, the graph, or both increments the counter once. A save that changes nothing does not increment it. Kent uses the value to identify the current definition and warn when a pending Approval refers to an older definition.

### Workflow

A durable directed graph that describes how tasks move through work. Workflows are top-level reusable definitions linked into projects.

### Execution Target Policy

A workflow-level rule for choosing where a task's executable nodes run. The five policies are no managed worktree, source `HEAD`, repository default branch, custom Git ref, and `ask_on_first_execution`, which asks the operator when an unlocked task first reaches executable work.

### Execution Target

The target-selection provenance locked to a task when Kent establishes its first Execution Root. Durable movement to executable work alone does not lock it. A managed target remains unlocked if preparation fails without changing the filesystem; after preparation retains managed-worktree filesystem state, Kent locks the requested Git revision and resolved commit when persistence succeeds. A no-managed-worktree target follows the task's current source workspace. Later nodes and retries reuse the locked mode and managed selection provenance despite workflow edits or Git ref movement.

### Execution Root

The directory Kent uses as the working directory and relative-path base for a Task's executable Nodes. It is the Task's source workspace when the Task uses no managed worktree and the managed worktree root otherwise.

### Workflow Draft

A Workflow definition that users can save while validation reports graph or Project errors. A Workflow Draft must have valid identifiers, valid references, unique keys, supported values, and exactly one Start Node.

### Validation Context

The operation for which Kent validates a Workflow, such as editing a draft, creating a Task, or starting work. The same validation issue can be informative for one operation and blocking for another.

### Project Workflow Link

An active association that lets a Project use a reusable Workflow without copying it. Each Task belongs to one Project Workflow Link.

### Label

A Project-owned user-defined organizational tag with an immutable UUID v4 identity and a mutable display name. A label is reusable across every workflow board linked to its Project, and tasks may use any subset of labels owned by their own Project. Labels do not affect workflow state, scheduling, prompts, or execution.

### Assignee

The subagent role associated with an executable node. UI surfaces may present the role as the node's assignee.

### Node

A state in a Workflow. Start, Agent, Script, and Terminal Nodes can appear as workflow states or board columns. A Join combines parallel branches and does not appear as a board column.

### Current Nodes

The Node or Nodes that contain a Task at this moment. A Task usually has one Current Node. It can have several Current Nodes only while parallel branches are active. Current Nodes belong to the Task and have no independent identity. A Current Node entered through a Transition retains that Transition Branch so Kent can resolve its live prompt and context policy from the latest Workflow definition. Leaving a Node removes its current execution state.

### Node Group

A workflow editor grouping around related graph nodes. GUI-authored node groups are execution-shaped parallel groups: they contain branch nodes and one join, and the fan-out is represented by one fan-out transition. A one-node group may exist only as an unsaved draft editing state.

### Start Node

The node where new tasks enter a workflow. A start node is non-executable and has no authored parameters.

### Task Start

An explicit operation that moves a newly created Task from start/backlog into its first executable current Node by applying the Start Node's outgoing Transition.

### Terminal Node

A sink node where workflow automation stops.

### Edge

A directed connection from one Node to another. In product surfaces, an Edge is presented as a Transition Branch.

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

The association that makes a Transition Parameter available to a target prompt or Join.

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

The immutable identity of one live agent or Script execution for a Task's Current Node and, when applicable, its parallel branch. Resume creates a new Exact Execution Scope only after the previous scope stops. Only a matching Exact Execution Scope proves that execution is live. Saved Task state, transcript entries, timestamps, Goals, and client state do not prove liveness.

### Resource Generation

The version of the live Session resources used by an Exact Execution Scope. Replacing those resources advances the Resource Generation. No stale handle can act on the replacement.

### Script Node

A workflow executable node that runs a local executable on the Kent server instead of starting an agent session. It reads incoming workflow parameter values from JSON stdin, writes workflow completion JSON to stdout, and uses stderr only for diagnostics.

### Session Contract

The model, provider, generation settings, enabled tools, and native web-search mode that a Session uses for one contract generation. These values stay fixed until a product operation creates a new contract generation. `compact_and_continue_session` creates a fresh target-node generation. Ordinary compaction can refresh system and reviewer instructions within the existing contract generation. Developer context remains part of the transcript.

### Runtime Parameter Contract

The possible and required Transition Parameters for an executable Current Node. Kent derives this contract from the latest Workflow definition when work starts or resumes. Kent does not retain an obsolete contract as execution history.

### Session

A durable Kent conversation associated with an Agent Node on a Task and, during parallel work, its Transition Branch Key. A Task can retain many Sessions because of loops, parallel branches, retries, or Context-Preservation choices. Script Nodes have no Session.

### Node Transition

An atomic Task state change through a Transition Branch. It removes completed source Nodes from Current Nodes, applies Parameter bindings and context preservation, and adds or blocks the target Nodes. Applied transitions are not retained as workflow movement history.

### Join

A non-agent fan-in Node that waits for the required current parallel branches before continuing. It exposes a read-only aggregate of incoming branch Parameters. Same-key incoming Parameter collisions are invalid.

### Task Interrupt

A resumable stop operation for one Session or every Exact Execution Scope on a Task. It waits for affected execution to stop and records the affected Current Nodes as interrupted. It does not delete the Task, Session, or worktree.

### Task Delete

An operation that removes a Task. Kent offers deletion only when the Task appears quiescent and checks that condition again before making changes. Deletion safely removes reconstructible managed artifacts, preserves Session artifacts, and removes the Task only after cleanup succeeds. Repeating cleanup for an already-absent managed artifact succeeds. Task Delete is separate from deleting a whole Workflow.

### Quiescence

The Task condition in which no work is executing, no automatic start is pending, and no conflicting lifecycle operation is in progress. Task Delete and whole-Workflow deletion require this condition.

### Question

A user-blocking ask emitted by a Session through `ask_question`. Questions carry prompt text, optional suggestions or options, an optional recommended-option index, and structured answer requirements.

### Orchestrator

An agent Node whose Transition Prompts ask it to coordinate work. Orchestration may happen inside one Session or through Workflow graph branches.

### Operational Stop State

A workflow/task state where auto-execution stops because the task is done, interrupted, blocked, or awaiting manual/user action.

### Workflow Execution

The product authority that sequences Workflow lifecycle changes, starts eligible work within the configured capacity, prevents conflicting changes, keeps related work together when possible, and reports live state.

### Automatic Intent

A temporary request for Workflow Execution to start eligible work automatically. Automatic Intents are lost on restart and are not reconstructed from saved Task state.

### Immutable Live Snapshot

A read-only view of live Workflow activity at one point in time. The view can become stale. Each operation checks the authoritative live state again before it changes a Task.

### Task Comment

A durable note on a Task. Deleting a Task Comment removes it completely.

## GUI

### Toast

A transient or persistent global notification surfaced by the desktop app. Toast and snackbar are equivalent terms in Kent GUI docs and code.

## TUI And Transcript

### Streaming Message

The temporary assistant message shown while a response is arriving. A Streaming Message is not committed transcript history. Committed assistant text replaces and finalizes it exactly once. If a completed response has no committed assistant text, Kent removes the Streaming Message with the typed `superseded` reason before the next model generation or before the response exits. This includes Question-only and tool-only continuation, Workflow preflight, external Workflow completion, reasoning-only or no-op retry, and final-answer-with-tools termination before final text is committed. A completed response with no active Streaming Message emits no stream-terminal event. Kent also removes the Streaming Message after interruption or abnormal termination. No assistant text can arrive after Kent finalizes or removes the Streaming Message.

### Thinking Status

The transient one-line description of a Session's current runtime work. While
the main Agent works, the server may update Thinking Status with its current
reasoning status. Thinking Status is live runtime presentation, not conversation
content or durable transcript history.

### Reasoning Trace

A provider-produced plain-text reasoning summary associated with an Agent Step.
Kent may project a Reasoning Trace progressively while it arrives. A completed
Reasoning Trace is durable transcript content. A Reasoning Trace is distinct
from Thinking Status.

### Ongoing Mode

The primary long-running TUI transcript mode. Ongoing Mode writes committed history to terminal Scrollback and shows live content below it. It does not own a separate scrollable viewport. It never rewrites lines that it has emitted.

### Detail Mode

The TUI mode for inspecting a bounded transcript page with selection, expansion, and line scrolling. The page changes when Detail Mode opens, when the selected Session changes, or when the user loads an adjacent page. Live events do not change the open page.

### Compaction-Preserved User Message

A user message retained in the model working set across a compaction boundary. Kent projects each preserved message into the transcript in provider order with detail-only visibility.

### Transcript Mode

The rendering posture for transcript entries. Current modes are ongoing and detail.

### Alternate Screen

Terminal buffer separate from normal scrollback. Kent avoids alternate screen for ongoing mode so persistent history remains in native terminal scrollback.

### Alternate Scroll

A terminal mode that converts wheel input into navigation events on Alternate Screen surfaces. Kent enables it only while a surface that supports wheel navigation is open. Ongoing Mode never enables it. The rollback editor ignores mouse input and also keeps it disabled.

### Mouse Capture

Terminal mode where the app receives mouse events instead of leaving them to native terminal selection. Kent keeps mouse capture disabled in ongoing and detail modes.

### Normal Buffer

Terminal buffer with native scrollback. Ongoing mode renders committed history here and treats emitted lines as immutable.

### Scrollback

Terminal-owned history of normal-buffer output. Kent does not replay, clear, or restyle committed ongoing scrollback after startup.

### Immutable Area

The Ongoing Mode content already emitted into Scrollback. Kent can append below it but cannot change, inspect, or compare it to decide later output.

### Mutable Band

The bottom part of Ongoing Mode that Kent can repaint. It contains unstable assistant output, live tool activity, prompts, pending messages, the composer, and the status line.

### Logical Line

One authored or rendered line that contains no line break created only by terminal width. The terminal can soft-wrap one Logical Line across several visible rows.

### Promotion

The act of appending stable Streaming Message lines from the Mutable Band into the Immutable Area.

### Re-emission

Appending content to the Immutable Area when equivalent content was already emitted there.

### Reconciliation

Comparing retained or received transcript data to emitted terminal content to decide what to render, suppress, reorder, or replace.

### Scratch Rehydration

Recovery that erases the Mutable Band, reopens the Session, and appends the active transcript segment below existing Scrollback. It does not inspect or change the Immutable Area.

## Runtime Steering And Goals

### Active Session Runtime

The shared live resource for one Session. Interactive clients, headless runs, and Workflow execution use the same resource as equal control surfaces. An available but idle Session is not a live execution.

### RuntimeActivity

The authoritative live status of a Session. It reports whether the Session is unavailable, starting, idle, running, awaiting a live Question or Approval, finishing, or closing. It also identifies exclusive work such as a user turn, Goal continuation, Workflow execution, compaction, shell command, or background step. Running and waiting require a matching Exact Execution Scope. Saved state and client-local assumptions cannot make a Session appear active.

### Runtime Command

The ordered operation through which model-visible human input, Workflow completion, Goals, and technical input enter an Exact Execution Scope. Runtime Commands determine acceptance, ordering, replacement by newer input, and whether rejected input returns to the user's draft. Answers to a live Question resolve that Question directly.

### Completion Fence

The point at which Workflow completion can become final for an Exact Execution Scope or one unambiguous idle executable Current Node. Human input accepted before the Completion Fence replaces pending completion and continues the same execution. Input that arrives after the fence is rejected, and the client restores its draft.

### Runtime Gate

A temporary guard that blocks conflicting changes while Workflow Execution changes live execution state. A Runtime Gate is lost on restart.

### Append Certainty

The result that tells Kent whether a Session change became durable. If a change did not commit, Kent can retry it and must not show it as applied. If a change committed, Kent applies it exactly once and must not retry it, even when later notification fails.

### ReadModelVersion

The increasing Session value that orders live status, prompts, main-view updates, and interruption results. Clients ignore updates older than the newest value they have accepted. A Read Model Version orders facts; it is not a transcript position.

### PendingModelRecovery

Saved recovery information for a model-visible step that was interrupted or crashed. Pending Model Recovery can identify unfinished tool calls for later repair. It never proves that execution is live or makes a client show the Session as busy.

### DraftRecoveryBuffer

An ordered collection of local input that the TUI preserves after an early exit. Opening the Session restores eligible text to the composer. It never sends or resumes that work automatically.

### DraftInputBuffer

One preserved text entry and its recovery category. Categories distinguish submitted text, queued text, pending or locked injected input, and reviewer input. A Draft Input has no delivery state and remains separate from the visible composer text.

### Forced Local Detach

The second-`Ctrl+C` exit path while an interrupt is pending. The TUI exits and detaches without force-closing the shared Active Session Runtime.

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

The ordered set of Steer operations that wait for the current Agent Step to finish. User steering, drained Queue messages, worktree changes, Workflow output, mode changes, and errors use the same ordering behavior.

### Equal Full-Control Attach

Every client attached to a Session has the same control capabilities over the shared Active Session Runtime. Kent has no controller client, limited-control client, read-only attachment, or client lease.

Client connection state is not Session state. A client connection or disconnection for any reason never starts, stops, pauses, cancels, closes, replays, restores, or otherwise changes a Session, Agent Turn, Goal, Queue, Steer, worktree operation, or accepted command. Only an accepted command changes server state. The server publishes every event without using subscriber count as a condition: zero connected clients do not suppress publication, and every connected client receives each applicable broadcast.

### Goal

A persistent self/user-declared objective with a continuation loop (nudges, suspend/resume, premature-stop reminders) that drives turns until the goal is completed, paused, or cleared. A goal may be set by the user or by the model itself, including inside a workflow-controlled Session.

### Goal Continuation Loop

The driver that repeats the step loop to keep working a goal across Agent Turns, injecting goal reminders. It does not operate while an Exact Execution Scope is driving the Session for Workflow Execution—the workflow turn loop is the single continuation driver there, and the Goal stays a passive objective folded into the workflow's continuation nudge.
