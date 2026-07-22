# Desktop GUI Spec

## Scope And Authority

- Desktop GUI is a remote-control client over an already-running Kent server.
- Server remains authoritative for projects, workspaces, workflows, tasks, runtime, Workflow Execution live state, validation, approvals, questions, comments, worktrees, persistence, and subscriptions.
- The Tauri app never bundles or starts the Kent server binary as a sidecar.
- Long-term GUI vision is broad CLI/TUI parity and eventual replacement of the TUI.

## Stack

- UI implementation is React + TypeScript.
- Desktop shell is Tauri.
- GUI code lives in this repository under `apps/`.
- `apps/desktop` contains the Tauri desktop app.
- `apps/desktop/packages/*` contains desktop-only shared packages.
- `apps/shared/*` is reserved until there is a second real GUI-app consumer.
- TypeScript API client is hand-written typed JSON-RPC/WebSocket plus GUI-side DTO adapters and contract tests.
- Native browser `WebSocket` plus in-repo JSON-RPC transport/reconnect layer owns request IDs, pending-request rejection, typed protocol errors, capped backoff, auth readiness, bounded buffering, and full refresh after reconnect.
- Do not replay mutations after reconnect; refetch/resubscribe and let the user issue a new command.
- React Query owns server read models, request cache, mutations, invalidation, and WebSocket-driven cache updates.
- Routing uses TanStack Router boxed behind Kent destination helpers.
- Route/search params are validated with Zod at the boundary.
- Dates use native `Intl`, not Temporal.

## Import Boundaries

- Feature components must not import Tauri APIs, raw transport, raw server DTOs, or `react-markdown`.
- Use native bridge packages, shared `MarkdownText`, API adapters, and app-local UI kit exports.
- Native dialog/modal actions go through bridge/helper paths such as `useNativeDialogFallback`.

## Native Bridge

- Native bridge capabilities are explicit and capability-checked.
- Browser implementations use real browser APIs where available.
- Browser may no-op cosmetic shell features only.
- Browser disables self-updater and window controls with explicit explanations.
- Native/client capabilities are separate from server protocol readiness. Use them only for clipboard, directory picker, native windows, window controls, notifications, and similar local affordances.

## Visual System

- No hardcoded colors/fonts in feature components.
- Tailwind is accepted for the desktop GUI despite older notes rejecting it.
- Shared UI/theme source of truth starts app-local.
- Use i18next/react-i18next static English locale files.
- No hardcoded user-facing strings in components.
- User-visible transitions should animate unless reduced motion is active.
- Reduced/deterministic blur and motion are used in tests/snapshots.
- Top-chrome readability treatment is platform-specific and mutually exclusive: macOS, Linux, and browser presentation use the contrast fade; Windows uses progressive blur across every destination and no darkening fade.
- Dropdowns use app-local `SelectField` custom combobox/listbox. Do not use native `<select>` in desktop GUI feature code.
- Use shared `EmptyState`, `LoadingState`, and `ErrorState` surfaces instead of one-off cards.

## Markdown

- Task bodies, comments, and future text surfaces are plain multiline inputs rendered as Markdown.
- Raw HTML is disabled; do not add `rehype-raw`.
- Links use safe-protocol allowlisting, open through native bridge external-link helper, and add `rel="noreferrer"`.
- `code`/`pre` use theme-token styling.

## Startup

- Desktop launches into safe shell even if setup/backend is broken.
- Startup initializes GUI config and server connectivity before feature surfaces mount.
- Protocol compatibility/readiness is the startup gate.
- Protocol mismatch blocks with title `Update Kent`, shows client/server protocol values, instructs updating CLI/service and desktop app from the same build, and includes retry.
- Same blocker is used whether client is newer or older than server.
- JSON-RPC handshake enforces mismatch.
- Readiness exposes server protocol, build, and version for blocker UX.
- If server is unreachable, GUI shows instructions to run the server.
- Startup failures are summary-first: human-readable failure text plus next action;
- Missing/expired/not-ready auth uses the same generic startup failure path as other readiness failures.
- Home does not show runtime identity/header fluff such as endpoint, Kent version, auth mode, logo identity, or runtime metadata.

## Navigation

