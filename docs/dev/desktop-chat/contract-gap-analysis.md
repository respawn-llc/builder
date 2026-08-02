# Desktop Sessions And Chat Contract Gap Analysis

This document classifies the current contract surface. “Reusable” means the domain capability exists; it does not mean the desktop TypeScript adapter is implemented.

## Reusable Server Contracts

### Session Discovery And Lifecycle

- Project-scoped typed session pages with newest/older/newer positions.
- Typed main/subagent categories.
- Open/create/rollback transition planning.
- Execution-target inspection and workspace retarget.
- Initial input and draft persistence.
- Runtime activation and release.

### Transcript

- Bounded directional transcript pages.
- Hydration-first live transcript subscription.
- Ordered message sequence.
- Committed rows and live assistant stream deltas.
- Tool, prompt, queue, runtime, compaction, goal, background, worktree, and diagnostic messages.
- Existing typed tool start/end facts share `ToolCallID` and already support one merged tool row as used by the TUI. Desktop must reuse that seam rather than inventing a reconciliation subsystem or requiring a server-tool refactor.

Provider/storage encodings, materialized-versus-synthesized provenance, repair paths, and deduplication bookkeeping are not Desktop product contracts. Suspected dead or legacy duplication is tracked separately in `KENT-303`.

The TUI currently permits expanding a backgrounded Shell/process row while revealing no additional content. Desktop must not copy that behavior: backgrounded tool rows and later completion/kill rows expand to the exact model-visible result/notice payload.

Raw workflow active/run/task/workflow IDs carried in transcript session status are behavior-gating facts, not a TUI-visible Chat status contract. Desktop must not turn those raw IDs into transcript/status rows. Workflow-linked Chat exposes only a typed Task short-ID/title navigation row backed by the authoritative task read model; raw Run and Workflow IDs remain omitted.

Current malformed-row integrity branches are defensive/legacy compatibility, not Desktop product contracts. Desktop must not reproduce recoverable or unrecoverable fallback-row UX. Such payloads are contract violations: debug fails fast, while release uses the product-level transcript failure/recovery path without fabricating row content.

### Runtime Operations

- Submit user turn.
- Queue, steer, submit queued work, and discard queued work.
- Interrupt.
- Session settings and rename.
- Context compaction and goal operations.
- Ask/approval answers.
- Process and worktree operations.

## Desktop Adapter Work

The desktop client needs typed schemas, DTO adapters, RPC methods, and contract tests for:

- Session pages.
- Session main view and transcript pages.
- Transcript subscription.
- Runtime activation/release.
- Draft read/write.
- Submit/queue/steer/discard/interrupt.
- Session settings, compaction, goals, processes, and worktrees.
- Ask and approval operations for session chat.

These are client gaps, not reasons to duplicate server business logic in React.

## Confirmed Product/Contract Decisions Needed

### Reviewer Feedback Projection

The current runtime fans one authoritative reviewer suggestion list into two representations:

- a model-facing `reviewer_feedback` control instruction; and
- optional preformatted reviewer-suggestions transcript text gated by `Reviewer.VerboseOutput`.

Desktop requires one typed, structured Reviewer feedback fact. Every nonempty reviewer result persists/projects exactly one client transcript row regardless of the legacy verbose-output setting. The model-facing instruction remains internal runtime/provider context and never becomes a second client row.

Reviewer lifecycle and reviewer failure remain separate typed facts. Clients must not infer reviewer semantics from generic notice text or diagnostic codes.

### Chat Sidebar Host

Chat uses the existing global sidebar host rather than adding an embedded or feature-local host. Contextual destinations render in `shift` mode on wide windows and `overlay` mode on compact windows.

The implementation gap is typed Chat destinations and adapters. `custom: ReactNode` is not an acceptable permanent destination because it bypasses typed identity, sizing, lifecycle, and pop-out/navigation contracts.

