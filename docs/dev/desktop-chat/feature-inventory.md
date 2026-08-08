# Desktop Sessions And Chat Feature Inventory

This inventory describes the repository baseline on July 18, 2026. It is a research snapshot, not a product specification.

Every capability in this inventory belongs to the desktop parity design. “Desktop translation question” means the desktop interaction is unresolved, not that the capability is optional.

## Session Discovery

### Shipped Server And TUI Behavior

- Session listing is project-scoped.
- Sessions are divided into typed `Sessions` and `Subagents` categories.
- Interactive opening of a Subagent session promotes it to Sessions only after target validation and any accepted retarget complete.
- Each category is recency ordered and cursor paginated.
- The TUI requests 50 rows and retains at most two bounded pages per category.
- Session summaries contain session ID, category, name, first-prompt preview, and updated time.
- The current list contract has no search, filters, alternate sort, total count, row activity, task/workflow identity, workspace metadata, or attention summary.
- Initial category windows load independently and expose independent loading/error states.
- Opening an existing session validates its execution target and may require an explicit workspace retarget.
- Opening restores the persisted input draft verbatim.
- Multiple clients may attach with equal control.

### Existing Desktop State

- Home has Projects and Workflows tabs plus the attention Inbox.
- There is no Sessions destination, session route, session query key, session API adapter, or session-list component.
- `IslandTabs`, `VirtualizedInfiniteList`, `InfiniteListBoundary`, loading/error/empty states, and route abstractions are reusable.
- Last-route restoration persists project/workflow context only.

### Desktop Browser Decisions

- Sessions use dense full-width list rows.
- Rows show only title, first-prompt preview, and recency.
- Ordering remains server recency order.
- Sessions/Subagents category chips are the only filters; the browser has no search or additional status filters.
- Row click opens full-page chat. Other session actions use the context menu; native pop-out trigger placement is unresolved.

### Desktop Chat Layout Decisions

- Chat is edge-to-edge with no enclosing level-0 island.
- The chat area is capped at 1200px and user/assistant conversational islands at 1000px through semantic layout tokens.
- Conversational messages reuse the shared island primitive; there is no bespoke bubble component.
- User islands are right-aligned and assistant islands left-aligned; both are content-sized to the maximum.
- Both roles use the same ordinary island level and tokens.
- Intrinsic layout owns content sizing; no line parsing or text-measurement state is introduced.
- Conversational islands have no avatars or role labels.
- User messages collapse after 10 rendered lines; assistant messages remain fully expanded without a collapse control.
- Timestamp and message actions live in an external footer below each conversational island.
- Same-role islands use smaller spacing than role changes.
- Same-role conversational groups copy Respawn's compact role-edge corner geometry while retaining separate islands.
- Tools and diagnostics use flat transcript rows.
- Chrome owns session title and Back.
- The shared embedded/inline sidebar opens contextual typed details such as files, plans, diffs, tool results, questions, trees, and summaries.
- One input-field/status island is required; its elements are designed individually.

## Transcript History

### Shipped Server Contract

- Transcript history uses opaque directional cursors, never offsets or total counts.
- A page may request older or newer content, not both.
- Pages carry bounded entries, older/newer cursors, edge facts, session identity, conversation freshness, and the latest rollback candidate.
- JSONL pages are whole compaction segments rather than fixed row-count pages. A large-context segment can approach 9MB, so desktop virtualization does not bound transfer or cache payload.
- No production transcript path may read `events.jsonl` from byte zero to EOF outside the ratified exemptions.
- The live subscription sends hydration first and then ordered transcript messages.
- Transcript messages cover committed rows, assistant deltas/abort, reasoning, tool activity, queued work, runtime state, session identity/status, compaction, context usage, goals, prompts, background processes, worktree outcomes, and operational diagnostics.
- Streaming assistant text is identified by stream identity and reconciled with the authoritative committed final row.
- Persisted transcript events are server-timestamped, but committed runtime facts and client DTOs do not expose that timestamp.
- Committed transcript rows lack one unique stable row key; `StepID` is not row identity.
- The resolved TanStack Virtual core exposes official end anchoring, append following, end detection, and scroll-to-end APIs. The existing shared infinite-list wrapper performs manual anchor/offset adjustment and is not the chat ownership model.

### TUI Presentation To Translate, Not Copy

- Ongoing mode provides a minimal live append-only view.
- Detail mode provides bounded inspection, expansion, and cursor-edge loading.
- Desktop needs the information fidelity of both modes without terminal buffers, alt-screen, or the detail camera state machine.
- Tool inputs remain visible for success and failure.
- Completed tool results, diagnostics, unknown recoverable rows, and developer/context entries remain inspectable.
- User and assistant Markdown, tool previews, diffs, notices, questions, reviewer entries, compaction, interruption, and background activity have typed presentation metadata.

### Existing Desktop State

- No TypeScript transcript union or Zod decoder exists.
- No session transcript subscription adapter exists.
- No bounded transcript cache/reducer exists.
- No message timeline, streamed assistant row, tool activity row, expansion model, or jump-to-latest behavior exists.
- Shared `MarkdownText` and semantic theme tokens are available.
- The installed `@tanstack/react-virtual` is 3.14.5; its installed types do not include the newer chat anchoring APIs.