- Home is the project-first landing destination.
- Relaunch restores last valid project/workflow route when possible; fallback is Home.
- Project workflow board routes are project-scoped.
- Workflow library/editor routes may be global workflow-definition routes, while project-originated task/board routes remain project-scoped.
- Board workflow picker and primary board actions live in a hover/focus non-modal popup/menu.
- Unpinned popup auto-collapses on unhover; pinned popup persists as floating island.
- The desktop version of the app ships no default browser-native hover effects. "No hover effects" means no browser-like hover color changes, highlights, or per-item animation effects. Where requested by humans, hover effects are enabled on an opt-in basis, incl. custom hover behaviors.
- Popup open/close should animate scale, opacity, and material reveal and must respect reduced motion/test mode.
- Back/forward navigation uses app/browser history when available; fallback is Home.

## Home And Projects

- Project creation uses an OS-native directory picker.
- If selected workspace already belongs to a project, the app opens that project/workspace.
- If selected workspace is unbound, the app opens project creation with editable project name and project key.
- Project creation default name comes from selected directory basename.
- Projects with no linked/default workflow show board blocker/empty state, have New Task option disabled, and point to CLI/agent/API workflow setup. Invalid linked workflows remain visible and keep Backlog task creation available as described in the board section.

## Project Edit And Workspaces

- Project key is editable at any time, including after the project has tasks. The input uppercase-normalizes and validates like project creation (2-8 chars, starts A-Z, A-Z/0-9 only) plus uniqueness. Renaming the key only sets the prefix for future task short IDs; existing task short IDs stay frozen at creation (no cascade, no aliases — historical IDs keep resolving).
- Project name is editable and validates like project creation: 1-80 visible chars, no edge whitespace, one line.
- Project name and key changes are saved explicitly together; an unchanged (including empty) persisted key never blocks a name-only save.
- Default workspace changes are saved immediately on selection.
- Back/navigation discards unsaved project name/key changes silently.
- Attach/detach workspace changes are immediate.
- Workspace list is backend cursor-paginated and frontend infinite-scrolled from first implementation.
- Workspace list keeps default workspace first, then sorts by attach time descending, page size 100.
- Workspace row shows only path, default icon, and unlink icon.
- Same path may be linked to multiple projects.
- Same path is deduplicated inside one project; selecting an already-linked path focuses the row or shows equivalent info.
- Attaching/unlinking a workspace never deletes files.
- Unlink hard-deletes the project workspace binding row after validation.
- Unlink blocks default workspace, only workspace, active/non-terminal dependent tasks, active sessions/runs, Kent-managed owned worktree dependencies, and missing durable history snapshots.
- Unlink must not cascade-delete session/task/worktree history.
- Unlink is allowed when only terminal historical tasks reference the workspace and their history remains readable through durable snapshots.
- Unlink confirmation is simple modal, no type-to-confirm, with copy explaining app-state effects, files stay on disk, completed history remains readable, and active work blocks unlink.

## Workflow Board