Inline transcript expansion is the sole full-detail surface for generic tool results, Patch/Edit diffs, source/file snapshots, completed Ask Question history, injected context, Reviewer feedback, compaction summaries, and other text summaries. Desktop must not add duplicate sidebar destinations for them.

Session settings/details are also not a sidebar destination. They use the shared Popover substrate in an upward-opening under-composer composition. Agent and Supervisor use adjacent option dropdowns; Thinking and toggle controls stay inline. The Desktop gap is typed setting rows/options and authoritative mutation adapters, not another navigation surface.

Processes is one typed list-only destination in that host. Desktop adds no process-detail destination or nested list/detail navigation. Its server scope, ordering, lifecycle, retention, and collection-contract fixes remain owned by the existing out-of-scope `/ps` ticket; the Desktop initiative only designs the native presentation and adapters.

The native presentation copies the TUI row information hierarchy and server order, uses dense non-expandable rows, intentionally omits `/ps inline`, maps Kill to an immediate no-confirmation action with pending state, and refreshes automatically without a manual Refresh control.

Desktop also intentionally omits `/ps logs`. Killable rows expose an always-visible trailing icon-only action. Initial loading, error/Retry, and empty states reuse the existing shared non-full-page Desktop state components; no process-specific state system is added.

### Session Destination Scope

The server list is project-scoped. A Home Sessions destination could be:

- Project-scoped.
- Global across projects.
- A global shell with project grouping/filtering.

A global destination requires a deliberate server read model and cursor contract; the client must not fetch every project and merge pages in memory.

### Session Summary Shape

The existing summary is sufficient for the TUI recency picker but may be too sparse for a desktop session browser. Candidate additions include:

- Project and workspace identity.
- Task/workflow/run association.
- Runtime activity.
- Pending attention.
- Session target availability.
- Model/provider facts.

The owning server read model and update mechanism must be decided before adding fields opportunistically.

### Live Session-List Updates

No session-list subscription exists. The design must choose between:

- Explicit refresh plus reconnect refresh.
- Server invalidation events followed by page refresh.
- A session-index subscription with typed change events.

The client must not infer liveness from transcript rows or maintain a second activity authority.

### Queue Item Editing

Desktop Pending Work intentionally has no edit, reorder, submit-now, or clear-all action. It needs one server-owned cursor-paginated read model containing both Steer and Queue items with typed identity, kind, original text, per-kind FIFO order, and removal state. Queue items project before Steer items. The client must not reconstruct this list from transient transcript lifecycle events or maintain the TUI's local queue.

The existing queue-item discard operation is reusable for server queue items, but the complete cross-kind removal contract is missing. A successful initiating-client removal restores the original item text to that client's composer; a failed removal leaves the item and input unchanged. No edit RPC is required.

The TUI specification's unbounded in-memory pending queues violate the repository's bounded-memory invariant and are not a reusable architecture. That pre-existing runtime/storage correction belongs to a separate prerequisite issue, not the Desktop Sessions/Chat initiative. Desktop's peeking sheet has a fixed semantic max-height and consumes the prerequisite's bounded cursor contract; it does not redesign queue storage or execution.

### Prompt History

The TUI receives server-supplied history during its attach flow and records history through runtime control. The desktop route does not have an exposed adapter or confirmed standalone history-read operation. The required read contract and storage cap must be verified after BUI-186.

### Repository Path Suggestions

The TUI builds a local corpus by running `rg` in the workspace. A thin remote Desktop client cannot reproduce that ownership.

Desktop uses a server-owned bounded suggestion query over the session's effective working directory. It preserves TUI matching scope—files, derived directories, hidden paths included, `.git` excluded, server fuzzy order—and returns typed repo-relative path plus directory identity. The client never scans its filesystem, receives the complete corpus, or implements matching rules.

The picker shows at most seven rows and inserts the exact `@`-prefixed path, adding `/` for a directory. A bounded top-suggestion contract is search, not pagination; it must return an explicit capped result rather than a fake page of an unbounded client-held collection.