## Session Runtime Control

### Available Server Capabilities

- Activate and release a session runtime.
- Submit a user turn.
- Queue a user message while busy.
- Steer a live run at safe boundaries.
- Submit or discard queued user messages.
- Interrupt the current turn/tool while keeping the session.
- Persist and restore input drafts.
- Rename the session.
- Change Thinking, Fast mode, Supervisor enabled state, Auto-compaction, and
  Questions settings. The existing runtime operation cannot choose Supervisor
  After edits versus Always.
- Trigger compaction and observe context usage.
- Show and control goals.
- Answer questions and approvals.
- Inspect and control background processes.
- Inspect and change the session worktree target.
- Read the latest committed assistant final answer.
- Navigate session lineage and create rollback forks through typed session transitions.

### Desktop Gaps

- The desktop API client exposes none of the session runtime controls.
- No attach/release lifecycle is owned by a desktop destination.
- No reducer reconciles operations with transcript events.
- No chat draft restoration or release-time persistence is wired.
- No desktop queue editing contract exists.
- No desktop process, worktree, goal, compaction, or session-settings surface exists for chat.
- Runtime/session projections do not expose the selected Agent role or explicit
  cache-lock state.
- Server readiness exposes Agent role names only; role-effective model/Thinking
  summaries are not projected.
- Lazy launch accepts Agent and Thinking overrides only. It cannot atomically
  accept the complete six-setting draft selected before first Send.
- Fast, Supervisor, and Questions controls currently persist system-feedback
  entries; the approved Desktop settings behavior creates no transcript rows.

## Composer And Pending Work

### TUI Behavior Inventory

- Multiline grapheme-aware editing and prompt history.
- Idle submit versus busy queue/steer behavior.
- Strict FIFO pending work.
- Visible queued and steering panes.
- Interrupt recovery of pending text.
- `@` repository-path autocomplete.
- Clipboard image and text handling.
- Draft persistence across abrupt and graceful detach.
- Run-safe navigation commands while a server-owned run continues.

### Desktop Translation Questions

- Desktop-native send, queue, steer, stop, edit, and discard controls.
- Whether queue and steer remain distinct user-facing concepts.
- Prompt-history discovery and recall.
- Path/file attachment and autocomplete model.
- Workspace path autocomplete remains in scope. Client file/image uploads,
  drag-and-drop, and clipboard-image attachments are explicitly excluded from
  this initiative and its task graph.
- Draft lifetime across route changes, windows, reconnect, and app restart.
- Keyboard shortcuts that complement visible controls without recreating terminal editing.

## Prompts And Attention

- The server exposes typed pending asks and approvals and typed answer operations.
- Transcript events also signal prompt pending/resolved state.
- Desktop task detail already renders questions and runtime approvals.
- Desktop attention notifications already route task-scoped questions, approvals, and error-interrupted runs.
- A session chat needs prompt placement, multi-prompt navigation, cross-client resolution, and non-task-session attention decisions.

## Slash-Command Capability Inventory

The desktop design must assign each capability to a visible control, menu, setting, route, or dialog:

- New/open/back session navigation.
- Login/logout.
- Compaction.
- Session rename.
- Thinking level and fast mode.
- Reviewer/supervisor.
- Auto-compaction and question toggles.
- Status inspection.
- Goal management.
- Process inspection/control.
- Worktree inspection/control.
- Copy latest final answer.
- Review/init embedded prompts.
- File-backed prompts and commands.

Unknown slash input remains a normal model prompt in the TUI; desktop slash parsing is not assumed.

## Rollback And Forking

- Rollback never rewrites the current session transcript.
- The server finds typed rollback candidates through bounded transcript pages.
- Selecting a candidate creates or opens a forked session target.
- The earlier desktop direction proposed a per-message fork affordance; its exact eligibility, confirmation, edit flow, route transition, and lineage presentation remain unresolved.

## Failures And Reconnect

- Desktop keeps cached state visible while disconnected, disables mutations, preserves drafts, and never replays mutations automatically.
- Transcript sequence gaps require authoritative rehydration.
- Session-specific errors need actionable retry, return, or recovery affordances.
- Transient runtime notices, persistent connection state, operation errors, and committed transcript diagnostics must not be collapsed into one generic error mechanism.

## Current Related Work

- KENT-278 is changing session-picker update-status presentation and server ownership.
- KENT-257 proposes replacing the internally constructible transcript discriminant/payload mismatch with typed variants.
- KENT-258 proposes one authoritative transcript hydration snapshot transaction.
- KENT-272 removes client-fabricated transcript rows for queue/steering failures.
- BUI-186 bounds prompt-history storage and reads to 100 entries.
- BUI-60 investigates reducing persisted transcript event size.

Task planning must either depend on these tasks, explicitly tolerate their current contracts, or avoid overlapping their write scope.

The cross-desktop migration from non-Kanban cards to list rows is a separate task and must not be folded into a Sessions or Chat implementation ticket.