- Board scope is one selected project plus one selected workflow.
- Board shows tasks from all project workspaces. Workspace is card/context metadata, not primary scope.
- Workflow picker orders project default first, then most-recently-used, then display name.
- Workflow columns follow server/workflow-defined node order.
- Grouped workflows render through group-aware UI. Initial preferred shape is group islands wrapping related columns unless implementation evidence shows better.
- Join nodes are internal and not board columns.
- Cards show task ID, task title, backend-native status component, and workspace chip when useful.
- Cards do not show execution-target policy or locked execution-target facts.
- Board metadata contains selected-workflow, picker, grouping, column, count, validation, and generation facts but no cards. Paged column responses are the sole source of board cards.
- Board-card responses omit the full task body. They carry one nested preview value whose outer whitespace is trimmed and whose Markdown text is hard-cut at 512 Unicode code points with an explicit truncation fact.
- Board columns use bidirectional 25-card pages, retain at most three pages per active column, and virtualize their rows.
- Columns release retained card pages when they leave the near-viewport activation region. Returning to a column loads its newest page at the top inside the existing shell without changing or animating its expanded/collapsed state.
- The client parses Markdown previews only for cards intersecting the visible board viewport. A preview ends with an ellipsis when server truncation or available card space omits trailing content.
- Question-blocked cards replace the normal border color with the primary semantic color. Approval-blocked cards use the secondary semantic color.
- A workspace chip is useful only when the project currently has multiple attached workspaces and the task source workspace differs from the current default workspace. Detached historical workspace context remains available in task detail rather than creating a board exception.
- Card click opens task detail.
- Resume appears only when resumable.
- Interrupt appears in the same action slot when exactly one active run is interruptible and acts immediately.
- Tasks with multiple active runs open detail for per-run controls.
- Board visual states include Backlog/idle, queued, running, interrupted, approval-gated, question-gated, and done/completed.
- Dragging Backlog task to first active node starts automation immediately with no confirmation.
- When task start or an executable manual move requires an execution target, one reusable centered dialog continues the initiating action. It is not anchored to the card/control and does not use the global sidebar.
- Desktop does not override a usable fixed workflow policy; it opens selection only for `ask_on_first_execution` or an unavailable configured target.
- Closing the target dialog leaves the task and initiating action unchanged.
- Normal selection offers no managed worktree, source `HEAD`, repository default branch, and custom Git ref, with repository default branch preselected.
- An unresolvable configured target is identified with its failure reason before the concrete choices. The configured mode and custom-ref input remain selected when useful; otherwise repository default branch is preselected.
- During target resolution, managed-worktree creation, and setup, the dialog remains open, uses shared loading/progress components, disables duplicate submission, and preserves the selection and custom-ref input. Failures stay in the dialog with the actionable server error and Retry/Cancel actions.
- The first executable-node drop hint reads `Drag here to start automation`.
- While dragging a card, approaching a horizontal board edge or vertical hovered-column edge scrolls that surface, accelerating continuously toward the edge. Horizontal and vertical scrolling may run together; horizontal board scrolling takes priority if reliable simultaneous nested scrolling would require materially greater complexity.
- Dragging to Done is a user archive/manual move, not normal edge completion.
- Manual move and Done drag targets follow server action flags derived from exact live scope and runtime-gate evidence. Desktop never infers movement blockers from durable run rows or task status.
- Agent and script drag targets are available only when the server exposes a concrete workflow edge to that executable target.
- Done permissions, pagination, and status handling are server-authoritative.
- Invalid/default-node-only workflows remain visible and their tasks remain visible.
- New Task stays available for invalid workflows and creates Backlog tasks.
- Backlog edits and comments remain available while backend permits.
- Drag/start/run/manual move/Done are disabled for invalid workflows.
- Interrupt and Resume follow server action flags for existing runs from earlier valid states.
- Non-startable Backlog tasks must not disappear.

## Project Labels

- The board has one transparent, single-line chrome row directly above the board content. It is drawn over the blurred window background and is not a separate island.
- The desktop initial release adds only a label filter control to this row; status, attention, column, and sort controls are out of scope.
- The label trigger reads `Labels` with no active filter, `Labels · N` for N selected named labels, and `No labels` for the unlabeled-only filter. A 14–16 px X action appears beside the trigger only while a filter is active and clears the filter.
- One label filter selection and OR/AND mode are shared across every workflow board in the same Project. They persist locally per desktop installation, Kent persistence root, and Project across navigation and relaunch; they are not synchronized between clients.
- OR is the default. The popup uses a compact `OR`/`AND` segmented control. `No labels` toggles the unlabeled-only filter: selecting it clears named selections and disables that control, while selecting it again removes the filter without resetting the remembered named mode. Selecting a named label clears `No labels` and restores the previously chosen mode. Clear removes the active filter and restores OR.
- Filter changes apply reactively through server-side board filtering while the popup remains open. There is no Apply button, no loading indicator, and current cards remain visible until replacement results arrive.
- While a label filter is active, each board column count shows the number of tasks in that column that match the active label expression.
- Deleting a selected label removes it from persisted filter state. Deleting the final named selection removes the label restriction; deleting an ordinary label does not change an active `No labels` filter.
- One reusable label chooser/manager serves board filtering, task assignment, and task creation. There is no standalone Project label-management page in the initial release.
- Label names accept Unicode letters and numbers plus spaces and `: & * % $ # @ ! ? . , / \ + | - _ ~ '`.
- The popup has a pinned search/create field using case-insensitive substring matching. Existing results retain case-insensitive alphabetical order. When no exact case-insensitive match exists, it exposes an explicit `Create “…”` row; creation selects the new label immediately for the invoking context.
- At the 100-label Project limit, search and selection remain available, creation is disabled with an explanation, and deleting a label restores creation immediately.
- The popup sizes naturally for fewer labels. Its scrollable result area shows at most 10 rows or fewer when constrained by available window space; search and context controls remain pinned.
- The popup remains open for selection, creation, rename, and delete flows. Clicking away or pressing Escape closes it. Clicking away while a rename is uncommitted discards that rename.
- Label rows are compact. The whole row toggles selection and selected rows use a subtle highlight plus a small success-colored checkmark. The pencil action is always visible. The trash action appears on hover or keyboard focus for pointer layouts and remains visible for touch-oriented layouts.
- Rename edits the row inline. Enter or the row's checkmark commits; Escape cancels; validation failures remain inline. Delete opens confirmation and removes the label from all tasks when confirmed.
- Assignment contexts omit the OR/AND and `No labels` controls while preserving the same search, create, rename, delete, and selection surface.
- Task labels appear as neutral subtle chips without custom colors.
- Board-card labels are informational and render in the existing one-line footer beside the workspace chip. The footer shows complete chips that fit and replaces the last fitting label position with `+N` when labels overflow.
- Task-detail Properties places Labels immediately after ID. Chips wrap, and the whole value area—including empty space—opens the shared popup; an empty value shows an add-label affordance.
- Label assignment is available for tasks in every lifecycle state. Attach/detach updates chips optimistically, reconciles from the authoritative resulting label set without reloading full task detail, and rolls back with a persistent Retry error when the operation fails.
- Labels display in case-insensitive alphabetical order in the popup, task detail, and board cards. Renaming may reposition them. Manual drag ordering and label-based task sorting are not part of the initial release.

