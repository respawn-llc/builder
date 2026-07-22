# Desktop Sessions And Chat Question Map

Questions are resolved in dependency order. Later branches should not be specified while an owning earlier decision is open.

## 1. Product Boundary

- The initiative specifies the complete desktop implementation with 100% TUI capability parity; there is no MVP or v1 subset.
- Session Browser, Chat, and capability sub-surfaces may be separate implementation tasks, but none are optional parity backlog.
- Home Sessions and Task Detail both enter the same chat destination.
- Which recovered July 5 decisions carry forward unchanged?
- Is development-only gating retained, and what event removes the gate?

## 2. Session Information Architecture

- Sessions are scoped through a selected project; there is no all-project session feed.
- Sessions and Subagents use category filter chips over one project-scoped virtualized list.
- The browser uses dense full-width rows with title, first-prompt preview, and recency.
- The browser stays recency ordered and adds no search or additional status filters.
- Row click opens full-page chat and secondary actions use a context menu. Pop-out trigger placement is unresolved.
- Which session classes are visible?
- Primary New Session uses the project default workspace and opens an empty new-chat destination.
- New in workspace opens a virtualized cursor-paginated workspace-picker sidebar; worktrees remain post-open.
- New Session has no setup/name/model/provider/role/worktree fields.
- Creation is lazy: abandoning untouched new chat leaves no durable session; the first agentic trigger materializes it and replaces the route.
- How are unavailable or detached targets presented?

## 3. Home And Navigation

- Home uses a compact left navigation island and a remaining-space content area.
- A typed pinned Inbox destination sits above the project list and is the Home fallback.
- Selecting Inbox shows aggregate attention. Its empty state is a focused `All caught up` state.
- The base pinned Inbox row has no required badge. An exact nonzero global unresolved count is an optional standalone feature/task backed by an authoritative server aggregate.
- Home keeps the existing auto-fit responsive behavior: side-by-side when both panes fit and stacked when they do not. The navigation pane is not resizable or drawer-based and gains a semantic maximum width in the side-by-side layout.
- Selecting a project shows a project workspace with Workflows and Sessions tabs.
- One window-local last-used Workflows/Sessions tab choice follows navigation between projects; relaunch falls back to Workflows.
- The project workspace has a compact sticky header with project identity, Workflows/Sessions control, and contextual Link workflow or New Session action.
- Project navigation uses compact list rows; title and path each allow two lines, and selection elevates the row by one island level.
- The project Workflows tab contains project-workflow links; sessions are a parallel project-owned collection. Selecting either row enters its full-screen destination.
- Project-workflow links use cursor-paginated compact rows with default and validation facts.
- Workflow definitions leave primary navigation. The reusable Workflow Library remains a secondary `Manage workflows` destination from the Link workflow flow.
- Link workflow opens the existing overlay with Create workflow and Manage workflows header actions.
- Chat supports a standalone route, native pop-out, adaptive master-detail, and sidebar/overlay hosting.
- Task Detail's `Open Chat` navigates to the standalone route.
- Inbox items continue opening Task Detail in the existing overlay sidebar.
- What route and scroll/composer state restore after relaunch?
- Standalone route and native pop-out are the foundational hosting surfaces.

## 4. Chat Surface Layout

