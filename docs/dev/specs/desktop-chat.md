# Desktop Sessions And Chat

## Authority And Scope

- Desktop Sessions and Chat are thin remote-control surfaces. The Kent server is authoritative for Sessions, project and workspace scope, runtime activity, transcript history, drafts, Pending Work, prompts, goals, processes, worktrees, and durable state.
- Desktop provides the session-browser and chat capabilities available in the terminal product unless this specification explicitly excludes a capability.
- Session discovery is always scoped to a selected Project; there is no unbounded all-Project session list.
- All desktop presentations of a Session control the same server-authoritative Session. Opening the same Session in another presentation neither creates separate runtime ownership nor forks client state.
- Task Detail offers `Open Chat` in place of `Open in CLI`. It opens the referenced Session at its latest transcript position.
- Chat can appear as a full-page destination, a separate window, or an adaptive detail presentation. The location of the separate-window action is unspecified.

## Sessions

- A Project's session browser has `Sessions` and `Subagents` categories. Each category uses server-authoritative Infinite Scroll. Changing category requests that category alone, and Desktop never materializes a complete category.
- Sessions are recency ordered. A compact full-width row shows the Session title, first-prompt preview, and recency. There is no search or additional status filter.
- Selecting a Session opens full-page Chat. Secondary actions are in the row menu.
- `New Session` opens an empty Chat using the Project default workspace. `New in workspace` lets the operator choose an attached Project workspace from an infinite-scrolling list.
- New Session creation does not select a worktree. Worktree control is available after Chat opens.
- New Chat has no setup form for name, model, provider, Agent, worktree, or prompt. It starts with an empty composer; rename and settings remain available after Chat opens.
- Opening and abandoning untouched new Chat creates neither a durable Session nor a session-browser row. The first nonblank user message, or another action that enters the agent loop, creates the Session.

## Transcript

- Chat has one infinite-scrolling transcript with a live assistant tail. It does not expose the TUI's Ongoing Mode and Detail Mode split. It shows every committed transcript item except items whose explicit hidden classification requires omission.
- Each non-conversational item has a compact and full presentation. Expansion is local to the visible row and returns to that row type's default after the row leaves and re-enters the viewport. Expanded rows alone show their committed timestamp and actions; live rows never invent a timestamp.
- Expanded flat rows use the transcript width without a nested island. They are borderless and neutral except for the semantic color of their leading status icon. Their header shows icon, localized type, compact summary, and available status or actions. The header toggles expansion without taking row-specific actions.
- Context rows start collapsed: loaded instructions with their source label, skill guidance, environment facts, Subagent context, handoff context, mode instructions and restoration, Workflow execution instructions, active-goal continuation, manual-compaction carryover, compaction summaries, context-pressure reminders, Goal feedback, and worktree changes. Worktree entry shows branch and path; return to the main workspace shows its destination. Full expansion shows the original complete content.
- Assistant commentary is a full assistant message without a phase label.
- Warnings, runtime errors, generic system notices, generic warnings, legacy notices, and reviewer errors start expanded. Runtime diagnostics start collapsed. Empty known developer context is omitted; empty unknown developer context is an expanded Diagnostic row that identifies the unknown source and integrity failure.
- Reasoning summaries and reviewer lifecycle are shown only in Thinking or status, never as transcript rows. Reviewer suggestions create exactly one collapsed Reviewer feedback row; the related model instruction is never another transcript row.
- Pending Questions and Approvals appear only in their prompt controls. A completed Question creates exactly one expanded Ask Question row. Approval has no separate decision-history row: its associated tool item owns the request and outcome.
- A tool-associated prompt has one prompt control and one associated tool item; it is never duplicated as another transcript row.
- Runtime lifecycle, active-work changes, compaction lifecycle, Goal lifecycle, unavailable runtime, accepted or discarded Pending Work, Queue failure, and input-operation reconciliation do not create transcript rows. Their controls or notifications own feedback.
- User interruption creates no transcript row. Runtime error feedback and committed error items remain visible where supplied by the server.
- Background activity and controls appear in Processes, while committed Background process results remain in transcript order. Worktree control outcomes, sleep-guard failures, and prompt-history failures use notifications and create no transcript row.
- Thinking, Context, Goal, and Pending Work are the only persistent live-control areas defined here. Session name stays in application chrome. Transient status-line notices and errors use notifications rather than feature-specific status surfaces.
- Malformed user, assistant, tool, or notice items are contract failures, not display variants: development builds fail immediately; production uses the transcript contract-failure recovery path. Chat never substitutes placeholder content or roles.
- Each committed transcript row has one stable Kent-provided identity across pagination, hydration, and live updates. Desktop does not infer row identity from display text, role, position, or transport order.

## Tools And Processes