## Task Creation

- Task creation form has required title, optional body/details, Project labels, hidden source URL/import metadata, and source workspace selector.
- Labels appear after Body and before Source workspace. The field reuses the shared label chooser/manager, and selected existing labels are assigned atomically with task creation.
- Creating a Project label from New Task is immediate and selects it for the pending task. The new catalog label remains if the task dialog is later canceled.
- Workspace default is current/opened workspace context when present, otherwise project default/main workspace.
- If project has exactly one workspace, show compact disabled workspace selector/chip.
- Task creation creates a Backlog task. There is no Start button; users drag cards to start them.
- Title/body/source workspace are editable only while task is still in Backlog.
- A locked managed target remains tied to its original source workspace. A no-managed-worktree target uses the task's current source workspace.
- User can see validation error text when task create/edit fails.

## Task Detail

- Task detail opens from Home/Board/shell through reusable native child-window infrastructure when native windows are available.
- Browser/tests use in-app dialog fallback.
- Direct desktop/browser route `/tasks/:taskId` renders standalone inline detail page.
- The task-detail sidebar header exposes a pop-out control (only when native windows are available) that reopens the current task as a standalone native task-detail window and closes the sidebar. Pop-out availability and window options come from a reusable per-destination mapping (`sidebarPopOutOptions`) so future sidebars opt in without new bridge plumbing.
- Pop-out windows are keyed per task: re-popping a task that already has a window focuses the existing window instead of duplicating it; different tasks open separate windows.
- Native/Tauri owns task-detail size and position. Do not keep custom remembered in-app sizing for native detail.
- Closing child window after mutations blanket-refetches visible queries.
- Header/actions and task description are leading rows in the task-detail virtualized list and scroll with the rest of the surface.
- Long task descriptions start in a collapsed read view targeting half the window height, clamped between approximately five and ten rendered text lines. Only overflowing descriptions show the centered bottom expand affordance and text fade; the island itself remains visible.
- Expanding a description is one-way until the current task-detail surface closes, keeps the description top anchored in the viewport, and grows content downward. Entering edit mode expands automatically. Reopening task detail starts from the normal collapsed state.
- `Inbox` area sits above tabs and shows current blockers plus answer/approval/resume controls.
- Contextual resume modal is superseded by task detail Inbox; resume/next-blocker actions focus/reveal relevant Inbox item.
- Tabs are `Comments` and `Activity`; default tab is `Comments`.
- Comments tab has composer, list, edit/delete, and count badge.
- Activity tab is compact timeline with no mutation controls and no count badge.
- Task-detail errors surface through the shared error state without requiring an inline Retry action. Reopening or refreshing the destination is the recovery path.
- Required identity/status fields: task ID, title, body rendered as Markdown, project, workflow, one source-workspace row with its display name stacked above its monospaced root path, current node/status, completion/done state, and server action flags including `can_delete`.
- Conditional fields: locked execution target mode; one managed-worktree path row when the managed worktree is available; requested Git revision, resolved commit, current named branch when the managed root is available; agent role/run status, session ID/name, source URL, and assignee/column ownership when server provides them.
- Task detail does not render standalone Source root or Execution root rows. A no-managed-worktree target communicates source-workspace execution through the Execution target value; an available managed target shows its path only in the Managed worktree row directly below Execution target, before revision and commit facts. An unavailable managed worktree has no Managed worktree row.
- Visibly rendered copyable values in task detail use the Transition Output text-copy interaction: the text itself highlights for pointer and keyboard interaction, no copy icon is shown, and clipboard success or failure appears in the status-toast surface. Success identifies the copied value type; failure includes the clipboard error detail. Actions that copy deliberately hidden payloads, including the generated CLI command and structured interruption details, remain explicit buttons.
- Irrelevant execution-target fields are omitted. Resolved and observed commits render as short monospaced hashes whose text-copy action copies the full hash.
- Missing-field policy: hide expected-not-yet-created fields, show continuity fields empty/unassigned where useful, and render unexpected meaningful missing fields as unavailable/error states. Unavailable managed worktrees are omitted from execution-target facts.
- Task detail allows title/body edit only while still in Backlog. Source URL is shown read-only in Properties and is never editable: valid `http(s)`/`mailto` values render as a compact link labeled with the bare host (e.g. `github.com`) opening in the system browser, and other values fall back to plain `Source: <text>`.
- Task detail loads core task detail, task attention, activity, and comments as independent parallel read models. Core detail renders without waiting for task attention; attention controls appear progressively when their bounded task-attention read completes.
- The attention area has its own loading and retryable error states without disabling core task detail. When task detail opens for a specific Inbox item, it applies that requested focus after the matching task-attention item arrives.
- Task detail self-refreshes live while open: it subscribes to its project's workflow events and refetches its own read models (detail, task attention, activity, comments, pending asks) whenever a server event mutates the task — status, runs, transitions/approvals, comments, questions, or title/body — independent of the hosting surface (board sidebar, Home inbox, or standalone window). Refreshes reuse cached data so the update is flicker-free and never collapses the surface to a loading state.
- Live refresh never overwrites unsaved edits: a clean surface follows server updates, but in-progress title/body edits take priority and are preserved until the user saves or reverts them.