- Chat is edge-to-edge with no enclosing level-0 island.
- The chat-area max width is 1200px and user/assistant conversational-island max width is 1000px.
- Conversational messages reuse ordinary shared islands; no bespoke bubble component exists.
- User islands are right-aligned and assistant islands left-aligned; both use the same ordinary island level and intrinsic content sizing with no manual text measurement.
- Conversational islands have no avatar or role label; tools and diagnostics use flat rows.
- User messages exceeding 10 rendered lines start collapsed. They copy Task Detail's in-island bottom gradient and centered icon-only `ChevronDown` Expand control.
- Expansion is one-way and reveals the complete message; user messages have no Collapse action.
- Expansion is row-local presentation state and resets whenever virtualization unmounts the row. Chat has no separate stable-ID expansion registry or persisted expansion marker.
- Assistant messages remain fully expanded and have no collapse action.
- Each conversational island has an external footer matching its measured width and role edge. The muted timestamp and all footer actions are always visible.
- User-message footers contain Copy and one Edit action. Edit forks on submission without rewriting the original session; there is no separate Fork action.
- Assistant-message footers contain Copy only.
- Message Copy writes the original Markdown source to the clipboard.
- Footer actions are always-visible icon-only Lucide buttons with accessible names and hover/focus tooltips.
- Each message island and footer share the wider intrinsic group width. Short messages expand to the footer's minimum width, and the group remains capped at 1000px.
- Committed-message timestamps use the server-assigned durable transcript-event timestamp. Live and hydrated projections expose the same instant; client clocks are not authoritative.
- Message timestamps display relative age for messages less than 24 hours old, then a compact localized date and time with the year only when it differs from the current calendar year. Hover/focus exposes the full absolute timestamp and time zone.
- A provisional streaming assistant island has no footer or reserved placeholder. The committed row resolving the same stream identity adds timestamp and Copy together.
- Same-role islands use smaller vertical spacing than role changes.
- Conversational islands copy Respawn's full role-edge corner grouping geometry.
- Chrome owns session title and Back; chat has no in-content title header.
- Chat uses the existing global sidebar host for explicitly designed capability destinations: `shift` on wide windows and `overlay` on compact windows.
- Chat adds no embedded host, Chat-specific sidebar runtime, or permanent `custom` destination.
- Chat has one input-field island containing composer/status elements.
- Pop-out action placement is unresolved.
- Every input/status element must be designed with its own question; no aggregate composition is assumed.
- Sidebar destination contents, entry actions, placement, sizing, and adaptive behavior.
- Transcript, pending-work area, prompts, composer, and secondary controls.
- Adaptive behavior from narrow vertical windows to ultrawide.
- Island hierarchy and maximum nesting.
- Full-screen loading, reconnect, error, empty, dormant, and unavailable states.
- Existing-session hydration keeps chrome visible and shows the existing UI-kit Loading experience as a compact intrinsic island centered in the available transcript viewport, never a full-surface state or skeleton. Transcript and composer appear atomically; the composer is absent before hydration.
- Initial hydration failure replaces it with the matching compact centered UI-kit Error experience and primary Retry. Chrome Back remains available; the composer stays absent.
- Placement of session identity, project/task lineage, model, runtime activity, context, compaction, and goal, one element at a time.

## 5. Transcript Semantics