- A tool starts as one collapsed activity row with a tool-specific summary. Completion changes that same row to a compact result or error summary; expansion exposes full input, output, and diagnostics.
- Cancelled tools and tools aborted by execution failure leave no committed tool row. Wider failures use the applicable notification or committed error row.
- Generic tools show name and compact input/result. Shell commands show command and running, exit, or background state. Shell input is a separate chronological row. Patch/Edit rows show operation, affected files, and addition/removal counts. Source results expand as selectable, syntax-highlighted source; plain results expand as selectable text. Web Search has its own query-and-sources presentation.
- A backgrounded tool remains a completed Backgrounded tool row. Its later process completion or termination is a separate Background process row at its authoritative chronological position; it never changes the earlier row.
- Expanded context, notice, Reviewer feedback, and Diagnostic rows copy their original full payload. Generic tools copy full input and output; Shell commands copy command and output; Shell input copies returned terminal output only; Patch/Edit copies only the patch or diff; completed Questions copy question, selected answer text, and commentary. Web Search has no copy action but retains ordinary selection and safe source links.
- Combined copied tool and Question sections preserve display order, separated by blank lines, without headings.
- Processes is a single dense list with no detail view, inline output insertion, or log view. It preserves server ordering and shows state, process identifier, age, working-directory basename, one-line command, and latest nonempty output line.
- Process termination needs no confirmation. It disables repeated activation and shows stopping while pending. Only killable processes show the accessible terminate action. Processes refreshes automatically; stale rows remain visible if refresh fails, with notification feedback. Loading, failure with Retry, and empty states use the standard compact list states.
- The terminate action is always visible for a killable process and absent for every other process.
- A process row does not offer an action that opens it from a transcript row.

## Transcript History And Live Output

- Transcript history uses server-authoritative infinite scroll. The desktop never loads or retains the complete transcript.
- The visible transcript retains the newest segment and at most one adjacent segment. Loading an adjacent segment occurs at that transcript edge without replacing visible content or the viewport. Failure replaces only that boundary row with an actionable Retry for the same opaque cursor. At the oldest edge, no start-of-session marker is shown.
- Every Chat entry opens at the newest transcript content and composer, never at a specific message, Question, Approval, tool, or workflow event. Reopening a Chat begins at the newest content; Chat has no durable read state or saved scroll position.
- When the operator has scrolled away from the tail, incoming content preserves the viewport. A `Jump to latest` control appears when a newer server segment exists or when the viewport is more than 80px from the end of the loaded newest segment. The control has no unseen count. Activating it fetches the newest segment in one request when necessary and never walks intermediate segments.
- `Jump to latest` is a 40px circular glass control with a 24px downward arrow and 12px end and bottom spacing above the composer. It uses scale motion when it appears or disappears; reduced motion changes visibility immediately.
- At the tail, new assistant content, committed rows, and composer-height changes keep the view at the tail. Reduced motion removes movement animation.
- User, committed assistant, and live assistant content use the same safe Markdown presentation. Raw HTML, unsafe links, diagrams, math, and built-in Markdown controls are unavailable. Incomplete Markdown remains safe and syntax highlighting waits for an incomplete code fence to close.
- A live assistant message appears only after its first nonempty content arrives. It presents live text with the approved caret and text motion; completed messages use static presentation. Reduced motion shows characters immediately and omits the caret.
- A live Chat opened mid-response may animate already received eligible text once, then only newly arriving text. Each assistant update immediately changes the one visible live message; Chat does not alter, repair, or buffer model text.
- Live Markdown is available only when it meets the approved safety, scrolling, responsiveness, and product-review requirements for long output, incomplete Markdown, tables and lists, hostile links and HTML, and unfinished code. If it does not, streaming uses selectable plain text and the completed message uses the shared static Markdown presentation.

## Chat Presentation

- Chat is edge-to-edge. Existing Session hydration shows a compact centered loading state while retaining application chrome; transcript and composer appear together only after authoritative hydration. On initial failure, the same position shows an error with Retry and Back remains available. New Chat does not show this loading state.
- Application chrome owns Session title and Back. Chat has neither an in-content title nor a second Back action.
- Transcript content is at most 1200px wide. User and assistant messages are content-sized up to 1000px, with normal wrapping. User messages align right and assistant messages align left; there are no avatars or role labels.
- User messages longer than 10 rendered lines begin collapsed to 10 lines with a fade and accessible Expand action. Expansion is one-way until the row leaves the viewport. Assistant messages stay expanded.
- Each committed message has an always-visible footer aligned and width-matched with the message. Footer actions are icon-only controls with accessible names and explanatory hover or focus text. User messages offer Copy and Edit; assistant messages offer Copy. Copy uses the original Markdown source. Editing is the only fork action: submitting an edit creates a new Session branch and leaves the original Session unchanged.
- A live assistant message has no footer. The committed message receives timestamp and Copy together when it resolves.
- Committed-message time uses the server's durable timestamp: relative age for 24 hours, then localized compact date and time, including the year only when different from the current year. Full date, time, and time zone are available on focus or hover.
- Consecutive user or assistant messages use tighter spacing and a compact adjacent corner on their matching side. Messages remain separate islands.
- Tools, diagnostics, context, and notices use flat transcript rows rather than message islands. Transcript content opens no duplicate detail surfaces; its full content is available through expansion.
- Contextual destinations adapt between shifted and overlay presentation. Processes is the only defined Chat destination. Session settings use a non-modal popover under the composer, not a contextual destination.