### Client Attachments

The existing submit, Queue, and Steer contracts carry text only. TUI clipboard images rely on a client-local temporary file path, which is invalid for a remote-capable Desktop client.

Client file/image uploads, drag-and-drop, clipboard-image attachments, server-owned draft blobs, attachment persistence, and transcript attachment presentation are explicitly outside this initiative and must not produce a task in its implementation graph. Workspace `@` path references remain in scope as textual server-workspace references.

### Independent Interactive Sessions

The earlier design attached only to task-run sessions. The Home Sessions direction and desktop-only product goal require an explicit create/open contract for ordinary sessions, including project/workspace selection, model/session setup, draft behavior, and route ownership.

The ratified desktop flow keeps TUI's zero-form and lazy semantics:

- primary creation uses the project default workspace;
- an alternate sidebar destination uses the existing cursor-paginated project workspace list;
- worktree selection remains post-open;
- no name/model/provider/role fields are added;
- no durable session exists until the first agentic trigger.

Current `SessionLaunchService.PlanSession` creates an independent session plan but `server/launch.Planner.createSession` calls `EnsureDurable` before first model use. That code/spec drift must be resolved at the server ownership boundary rather than hidden by a desktop-only workaround.

### Thinking Status And Reasoning Traces

The current transcript feed combines two product concepts in
`TranscriptReasoningUpdate`: cumulative Reasoning Trace text and an optional
current Thinking Status. Hydration retains only one active reasoning update,
while completed traces are persisted later as detail-only reasoning local
entries.

The shipped contract already supports cumulative live trace updates and an
explicit provider-attempt reset. It does not yet provide the complete Desktop
contract:

- Thinking Status and Reasoning Trace need distinct typed ownership so clients
  cannot conflate live runtime status with durable conversation content.
- Hydration and live delivery must preserve every server-provided trace and its
  authoritative order rather than exposing only one ambiguous active value.
- One server-identified provisional trace must reconcile with its committed row
  without duplication. A provider-attempt reset removes only the discarded
  provisional trace and retains Thinking Status.
- The server presentation projection must provide the compact first logical line
  and complete plain text. It must move the TUI's existing removal of outer
  literal `**` delimiters to the shared server projection while leaving
  persisted and model-facing content unchanged.
- Runtime activity and active-work kind already provide the authority for
  Thinking Status visibility and fallback copy. Desktop must not infer activity
  from transcript rows, local booleans, or reasoning-text presence.
- Committed Reasoning Traces need the stable row identity and authoritative
  timestamp owned by the transcript-row metadata task.

Kent does not retain authoritative Reasoning Trace duration. That optional
future capability is independent of initial Desktop parity. Its approved
meaning is elapsed server time from the first nonempty update for one trace
through that trace's commit.

### Session Settings And Lazy Draft

The existing server surface provides useful but incomplete pieces:

- capability facts expose `SupportsThinking` and ordered supported Thinking modes for each model;
- readiness exposes ordered Agent role names, but not the configured descriptions or each role's effective model/Thinking summary;
- runtime status exposes Thinking, Fast availability/enabled state, Supervisor frequency/enabled state, Questions, Auto-compaction, compaction mode, conversation freshness, and workflow linkage;
- session launch accepts Agent and Thinking overrides, but not the complete six-setting Chat draft;
- runtime control can set Thinking, Fast, Questions, Auto-compaction, and Supervisor enabled state, but cannot atomically select Supervisor Off, After edits, or Always;
- runtime/session projections do not expose the selected Agent role or explicit cache-lock state.

Desktop's lazy New Session design requires one server-owned draft aggregate containing the unsent message plus Agent, Supervisor, Thinking, Fast, Questions, and Auto-compaction. First Send atomically validates and applies that complete draft, creates/launches the session, and submits the message. Partial setting application must create no session and send no prompt. Persisting six independent client settings or issuing post-launch setup mutations is not an acceptable substitute.