## Comments, Activity, Inbox

- Activity feed uses server read model as source of truth and never loads full transcripts or `events.jsonl`.
- Activity feed is newest-to-oldest and paginated for older entries.
- Deleted comments are hidden unless backend later adds explicit delete audit rows.
- Home Inbox uses the global paginated attention feed and lists/deep-links attention items. Answer/approval actions happen in task detail Inbox.
- Task detail uses a separate bounded, non-paginated task-attention read. Core task detail does not embed attention items, and there is no project-scoped attention feed.
- Task detail sidebars opened from the Home Inbox expose live Previous/Next controls that step through the attention feed order. Navigation reflects the live inbox; after the open task is resolved and leaves the inbox, Next advances to the item that took its place. Controls are Inbox-only — board/standalone task detail has no Previous/Next.
- Top detail action opens or focuses next/highest-priority unresolved attention item.
- If multiple unresolved attention items exist, all get inline controls.
- Question UI preserves ask functionality with options, blank commentary/freeform field, recommended marker, click or arrows plus Enter, and standard Tab focus. Do not show source-origin label.
- An ordinary question with no actual answer suggestions shows only the freeform answer field and does not offer `Neither` as its sole option.
- Runtime approval prompts are surfaced as question attention in Home Inbox, notifications, and task detail. They use the real prompt text, show approval-specific choices, may preselect the primary approval choice, and do not show a `Neither` freeform-answer option. Deny is the negative approval choice and requires commentary.
- Workflow transition approval UI exposes Approve only.
- Approve uses the same centered execution-target dialog when the approved transition first requires selection; dismissal leaves the approval pending.
- Workflow transition approval UI shows stored approval snapshot: source node, transition label/id, target nodes, required provision fields/values, commentary, workflow version, stale warning.
- Interrupt acts immediately with no confirmation.
- Standalone task detail opened from Home attention stays open after resolution; feed/status update and Home row is removed or resorted in background.

## Desktop Attention Notifications