- A chat navigation-destination instance owns transcript position only for its own lifecycle.
- A new destination without an explicit target anchors at the newest row/composer. Destination disposal discards position, so reopening starts at the default latest anchor.
- Chat has no global/window-wide session-position registry, cross-destination restoration map, or persisted marker. Position does not define read/unread state.
- New committed rows and assistant deltas preserve an away-from-tail viewport.
- Server opaque cursors remain pagination authority. A deep transcript-list module delegates bidirectional page state/retention to TanStack Query and all virtualization, anchoring, following, and end-scroll behavior to TanStack Virtual's official chat APIs.
- JSONL-backed server pages remain compaction-bounded segments. Roughly 100-row pages are excluded from the JSONL design and belong to the SQLite transcript read model; no side index, projector checkpoint, cumulative offset, or persisted page marker is introduced.
- TanStack Query retains exactly two segments with `maxPages: 2`, matching the TUI's current-plus-one-adjacent bounded window. Query owns far-segment eviction.
- Adjacent-segment loading/error uses the existing UI-kit infinite-list boundary row. Loading is compact; failure is inline and actionable with Retry for the same cursor; loaded content remains usable.
- The absolute oldest transcript edge has no terminal marker or dated divider; the boundary slot disappears.
- Feature code performs no manual anchor capture, offset/index compensation, `scrollTop` mutation, scroll registry, or custom virtual-range rule.
- Jump to latest copies Respawn's visual treatment: a 40px circular glass control with a 24px `ArrowDown`, bottom-end aligned 12px above/inset from the composer, and scale animated.
- Visibility is the inverse of TanStack Virtual `isAtEnd()` with one semantic 80px `scrollEndThreshold`; the previous Respawn two-item condition is superseded by native TanStack ownership.
- Global tail is derived from both native facts: no newer Query page and Virtual `isAtEnd()`. A local historical-window end never hides Jump to latest.
- Jump to latest has no visible label or count, has the accessible name Jump to latest, animates to the tail, and honors reduced motion.
- When the newest segment is evicted, Jump to latest resets the infinite query to its cursorless newest initial page in one request, then calls Virtual `scrollToEnd()`; it never fetches every intermediate newer segment.
- One native `isAtEnd()` result owns control visibility and following. Hidden means tail-follow is active; shown means incoming growth preserves the viewport. Clicking the control or manually returning resumes following.
- Committed rows use a typed server locator `(durable session-event sequence, event-local projected-row ordinal)`, scoped by session and identical across page, hydration, and live projections. It adds no JSONL marker or client registry.
- Every Chat entry point opens the latest transcript row and composer. There are no message-specific deep links, open-at-message instructions, target-page resolvers, target highlights, or transcript-target route/search params; attention and task entry points do not jump to particular rows.
- Stable prepend and streaming growth behavior.
- Per-entry collapsed/full defaults by typed row kind.
- Transcript visibility is detail-mode-like: every committed item remains present unless its exact type is explicitly classified as bloat. TUI ongoing/collapsed/detail-only classifications map to desktop collapse defaults rather than visibility.
- Audit every active committed item type independently for transcript presence, collapsed representation, expanded representation, and default collapse state.
- Provider-supplied reasoning summaries are hidden from the transcript and appear only in the separately designed Thinking loader island/chip.
- Developer/context/diagnostic entries remain to be audited per exact type; no broad category may be hidden as a shortcut.
- `AGENTS.md`, skill guidance, and environment facts are visible flat transcript rows. Each starts collapsed; expansion reveals the full Markdown or structured facts/text. The `AGENTS.md` collapsed row uses its source path or compact label.
- Assistant commentary is an ordinary fully expanded assistant island with no phase label; Copy and authoritative timestamp appear when durable.
- Subagent context, future-agent handoff context, and headless-mode instructions are visible flat rows that start collapsed and expand to their full content.
- Interactive-mode restoration, workflow instructions, and active-goal continuation are visible flat rows that start collapsed and expand to their full details/content.
- Manual-compaction carryover remains visible for now as a collapsed flat row that expands to the preserved user message.
- Context compaction summaries and compaction-soon reminders are visible flat rows that start collapsed and expand to their full summary/instruction.
- Prompt-cache continuity warnings are visible flat warning rows expanded by default with full structured diagnostics and an available compact form.
- User interruption feedback is hidden from the transcript because the initiating action already communicates the event.
- Runtime/developer error feedback is a visible flat error row expanded by default with full diagnostics and an available compact form.
- Goal feedback is a visible flat row that starts collapsed and expands to full feedback.
- Worktree-enter and main-workspace-return rows are visible flat rows that start collapsed with destination summaries and expand to full workspace/effective-directory details.
- Background-process notices are visible flat tool-family rows in authoritative transcript order, collapsed by default and expandable to full process facts.
- Generic system, generic warning, and legacy untyped notices are visible flat rows expanded by default with full text and available compact forms.
- Runtime diagnostic notices are visible flat rows that start collapsed and expand to full structured diagnostics.
- Reviewer running/completed lifecycle stays out of the transcript and appears only in the later-designed Thinking/status island or chip.
- Desktop has one substantive Reviewer feedback row type, collapsed by default and expandable to full Markdown. Existing feedback/suggestions backend labels require root-cause normalization, not separate UI variants.
- Nonempty reviewer suggestions always persist/project that one structured Reviewer feedback row, independent of legacy `Reviewer.VerboseOutput`. The derived model-facing control instruction is internal and never a second client row.
- Reviewer failures are visible flat error rows expanded by default with full diagnostics and an available compact form.
- Pending question and approval controls live only in the dedicated prompt surface attached to the composer/status island; neither creates a pending transcript row.
- Answered questions produce one completed Ask Question transcript row expanded by default.
- Approvals create no separate decision-history row because the associated tool call owns the approval request/outcome.
- Tool-associated prompts use one control surface and one associated tool transcript item without duplicate prompt rows.
- Desktop reuses the TUI's existing tool start/end lifecycle keyed by `ToolCallID`; those facts directly produce one row without a second reconciliation subsystem or server-tool refactor.
- Provider/storage formats, materialized/synthesized provenance, repair paths, and deduplication are internal artifacts rather than UI row types. Their separate legacy/dead-code audit is tracked outside this initiative in `KENT-303`.
- Started and hydrated in-flight tools appear immediately as one collapsed flat row with tool-specific compact input and an activity indicator.
- Successful completion updates the same row, stops activity, and leaves it collapsed with an input/result summary. Completed failures, including nonzero shell exits, also remain collapsed with an error summary; both expand to full details.
- Canceled and failed-abort tools follow the TUI behavior: remove the pending row and create no committed tool row. Sonner and any authoritative durable error-feedback row own wider failed-abort information.
- Generic/unknown tools start collapsed with name and compact input/result summary; Shell tools start collapsed with command and lifecycle status. Both expand to full structured/selectable details.
- `write_stdin` is a separate chronological Shell input row, collapsed with target/input summary and expandable to full sent input/result.
- Patch/Edit follows the TUI: collapsed operation/file/count summary, expandable structured diff/result.
- Source result mode expands to inline syntax-highlighted selectable source using typed path/language facts; plain result mode expands to full selectable plain text.
- Web Search is a distinct typed row, collapsed by default with query/result summary and expandable full sources/results; no client tool-name string check is allowed.
- Background execution copies TUI: the original tool completes as Backgrounded; a separate collapsed process row appears later in authoritative chronology for completion/kill and links by typed process identity without mutating the original row.
- Accepted queued/steered work lives only in the Pending Work surface inside composer/status. Submission hands history to the authoritative user-message row; discard and typed queue failures create no transcript marker.
- Normal runtime lifecycle and active-work kind live only in the later-designed runtime/Thinking/status surface. Runtime unavailable uses the runtime/connection error surface. None creates transcript rows.
- Step/run lifecycle stays in runtime/status; actual user/assistant/tool/error-feedback rows own durable history rather than lifecycle markers.
- Compaction lifecycle is Thinking/status/Context-only; goal lifecycle is Goal/status-only. Neither creates lifecycle transcript rows, while committed compaction/goal rows own history.
- Live background activity and process controls belong to a new dedicated contextual-sidebar destination with no duplicate live transcript rows.
- Processes is one typed list-only sidebar destination with no detail destination or nested navigation. Desktop presentation copies `/ps`; server scope/order/retention redesign is out of scope and remains owned by its existing ticket.
- Process rows copy TUI fields/order, are dense and non-expandable, and expose direct actions only.
- Desktop intentionally has no `/ps inline`/Insert output equivalent.
- Desktop intentionally has no `/ps logs`/Open log equivalent.
- Kill has no confirmation, shows a pending stopping state, prevents duplicate activation, and uses Sonner. Killable rows show an always-visible trailing icon-only tooltip button; terminal/non-killable rows omit it.
- Refresh is automatic with no manual action; failures preserve stale rows and use Sonner. Initial loading/error/Retry/empty literally reuse the existing Desktop sidebar-list state patterns.
- Context usage appears only in the selected AI Elements Context control/counter below the input field; exact design is deferred.
- Session name is chrome-owned. Previous-session lineage uses the first session-facts action, `To parent chat`; parent-agent lineage is omitted from ordinary Chat and reserved for Subagent UX.
- Raw workflow active/run/task/workflow IDs are internal behavior-gating facts, not TUI-visible Chat status. Workflow-linked Chat shows one typed Task short-ID/title row opening Task Detail and omits raw Run/Workflow IDs.
- Transient TUI status-line notices/errors map to Sonner; no per-feature error surfaces are invented.
- Worktree-transition outcomes create no transcript row; initiating controls update and transient feedback uses Sonner.
- Sleep-guard and prompt-history persistence failures are Sonner-only. Input reconciliation enum states appear nowhere.
- Malformed row integrity branches are impossible contract violations, not UI item types. No fallback/placeholder rows are designed; debug fails fast and release contract-failure recovery remains to be designed.
- Empty known developer context creates no row. Empty unknown developer content creates one expanded Diagnostic row with type/source metadata. Legacy explicit Hidden entries remain omitted.
- Expandable flat rows use a full-width semantic disclosure header with localized label/summary, independent trailing actions/status, and right/down chevron. Header space toggles reversibly.
- Flat-row expansion is row-local and resets to the audited type default after virtualizer unmount. No expansion registry or persistence exists.
- Flat-row committed timestamps appear only while expanded. Disclosure uses shared vertical motion, switching instantly under reduced motion.
- Flat rows are transparent with no border/card/separators and use only a subtle hover/focus wash. Actions appear only while expanded.
- Semantic state changes only the leading icon color, matching TUI; all other row chrome/content stays neutral.
- Expanded bodies use the full 1200px transcript-row width without a nested island.
- Expanded context/notice/Reviewer/Diagnostic rows copy original full source/text. Generic and Shell-family tools use one combined Copy all action.
- Patch/Edit copies only the patch/diff, never its result. Completed Ask Question copies question, selected answer text rather than index, and optional user commentary.
- `write_stdin` is the Shell-family exception: Copy includes returned output only, never sent characters or call metadata.
- Web Search has no Copy action. Expanded-row buttons are icon-only Lucide tooltip buttons with accessible names, and Copy success uses Sonner.
- Backgrounded Shell and later Background process rows expand to their exact full model-visible result/notice payloads. Desktop does not copy the TUI no-op expansion bug.
- Neither background row has a row-local Open process action; the separate process sidebar destination owns its own entry point.
- Combined Copy payloads concatenate audited raw sections in display order with blank-line separators and no headings.
- Generic tool results, diffs, source snapshots, answered questions, injected context, Reviewer feedback, and summaries have no duplicate sidebar destinations; inline expansion owns their full content.
- Session settings/details are not a sidebar destination. They use a rounded under-composer trigger and an upward-opening non-modal popover that copies the approved reference's anchored material/dropdown treatment. Agent and Supervisor open adjacent option dropdowns; Thinking and the remaining settings stay inline. Kent has no Advanced disclosure or grouped settings taxonomy.
- Tool input, output, error, background, and process presentation.
- One shared UI-kit Markdown renderer owns provisional and committed assistant text.
- Live Markdown is a hard-gated Streamdown integration using streaming/static modes with Kent sanitization, links, components, typography, and selectable text. Streamdown controls, Mermaid, math, and raw HTML are disabled; incomplete-fence highlighting is deferred.
- Active provisional text uses Streamdown's static built-in block/line caret plus character-separated `blurIn` animation with library-default timing/easing and ordinary code/preformatted-content exclusions. Committed text is static. Kent adds no tokenizer, animation engine, custom caret, caret keyframes, blink override, or parallel animation state.
- Under `prefers-reduced-motion`, provisional characters appear immediately and the inline caret is absent. Styling owns the adaptation without JavaScript preference or transcript state.
- The provisional assistant island appears only on the first nonempty content delta. Pre-first-token activity belongs to the runtime-status area; chat creates no empty caret-only or blank conversational row.
- Every received assistant delta updates the one authoritative provisional-row value immediately. There is no app-authored frame scheduler, timer throttle, coalescing buffer, or second rendered-text state; ordinary React/renderer batching is sufficient unless manual product review rejects Streamdown.
- Attaching to an already-running stream accepts Streamdown's one-time animation of all eligible accumulated provisional text on initial mount; subsequent updates animate only newly appended characters. Mid-stream hydration joins the manual gate, with no static-prefix state, split renderer, or custom pipeline.
- The gate is below 1,000 net production lines and includes actual-cadence 50k-character, malformed Markdown, GFM, hostile-content, and 500-line unfinished-fence fixtures. The user makes the final performance verdict through manual product review; no fixed benchmark hardware, numeric timing budget, or mandatory visible-render cadence substitutes for that verdict. Failure selects plain streaming text followed by shared static Markdown on commit.
- Diffs, links, copying, selection, and accessibility.
- Grouping and spacing of user, assistant, tool, prompt, notice, and reviewer entries.
- Timestamp policy.