The same server-owned aggregate persists after a session exists. Desktop adds no GUI-local or per-window draft store and no live collaborative composer synchronization. Simultaneous multi-client draft editing is not a product mode; the server remains the sole persisted draft authority.

After launch, non-Agent settings use runtime controls. Supervisor requires one typed atomic mode operation shared by TUI and Desktop; clients must not compose separate enablement and frequency mutations. Agent remains immutable after the first model dispatch.

The current Fast, Supervisor, and Questions control paths append committed system-feedback entries, while Auto-compaction follows a different path. Desktop settings changes create no transcript rows. The shared server control seam must be normalized so control state and Sonner failures own feedback without client-specific hidden rows.

The settings read model also needs:

- selected Agent role and cache-lock state;
- each available role's effective model and Thinking effort;
- effective current model and Thinking effort for the compact line below Agent;
- authoritative Questions availability for the selected Agent;
- enough compaction policy to distinguish workflow-required Auto-compaction from Manual-only disabled behavior;
- previous-session navigation and copyable Session ID;
- a typed Task summary/navigation target for workflow-linked sessions.

Provider, workspace/worktree/branch, compaction count, raw Run/Workflow IDs, and parent-agent lineage are not part of this settings read model.

### Context And Manual Compaction

The runtime main view and transcript feed already expose authoritative context-window usage and typed compaction lifecycle. Runtime control already accepts manual compaction with an optional guidance string.

Desktop preserves the terminal `/compact` flow instead of adding a separate guidance form. Bare `/compact` requests manual compaction without guidance; text after the command is passed as guidance through the same typed operation.

When no Agent Step is active, manual compaction may start immediately. During an Agent Step, both the slash command and Context button must enter the same server-owned Steer/Pending Work path as other next-boundary control. The item executes after the current Agent Step and before the next Agent Step. It must not wait for turn completion and must never become a model-visible user message.

Pending Work must therefore represent a typed compaction control item rather than relying on display-text parsing. The Desktop client renders that item in its familiar `/compact` form, including optional guidance. Repeated requests remain distinct Steer items; neither client nor server silently coalesces them.

The separate Context meter opens a compact detail pop-up. Its `Compact` action uses that same operation with no guidance. The pop-up must not become a second manual-compaction input model.

The pop-up reproduces the TUI Context summary without its detailed instruction, skill, and Agent-file token breakdown. It needs remaining tokens and percentage against the context window, automatic-compaction threshold tokens and percentage, Auto-compaction state, and completed compaction count. The runtime main view already provides usage, Auto-compaction state, and count, but it does not expose the configured automatic-compaction threshold. The server read model must add that typed fact; Desktop must not derive policy by reading configuration.

An open pop-up consumes the ordinary Session-status and context-usage broadcasts already used by Chat. Desktop must not add a Context-specific poll, refresh timer, or reconciliation state machine. Facts not changed by an ordinary broadcast refresh through the next standard authoritative snapshot.

Lazy New Chat needs the same facts before a Session runtime exists. The server-owned Chat draft must project the effective draft Agent's context window, automatic-compaction threshold, and Auto-compaction state. Desktop presents zero used tokens and zero compactions until materialization. Agent or setting changes that alter the effective context contract update this one draft projection.

Manual compaction is unavailable before the first Agent Step. A pre-Session `/compact` must remain a recognized command and return the typed unavailable or too-soon outcome without materializing a Session. Desktop must not create a throwaway Session merely to reject compaction.

The remaining contract work is that threshold projection and the Desktop command adapter, plus a typed step-boundary compaction Steer item in the server-owned Pending Work contract. The current TUI has separate immediate dispatch and client-local turn-drain command paths; those are not the Desktop architecture and do not satisfy the next-Agent-Step contract.

Only one compaction may be active. Server admission must expose typed manual compaction rejection reasons for disabled policy, active compaction, and no Agent Step boundary since Session creation or the latest successful compaction. A request queued during an active Agent Step may use that step's boundary to become eligible. Repeated pending requests are evaluated in order, so the first may compact and later requests may fail as too soon.