- The server owns one attention-notification publisher and emits live attention events for workflow questions, workflow approvals, and workflow runs interrupted because of errors. User-interrupted runs are not notification-worthy desktop attention.
- The notification stream is live-only. Home Inbox and task detail remain backed by durable attention read models and are not rebuilt from notification events.
- Desktop clients filter out unsupported notification kinds and targets. The server still enforces route authorization and subscription scope so clients cannot receive unauthorized task or session data.
- Generic runtime/session prompts without a desktop answer surface remain non-desktop attention.
- Notification batching is backend-owned. Questions emitted by one assistant turn/tool-call batch in the same task run produce one desktop surface; clients must not infer notification batches with debounce or time-window grouping.
- A focused desktop window shows a persistent in-app Sonner above all app content, including the sidebar. Clicking the Sonner opens the task detail sidebar focused on the relevant attention item.
- Every desktop in-app Sonner/status toast exposes the shared UI-kit close button. Toast lifetime and persistence are controlled separately from closeability; persistent Sonners remain manually closeable.
- An unfocused desktop window attempts a native or browser system notification when the local notification backend can deliver one and activation can focus Kent task detail. macOS, Windows, Linux, and browser backends may advertise support; unknown Tauri platforms do not.
- Native notification activation carries Kent's structured task-detail target. Clicking the delivered notification focuses the main Kent window and opens the matching task detail focus target in the overlay sidebar, not a native child task-detail window.
- Question notifications focus the first unresolved question in the batch. Approval notifications focus the matching approval item. Error-interrupted-run notifications focus the matching interrupted-run attention item in task detail.
- Attention payload messages are optional structured context. Clients own and localize fallback display copy when a message is absent; the server does not synthesize user-facing notification copy.
- Resolved or cleared notification events dismiss matching persistent in-app Sonners.
- Notification delivery is best-effort and must not affect workflow state. Desktop debug builds fail fast with diagnostic information on notification delivery failures; release builds ignore delivery failures and log when logging is wired.

## Connection Loss

- Mutating actions are disabled while disconnected.
- Last cached state may remain visible.
- Global disconnected status is persistent by default until reconnect, can be manually closed through the shared toast close button, and manual close does not change connection state.
- Preserve unsent local text drafts in memory while the window remains open.
- Preserve drafts for new task, comments, and editable task/project text.
- No offline mutation queue.
- No automatic mutation replay after reconnect.
- On reconnect, refresh server state, resubscribe, and let the user submit preserved drafts manually.
- User save overwrites remote state regardless of whether remote changed while disconnected.

## Logs, Telemetry, Release Scope

- Local GUI log lives under Kent persistence root, bounded to 10 MB, redacting auth headers, tokens, env values, and request bodies by default.
- GUI CI runs checks/tests/lint/typecheck/web build/native check in regular CI; full bundles ship through release workflow.
- Do not downgrade GUI toolchain to Node 22 just because it is an LTS floor; use current Node 25+ where available unless concrete issue appears.

## Static Web UI

- Browser-hosted web UI is architecture-compatible future/secondary surface.
- Future direction is Go server serving built SPA assets under an explicit route prefix without taking over server root or conflicting with `/rpc`, `/healthz`, and `/readyz`.

## Q/A Decisions Preserved

- Q: What is the minimal task creation form? A: Required title, optional body/details, hidden source URL, workflow picker outside the form.
- Q: Does creating a task start automation? A: No; it creates a Backlog task and user drags to first active node.
- Q: What status vocabulary appears on cards? A: Backend-native status verbatim, not compact UI aggregation.
- Q: What is canonical board order? A: Backlog fixed left, workflow-defined nodes, Done fixed right.
- Q: Where are completed tasks shown? A: Same board in fixed-right Done with per-node infinite scroll.
- Q: What task fields are required if backend data is missing? A: Hide expected-not-yet-created fields, show continuity fields empty/unassigned, unexpected meaningful missing fields as unavailable/error.
- Q: Where are workflow questions and approvals answered? A: Home Inbox lists/deep-links; task detail Inbox owns action controls.
- Q: Should Interrupt confirm? A: No.
- Q: Should drag-to-start confirm? A: No.
- Q: What format do body/details/comments use? A: Plain multiline Markdown, no WYSIWYG.
- Q: How does desktop find server endpoint? A: Kent config/default host and port only. Desktop does not persist a separate endpoint. When the configured host is an unspecified IP listener, Desktop projects `0.0.0.0` to `127.0.0.1` and `::` to `::1` for its connection endpoint while preserving the configured port; concrete hosts remain unchanged. This Desktop-only projection does not edit Kent config.
- Q: How should workflow groups render? A: Implementation-led first pass, initial preference group islands.
- Q: What happens to drafts during disconnect? A: Keep local drafts, disable submit, refresh on reconnect, user manually saves and overwrites.
- Q: What should the task detail CLI action do? A: Copy `kent --session=<session-id>` to clipboard and show a success toast. Do not launch terminals from the GUI.
- Q: How does project creation map directory picker result to Kent project/workspace binding? A: Bound workspace opens existing project; unbound workspace opens project creation with editable project name and key.