## AI Elements Baselines

- Selectively reuse Chain of Thought, Context, Model Selector adapted as Agent Role Selector, and Reasoning.
- Keep the selected source close to pinned upstream registry output and deliberately re-sync it; Kent's shared UI kit owns the sole local source and typed Kent adapters.
- All other AI Elements components are out of scope/not fitting unless explicitly reopened.
- Speech Input is future Voice Mode work and creates no Sessions/Chat behavior or task.
- Audit the four active candidates independently; adopting their upstream state/data ownership is not implied.
- Attachments/client uploads are outside this initiative and create no task. Workspace `@` references remain independent.

## 6. Composer

- Idle Send starts a user turn. Busy Send steers the active turn at the next safe boundary. Queue remains a separate explicit action for a later turn.
- `Enter` sends, `Ctrl+Enter` queues, and `Shift+Enter` inserts a newline. `Tab` remains focus navigation.
- Active `@` autocomplete is the sole Tab exception: Tab or Enter accepts the visible path; otherwise Tab navigates focus.
- Queue has no visible button. The busy empty-input placeholder says `Ctrl+Enter to queue`.
- Idle `Ctrl+Enter` queues and starts immediately because no active turn precedes it.
- Active work shows a separate icon-only Stop button while Send continues to steer.
- Stop has no keyboard shortcut.
- The multiline input grows up to one-third of available Chat height, then scrolls internally.
- Prompt history matches the TUI boundary behavior: Up/Down recalls only at whole-buffer edges, restores the pre-navigation draft below newest, and detaches a recalled entry when edited.
- Workspace `@` autocomplete is a derived picker above the composer. It matches TUI scope/matching/insertion, shows at most seven file/folder rows, and uses a server-owned bounded search over the effective working directory.
- Idle placeholder copy remains unresolved for visual product review.
- Visible buttons and remaining keyboard shortcuts.
- Multiline submission shortcut.
- Draft persistence boundaries.
- One server-owned text/settings draft aggregate; no GUI-local/per-window store and no live collaborative synchronization.
- Prompt history.
- File/path references.
- No client-side repository scan or full-corpus transfer.
- Ordinary clipboard text paste. Client image/file attachments are explicitly outside this initiative.
- Disabled/read-only/unavailable states.