Desktop disables the Context action while active compaction is authoritative, but it adds no client-owned cooldown or Step counter. Raced button actions, slash commands, and drained pending items all use the same server rejection contract. Failed items leave Pending Work and use Sonner unless the server explicitly identifies a durable transcript error as the feedback owner. Desktop never creates a local error row.

User-triggered compaction closes its initiating transient surface. Automatic compaction does not close an already-open Context pop-up; the pop-up remains readable with its Compact action disabled. The compact Context meter switches to a compacting presentation for every compaction origin.

Successful user-requested compaction follows the TUI focus policy. A focused Desktop window stays silent. An unfocused Desktop window sends a system notification only after the Pending Work drain following compaction is idle; activation opens the owning Session at latest. Automatic, pre-submit, and handoff compaction do not send a completion notification. This is transient client lifecycle feedback, not a durable attention-feed item.

Compaction policy, ordering, pre-submit compaction, and execution remain server-owned. Desktop introduces no second compaction queue or client state machine.

`compaction_mode=none` remains the authoritative fully disabled policy. Desktop must not call it Manual-only. In that state, the Context detail suppresses the irrelevant threshold, reports compaction and Auto-compaction as unavailable, and disables its Compact action. `/compact` still dispatches the typed operation so the server's disabled-policy error is surfaced rather than sending the text to the model.

Existing onboarding language that calls `none` Manual-only is product drift. Its correction is a separate follow-up candidate and is not part of Desktop Sessions/Chat.

### Goal Control

The server already owns durable Goal state and typed set, pause, resume, complete,
clear, and show operations. Goal inspection works for live and dormant Sessions.
Runtime and transcript projections broadcast objective, status, and the
runtime-local suspension fact. The unary Goal projection also carries created
and updated timestamps.

Desktop's locked Goal design exposes a narrower user contract:

- Goal suspension is not a Desktop product state and must not affect Goal copy,
  controls, or visual state.
- The sidebar needs Goal created time for `Set <age> ago`, but the current
  `clientui.RuntimeGoal` and transcript Goal projection drop both timestamps.
- The under-composer control can use the ordinary Chat Goal snapshot, while the
  sidebar intentionally performs one fresh ShowGoal read on open and then
  consumes ordinary Goal broadcasts.
- A Goal broadcast that arrives while the open read is pending must win over the
  late unary response. This is one bounded client rule, not a Goal revision,
  retry loop, or poller.
- The selected Agent's authoritative locked-tool capability must distinguish
  missing `ask_question` from Questions being toggled off. Only the former makes
  Save and Resume unavailable.

Runtime control and its tests allow user Goal mutations in workflow-controlled
Sessions. That behavior is authoritative. Desktop must expose the same Goal
affordance and mutation controls there rather than inventing a workflow-specific
read-only mode.

Goal Set currently requires an existing Session ID. Lazy New Chat therefore
needs a first-agentic-trigger flow that materializes the Session and then applies
the Goal operation. Product does not require rollback if later Goal validation
or admission fails: the Session remains, Goal work does not start for the failed
request, the Goal draft remains available in the open sidebar, and the error is
surfaced. Once Goal work is accepted, later provider, tool, or runtime failure
uses ordinary Session failure behavior and leaves the Session and Goal intact.

The current Task Description Markdown field is feature-local. Goal must not copy
it. The editor/read-view, overflow, Markdown, focus, accessibility, and draft
reconciliation behavior need one shared UI-kit module used by both Task Detail
and Goal. Save and Goal lifecycle controls remain outside that field.

Desktop deliberately does not copy two TUI presentation mechanics: active-Goal
Clear has no confirmation, and Goal mutation requests are single-flight rather
than client-coalesced newest-request state.

### Project Workflow Links Read Model