## Session Settings And Drafts

- The settings trigger shows Agent and Thinking, plus `Fast` when enabled. When Thinking is unsupported, it shows Agent and optional `Fast`.
- The settings trigger is a rounded control below the composer. It opens an anchored non-modal popover.
- Settings appear in this order: Agent, Supervisor, Thinking when supported, Fast mode when supported, Questions, Auto-compaction; then session facts. The facts appear as `To parent chat`, Task navigation, and Session ID when present.
- `To parent chat` opens the preceding Session at its newest content. A workflow-linked Chat shows Task short ID and title that open Task Detail. Parent-agent lineage, Workflow identifiers, workspace, worktree, branch, compaction mode, and compaction count are not ordinary Chat facts.
- Session ID is shown as a shortened copyable value; copying uses the complete identifier and focus or hover reveals it.
- Agent selects the Session role. It is locked after the first model request and is locked from the start for a workflow Session. The locked explanation is `Locked by caching policy`.
- Agent choices show role, effective model, and Thinking effort, without role descriptions or provider. Changing Agent preserves compatible explicit settings and resets incompatible settings to that role's authoritative baseline without confirmation or notification.
- Supervisor has exactly `Off`, `After edits`, and `Always`, with descriptions `No automatic review`, `Review turns that changed files`, and `Review every completed turn`.
- Thinking follows the server-provided ordered values. Enumerated values use a stepped control and display the exact chosen value; Low is gray, Medium and High use the primary tone, and Xhigh, Max, and Ultra use the secondary tone. Unsupported Thinking is omitted. A supported non-enumerated value uses an input and Apply action. Rejected input remains entered and shows notification feedback.
- Fast mode is omitted when unsupported. Questions remains visible, off, and unavailable with `Unavailable for this Agent` when the selected Agent lacks that capability. Required Auto-compaction remains on and unavailable; Manual-only compaction shows its stored value but is unavailable because it cannot run automatically.
- Before a new Session's first prompt, all settings and message text are one draft. The first prompt creates the Session and applies the complete draft atomically. After that, Agent is locked and other changes apply through normal runtime controls.
- Requested setting values appear immediately and reject repeat activation while pending. On failure, the control returns to the latest server value and shows notification feedback. Other non-Agent settings remain available while work runs and affect only later applicable work, never work already in flight.
- Sending remains available during a pending setting change. Server operation order determines whether it observes the old or requested value.
- The unsent message and complete settings draft persist and restore together across navigation, detachment, relaunch, and server restart. The server owns this one draft; there is no collaborative composer editing.
- Setting changes create no transcript rows. Clicking away or Escape closes settings.
- Choosing Agent or Supervisor keeps settings open.

## Composer And Pending Work

- Send starts a user turn while idle and Steers the active turn while work is running. A Steer takes effect at the next safe step boundary. Queue is a separate action that starts after active work completes; when idle, queued work starts immediately.
- `Enter` sends or Steers, `Ctrl+Enter` Queues, and `Shift+Enter` inserts a newline. Tab keeps normal focus navigation except that it accepts an active workspace-path suggestion. Stop has no shortcut.
- Queue has no visible button. While work is running and an empty composer can queue work, its placeholder is `Ctrl+Enter to queue`.
- While work runs, an icon-only Stop action is visible beside Send/Steer. Stop clears all Queue and Steer items.
- The composer grows to one-third of available Chat height, then scrolls internally. Up and Down recall prompt history only at whole-buffer boundaries; returning below the newest history item restores the pre-history draft, and editing recalled text detaches it from history.
- `@` path suggestions search the Session effective working directory. They include files, derived directories, and hidden paths; exclude `.git`; preserve server fuzzy order; and never scan the desktop filesystem or transfer the whole repository.
- At most seven path suggestions are visible. Up and Down select them; Enter, Tab, or pointer activation inserts the exact `@`-prefixed repository-relative path, with `/` for a directory, without sending. Escape hides suggestions until the query changes.
- Pending Work appears behind the composer's top edge only while Queue or Steer items remain. It is an unlabeled, scrollable sheet no taller than about five two-line items, with Queue items first in first-in-first-out order, then Steer items in first-in-first-out order. Each item shows no more than two lines and has an accessible Discard action.
- Pending Work has no edit, reorder, submit-now, clear-all, full-text preview, or secondary detail view. On successful discard, text returns only to the initiating client's composer: verbatim into an empty composer, otherwise after one newline. Failed discard leaves both item and composer unchanged and shows notification feedback.
- The Pending Work sheet preserves its position while inspecting older items and follows additions only at its newest edge. Stop restores cleared items only to the initiating composer, in displayed order separated by one newline; other clients see only their removal.

## Desktop Exceptions

- Desktop replaces terminal interaction mechanics with desktop controls without removing the capability they exposed.
- Desktop Chat has no file or image upload, drag-and-drop attachment, clipboard-image attachment, attachment chip, or transcript attachment. `@` workspace references are paths, not attachments.