## 7. Pending Work

- Steer and Queue are distinct typed user intents.
- Pending Work is a peeking sheet stacked behind the top edge of the composer. A semantic max-height sized for approximately five two-line rows caps it and overflow scrolls.
- The sheet has no header/title/count.
- One unlabeled list orders Queue FIFO above Steer FIFO and uses different leading icons.
- Rows show at most two lines and ellipsize, with no full-text tooltip/expansion/detail.
- Each item has a compact trailing `×` named `Discard`; successful authoritative removal restores original text only to the initiating input, separated from existing text by one newline.
- No edit, reorder, submit-now, or clear-all actions.
- Stop clears Queue and Steer pending work and restores all text only to the initiating input in displayed order, newline-separated.
- Concurrency behavior when another client changes pending work.

## 8. Questions And Approvals

- Inline placement versus dedicated prompt tray.
- Multiple-prompt navigation.
- Whether answered prompts remain in the transcript or collapse.
- Commentary and denial behavior.
- Cross-client resolution.
- Ordinary-session attention and notifications.

## 9. Session Controls And Slash Replacements

- Session create, open, back, rename, and copy.
- Session settings use an under-composer upward-opening popover. Its closed trigger summarizes Agent role and Thinking and appends `Fast` only while Fast mode is enabled.
- When Thinking is unsupported, the trigger shows Agent role plus optional `Fast`.
- Settings order is Agent, Supervisor, Thinking, Fast mode when supported, Questions, and Auto-compaction.
- All controls form one settings group with no subgroup separator, heading, inset section, or extra inter-group spacing.
- Agent and Supervisor are the first two dropdowns. Agent locks after the first model request; workflow Agent is locked from open. Both use the `Locked by caching policy` tooltip.
- Before a new lazy session's first prompt, all six settings are draft state rather than standalone requests. First Send atomically launches with the complete settings draft and user message; partial application creates no session and sends no prompt. Agent then locks at first model dispatch, while later non-Agent changes use runtime controls.
- Agent options use role name plus effective model and Thinking effort; configured role descriptions are omitted. A role change preserves supported explicit per-session choices and resets only incompatible choices.
- One compact muted line outside and directly below Agent shows `effective model • Thinking effort` plus an icon-only lock with `Locked by caching policy` tooltip when locked; provider is omitted.
- Incompatible choices reset silently by updating their controls; no confirmation, Sonner, or inline warning is added.
- Supervisor atomically selects Off, After edits, or Always through one typed server operation shared by TUI and Desktop.
- Supervisor descriptions are `No automatic review`, `Review turns that changed files`, and `Review every completed turn`.
- Thinking uses the server's supported modes in server order. Enumerated modes render as an unlabeled Radix stepped slider with the exact value on the right; no enumerated modes renders a single-line input with a trailing Apply button.
- Desktop adds no synthetic Disabled/None Thinking option.
- Unsupported Thinking is omitted; the text-input fallback applies only when Thinking is supported without enumerated modes.
- Rejected fallback Thinking input remains in the field and uses Sonner without inline validation.
- Known Thinking tones are Low gray, Medium/High primary, and Xhigh/Max/Ultra secondary, with animated active-color transitions.
- Thinking previews locally while dragging or stepping by keyboard and sends one change on interaction commit.
- Fast mode, Questions, and Auto-compaction are toggles. Unsupported Fast mode is omitted. Workflow-required Auto-compaction is visible, on, disabled, and explained by tooltip.
- Questions stays visible, off, and disabled with `Unavailable for this Agent` when the selected Agent lacks that capability.
- Under Manual-only compaction, Auto-compaction remains visible and disabled while truthfully displaying the server's stored on/off value; its tooltip explains that the mode prevents automatic compaction.
- Workspace/worktree/branch and compaction mode/count are omitted from this popover.
- A subtle divider after Auto-compaction separates settings from session facts/actions.
- Session facts/actions are ordered `To parent chat`, Task, Session ID.
- Session ID shows a shortened monospace prefix, copies the full ID, and exposes the full ID on hover/focus.
- Subscription/auth is omitted in favor of a separately designed small icon.
- Workflow-linked Chat shows a Task short-ID/title navigation row and omits raw Run/Workflow IDs.
- Previous session appears as the first session-facts action, with left-arrow icon and label `To parent chat`; parent-agent session is omitted from ordinary Chat.
- Changes preserve existing server runtime-control and persistence semantics with minimal RPC expansion and no client-owned settings model.
- Successful changes create no transcript row. Controls update immediately, reject repeat input while pending, remain changed on success, and restore authoritative state with Sonner on failure.
- Pending controls use disabled styling only.
- Non-Agent settings remain mutable during active work and affect only the next applicable operation, never work already in flight.
- Send remains enabled during pending setting changes. Desktop adds no local ordering barrier; server operation order decides which value a concurrent submission observes.
- The unsent message and complete settings draft persist as one aggregate across navigation, detach, and app/server restart.
- Kent has no Advanced disclosure or advanced/non-advanced settings grouping.
- Before first Send, every setting updates the lazy draft immediately. After launch, Supervisor applies immediately and Agent is locked. Agent/Supervisor selection never closes either level.
- Compaction action and status.
- Goal management.
- Process inspection and control.
- Worktree inspection and control.
- Review/init/file-backed prompt entry points.
- Login/logout and account state.