The current project-workflow link response is unpaginated and contains only link IDs plus default state. The current workflow list is cursor paginated but does not produce one project-link row with workflow identity, default state, and execution validation. The project `Workflows` tab requires one server-owned cursor read model combining those facts.

Server set-default and unlink routes exist, but the desktop client has no adapters for them. The project-workflow link work must expose those operations without client-joining complete link and workflow collections.

### Attention Outside Workflow Tasks

The attention contract and server already carry a typed `session_prompt` target for ordinary interactive Session Questions and Approvals. Desktop parses that target but its notification controller currently ignores every target that is not a Workflow Task. Desktop Chat must route the existing target through the same focused/unfocused attention surfaces and open the owning Session picker. It must suppress only the duplicate in-app notification while that exact Session Chat and picker are already focused. It must not create a second notification mechanism or a Session-specific grouping or sound policy.

Ordinary interactive Session failures and turn completion still need a typed target and durable read model if they should appear in Home Inbox or system notifications.

The current durable global attention page exposes paged items and no aggregate unresolved count. Project summaries expose per-project attention counts, but a client cannot sum cursor-paginated project pages into a global count and those counts do not cover the required ordinary-session attention. An Inbox badge therefore requires an authoritative aggregate field on the expanded global attention read model.

The Inbox badge is an optional standalone feature. Its server aggregate and desktop presentation must be implemented in their own task rather than expanding the Home redesign or session-attention task.

### Prompt Batch Answers

The server projects multiple pending Questions and Approvals with typed prompt identity, creation order, Step identity, suggestions, recommendation, approval decisions, and tool provenance. Existing answer operations resolve one prompt per RPC.

Desktop intentionally gathers answer drafts for one visible prompt batch and sends nothing until every prompt is explicitly answered or declined. Existing `TranscriptPrompt.StepID` is the batch identity; prompts use existing server order. No second batch identifier is needed.

Desktop needs one typed batch-answer operation. The operation resolves submitted entries that remain pending and returns typed resolved and stale/skipped prompt IDs. A stale prompt is never overwritten. An all-stale request is an idempotent no-op. This is a narrow prompt-control contract addition; it does not replace the existing parallel ask/approval runtime architecture.

Each batch entry needs a typed disposition: answered ordinary Question, answered Approval, or declined prompt. Declined must not be encoded through display text or a client-chosen error string. The server adapts the typed disposition to the runtime's prompt-cancellation behavior.

That cancellation preserves the existing transcript outcome. An ordinary Question remains an error/canceled Ask Question tool row instead of becoming a completed answered Question. No synthetic user message is added, and a declined Approval adds no separate decision row.

The client may own selected/freeform/answered/declined form state only while the picker remains open. Navigation inside that picker preserves the state. Route changes, connection loss, browser refresh, and Desktop relaunch discard it. Pending prompt identity, ordering, membership, and final resolution remain server-authoritative.

The existing per-prompt resolved transcript broadcast is sufficient for the normal cross-client path. It carries identity but not answer content. Although the answer exists at the execution-prompt submission boundary, exposing it would require threading new data through the execution feed, registry, transcript DTO/wire contract, clients, protocol version, and tests. That expansion is outside this initiative.

Desktop therefore discards local form state and removes an externally resolved prompt from the picker without answer or placeholder presentation. The batch response handles races where the broadcast has not arrived.

The owning implementation task also requires a developer-only browser fixture that repeatedly supplies synthetic prompt batches without model or production server dependency. It keeps generating the next batch after submission until the tab closes. Agent QA must exercise the fixture before the user acceptance gate. Completion requires an explicit user Approve response; a Reject response with findings returns the task to implementation and repeats both QA stages.

### Rollback/Fork Entry Point

The transcript page exposes rollback candidates, and session transitions can fork. A per-message desktop affordance needs a server-authoritative eligibility fact; the client must not infer candidate eligibility from row text or type alone.

### Committed Message Timestamps