## 10. Forking And Lineage

- Eligibility rules for user-message Edit.
- Edit-and-fork composer behavior.
- Confirmation and naming.
- Destination transition.
- Parent/previous-session navigation and lineage presentation.

## 11. Failure And Recovery

- Disconnect presentation and action blocking.
- Transcript sequence-gap recovery.
- Runtime activation/release failure.
- Draft persistence failure.
- Page-load failure at either history edge.
- Prompt-answer races.
- Queue/steer/interrupt operation failures.
- Workspace-target mismatch and unavailable worktrees.
- Debug fail-fast versus release recovery.

## 12. Accessibility And Input

- Transcript semantics and live-region policy.
- Screen-reader behavior during streaming.
- Focus restoration across prompts, sheets, and route changes.
- Keyboard navigation without hiding primary actions.
- Reduced motion.
- Copy/select behavior.
- High zoom and narrow layouts.

## 13. Delivery And Task Graph

- Contract prerequisites.
- UI-kit primitives.
- Navigation and route substrate.
- Session browser.
- Transcript adapter/store.
- Transcript rendering.
- Composer and pending work.
- Prompts and attention.
- Capability sub-surfaces.
- Browser QA and native QA.
- Protocol/version and migration effects.
- Dependencies on in-flight Kent tasks.
- One compile-time gate excludes the complete initiative from production until full parity acceptance passes.
- Migrating non-Kanban desktop collections from cards to list rows is a separate task, not Sessions/Chat ticket scope.