Persisted transcript events have a server-assigned `Timestamp`, but `runtime.ChatEntry`, `runtime.TranscriptCommittedRowFact`, and the `clientui` committed-row DTOs do not carry it. The timestamp must survive both bounded hydration and live subscription projection as one authoritative committed-row instant. Desktop must not derive message timestamps from client send, receive, or render clocks.

### Transcript Row Identity

Committed transcript rows do not expose a unique stable row identity. `StepID` is shared by multiple rows, and live subscription `Sequence` is transport ordering rather than persisted row identity. TanStack Virtual's end anchoring requires a stable server-projected item key so it can identify the same row across prepend, append, trim, and hydration. The key is ordinary row identity, not a client-owned scroll registry or persisted presentation marker.

The required contract is a typed session-scoped locator containing the durable event sequence and event-local projected-row ordinal. Runtime projection must preserve source sequence/ordinal through buffered assistant/tool turns, compaction replacement rows, bounded pages, hydration, and live committed rows. This is derived from existing events and does not change JSONL persistence.

### TanStack Chat Ownership

The resolved `@tanstack/virtual-core` contract already exposes `anchorTo`, `followOnAppend`, `scrollEndThreshold`, `isAtEnd`, and `scrollToEnd`. The shared `VirtualizedInfiniteList` instead captures a leading item key and intra-row offset, compares old and new key arrays, writes `scrollTop`, and invokes manual load effects. Chat must not use that scroll-management path. Its deep list module must compose bounded TanStack Query cursor pages with TanStack Virtual's official chat contract and expose only declarative product inputs.

### Streaming Markdown

The shared `MarkdownText` uses `react-markdown`, GFM, sanitization, and Kent's external-link policy, but it reparses the complete source string and has no syntax highlighter. TanStack Virtual can measure and anchor a growing row but does not bound Markdown parse or highlight CPU.

The approved candidate is one shared Streamdown-backed UI-kit renderer with streaming/static modes and optional library-owned highlighting. It requires a hard security, performance, and integration-size gate. Failure selects plain streaming text and the shared static renderer on commit; chat must not add a custom incremental parser or maintain parallel Markdown implementations.

### Fixed-Row JSONL Pages

Existing transcript cursors identify compaction-segment byte boundaries. A segment can approach 9MB when the active context is roughly 1–1.5 million tokens, so desktop virtualization alone cannot bound transfer, JSON decoding, or Query-cache payload.

Correct arbitrary byte slices are not independently projectable. Assistant tool turns span multiple events, `tool_completed` data is joined to message events, synthesized versus materialized tool results depend on later events, and `history_replaced` changes projection state. `PersistedTranscriptScan` row offsets and limits still require replaying and cumulatively counting the preceding projection, which is not an acceptable cursor contract.

JSONL-backed desktop transcript pages therefore remain compaction segments. The design rejects a JSONL side index, resumable projector checkpoints, cumulative row-offset cursor, and persisted page markers. A roughly 100-row cursor page belongs to the SQLite transcript storage/read-model migration.

## Contracts Requiring Revalidation Before Implementation

- Transcript message union shape after KENT-257.
- Hydration ownership and subscription sequencing after KENT-258.
- Queue failure presentation after KENT-272.
- Prompt-history read/write contract after BUI-186.
- Session-picker update metadata after KENT-278.
- Protocol version at the start of each contract-changing task.
- Exact TanStack Query `maxPages` retention and resolved Virtual package versions before transcript-list implementation.

## Terminal-Shaped Behavior To Exclude

- Native terminal scrollback and immutable emitted lines.
- Alternate-screen modes and terminal control sequences.
- Terminal cursor ownership and editor emulation.
- BEL/OSC notifications.
- Ongoing/detail mode toggle.
- Esc-Esc rollback arming.
- Ctrl+C dual meaning for interrupt and application exit.
- Slash command parsing as the primary discovery model, except for commands whose exact command flow is explicitly preserved by the Desktop Chat spec.

The desktop must preserve their underlying capabilities while replacing these interaction mechanisms.
