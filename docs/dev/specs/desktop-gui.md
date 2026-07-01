# Desktop GUI Spec

## Scope And Authority

- Desktop GUI is a remote-control client over an already-running Kent server.
- Server remains authoritative for projects, workspaces, workflows, tasks, runtime, scheduler state, validation, approvals, questions, comments, worktrees, persistence, and subscriptions.
- The Tauri app never bundles or starts the Kent server binary as a sidecar.
- GUI workflow API/read-model churn before Kent 2.0 is isolated behind GUI-side adapters.
- Long-term GUI vision is broad CLI/TUI parity and eventual replacement for routine desktop workflows, but no detailed design/planning is active now.

## Stack

- UI implementation is React + TypeScript.
- Desktop shell is Tauri.
- GUI code lives in this repository under `apps/`.
- `apps/desktop` contains the Tauri desktop app.
- `apps/desktop/packages/*` contains desktop-only shared packages.
- `apps/shared/*` is reserved until there is a second real GUI-app consumer.
- MVP feature code stays in `apps/desktop/src`; extract packages only for a second consumer, stable independent boundary, or oversized cohesive module.
- Package manager is pnpm.
- TypeScript API client is hand-written typed JSON-RPC/WebSocket plus GUI-side DTO adapters and contract tests.
- Native browser `WebSocket` plus in-repo JSON-RPC transport/reconnect layer owns request IDs, pending-request rejection, typed protocol errors, capped backoff, auth readiness, bounded buffering, and full refresh after reconnect.
- Do not replay mutations after reconnect; refetch/resubscribe and let the user issue a new command.
- React Query owns server read models, request cache, mutations, invalidation, and WebSocket-driven cache updates.
- Routing uses TanStack Router boxed behind Kent destination helpers.
- Route/search params are validated with Zod at the boundary.
- Forms use React Hook Form, `@hookform/resolvers`, and Zod. TanStack Form is deferred until workflow editor forms are genuinely complex.
- Dates use native `Intl`, not Temporal.
- Command palette is deferred; use `cmdk` when product-relevant.
- Shortcut map is deferred. Use platform-native modifiers, do not capture global shortcuts while typing, expose shortcuts visibly, and avoid keyboard-only hidden paths.

## Import Boundaries

- Feature components must not import Tauri APIs, raw transport, raw server DTOs, or `react-markdown`.
- Use native bridge packages, shared `MarkdownText`, API adapters, and app-local UI kit exports.
- Native dialog/modal actions go through bridge/helper paths such as `useNativeDialogFallback`.
- API-backed dialogs stay behind `StartupGate`.
- Event-only dialogs can bypass `StartupGate` when they communicate back to the main window through native events.

## Native Bridge

- Native bridge capabilities are explicit and capability-checked.
- Browser implementations use real browser APIs where available.
- Browser may no-op cosmetic shell features only.
- Browser disables updater and window controls with explicit explanations.
- Native/client capabilities are separate from server protocol readiness. Use them only for clipboard, directory picker, native windows, window controls, notifications, and similar local affordances.

## Visual System

- UI direction is clean, elegant, effective, island-based, and productivity-focused.
- MVP uses fixed desktop window shell with scrollable islands/panes.
- First MVP ships macOS-first with native border, native shape, traffic-light controls integrated into app chrome, and macOS blurred glass/vibrancy background.
- Island-style rounded surfaces render over glass material with readable contrast in light/dark themes.
- Tauri identifier `sh.kent` is final.
- Theme supports dark, light, and config override; system/auto otherwise.
- Montserrat is the main UI font.
- Monaspace Neon is used for IDs, paths, session IDs, branch names, code, commands, and log-like values.
- Semantic tokens own palette, spacing, radius, shadow, typography, and motion.
- No hardcoded colors/fonts in feature components.
- Tailwind is accepted for the desktop GUI despite older notes rejecting it.
- Shared UI/theme source of truth starts app-local.
- Use i18next/react-i18next static English locale files. No runtime language switch in MVP.
- No hardcoded user-facing strings in components.
- User-visible transitions should animate unless reduced motion is active.
- Reduced/deterministic blur and motion are used in tests/snapshots.
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
- Server/backend capability registry is removed; do not build `server.capabilities.get` for MVP.
- Protocol mismatch blocks with title `Update Kent`, shows client/server protocol values, instructs updating CLI/service and desktop app from the same build, and includes retry.
- Same blocker is used whether client is newer or older than server.
- JSON-RPC handshake enforces mismatch.
- Readiness exposes server protocol, build, and version for blocker UX.
- If server is unreachable, GUI shows instructions to run the server.
- Startup failures are summary-first: human-readable failure text plus next action; deeper diagnostics go to local GUI log.
- Missing/expired/not-ready auth uses the same generic startup failure path as other readiness failures.
- Home does not show runtime identity/header fluff such as endpoint, Kent version, auth mode, logo identity, or runtime metadata.

## Navigation

- Home is the project-first landing destination.
- Home uses a fixed glass shell split into Projects and Inbox.
- Relaunch restores last valid project/workflow route when possible; fallback is Home.
- User can return to Home/project list.
- Board has no persistent project-list sidebar.
- Project workflow board routes are project-scoped.
- Workflow library/editor routes may be global workflow-definition routes, while project-originated task/board routes remain project-scoped.
- Board workflow picker and primary board actions live in a hover/focus non-modal popup/menu.
- Unpinned popup auto-collapses on unhover; pinned popup persists as floating island.
- "No hover effects" means no browser-like hover color changes, highlights, or per-item animation effects.
- Popup open/close may animate scale, opacity, and material reveal and must respect reduced motion/test mode.
- Settings UI is removed from workflow MVP: no settings route/button/top app bar.
- Visible diagnostics destination is removed from MVP navigation. Local logs may remain support/developer paths.

## Home And Projects

- Home left pane lists projects sorted by latest activity descending.
- Project rows are simplified to project key, project name, and primary workspace path.
- Project row pencil icon appears top-right aligned with the row title area and opens project edit.
- Clicking a project row opens that project's default workflow board.
- Home right pane is `Inbox`, sorted newest activity first across attention items.
- Home Inbox rows use `Inbox` terminology and open task detail.
- Home rows do not contain answer/approval controls.
- Project creation uses an OS-native directory picker.
- If selected workspace already belongs to a project, the app opens that project/workspace.
- If selected workspace is unbound, the app opens project creation with editable project name and project key.
- Project creation default name comes from selected directory basename.
- Project key is editable during creation; backend validates collisions and immutability once tasks exist.
- Projects with no linked/default workflow show board blocker/empty state, keep New Task disabled, and point to CLI/agent/API workflow setup. Invalid linked workflows remain visible and keep Backlog task creation available as described in the board section.

## Project Edit And Workspaces

- Project edit is a full main-shell page route `/projects/$projectId/edit`.
- It is not a native child window.
- Back uses app/browser history when available; fallback is Home.
- Project key is editable at any time, including after the project has tasks. The input uppercase-normalizes and validates like project creation (2-8 chars, starts A-Z, A-Z/0-9 only) plus uniqueness. Renaming the key only sets the prefix for future task short IDs; existing task short IDs stay frozen at creation (no cascade, no aliases — historical IDs keep resolving).
- Project name is editable and validates like project creation: 1-80 visible chars, no edge whitespace, one line.
- Project name and key changes are saved explicitly together; an unchanged (including empty) persisted key never blocks a name-only save.
- Default workspace changes are saved immediately on selection.
- Back/navigation discards unsaved project name/key changes silently.
- Attach/detach workspace changes are immediate.
- Workspace list is backend cursor-paginated and frontend infinite-scrolled from first implementation.
- Workspace list keeps default workspace first, then sorts by attach time descending, page size 100.
- Workspace row shows only path, default icon, and unlink icon.
- Unlink icon uses unchain/unlink semantics, not trash.
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
- New Task exists only inside workflow/Kanban view and uses current selected workflow.
- Backlog is fixed left.
- Workflow columns follow server/workflow-defined node order.
- Done is fixed right, normal paginated node column and drop target.
- All board pagination uses infinite scroll only; no page numbers, next/previous, Load More, or button pagination.
- Grouped workflows render through group-aware UI. Initial preferred shape is group islands wrapping related columns unless implementation evidence shows better.
- Join nodes are internal and not board columns.
- Cards show task ID, task title, backend-native status component, and workspace chip when useful.
- Card click opens task detail.
- Resume appears only when resumable.
- Interrupt appears in the same action slot when exactly one active run is interruptible and acts immediately.
- Tasks with multiple active runs open detail for per-run controls.
- Board visual states include Backlog/idle, queued, running, interrupted, approval-gated, question-gated, done/completed, canceled, and validation-blocked.
- Dragging Backlog task to first active node starts automation immediately with no confirmation.
- Dragging to Done is a user archive/manual move, not normal edge completion.
- Done permissions, pagination, and status handling are server-authoritative.
- Invalid/default-node-only workflows remain visible and their tasks remain visible.
- New Task stays available for invalid workflows and creates Backlog tasks.
- Backlog edits and comments remain available while backend permits.
- Drag/start/run/manual move/Done are disabled for invalid workflows.
- Cancel, interrupt, and resume follow server action flags for existing runs from earlier valid states.
- Non-startable Backlog tasks must not disappear.
- Board search/filter remains deferred and is not current product scope.

## Task Creation

- Task creation form has required title, optional body/details, hidden source URL/import metadata, and source workspace selector.
- Workspace default is current/opened workspace context when present, otherwise project default/main workspace.
- If project has exactly one workspace, show compact disabled workspace selector/chip.
- Task creation creates a Backlog task. There is no Start button in MVP.
- Title/body/source workspace are editable only while task is still in Backlog.
- Source workspace becomes immutable after start/worktree creation.
- User can see validation error text when task create/edit fails.

## Task Detail

- Task detail opens from Home/Board/shell through reusable native child-window infrastructure when native windows are available.
- Browser/tests use in-app dialog fallback.
- Direct desktop/browser route `/tasks/:taskId` renders standalone inline detail page.
- The task-detail sidebar header exposes a pop-out control (only when native windows are available) that reopens the current task as a standalone native task-detail window and closes the sidebar. Pop-out availability and window options come from a reusable per-destination mapping (`sidebarPopOutOptions`) so future sidebars opt in without new bridge plumbing.
- Pop-out windows are keyed per task: re-popping a task that already has a window focuses the existing window instead of duplicating it; different tasks open separate windows.
- Native/Tauri owns task-detail size and position. Do not keep custom remembered in-app sizing for native detail.
- Closing child window after mutations blanket-refetches visible queries.
- Fixed header/actions and task description are always visible.
- `Inbox` area sits above tabs and shows current blockers plus answer/approval/resume controls.
- Contextual resume modal is superseded by task detail Inbox; resume/next-blocker actions focus/reveal relevant Inbox item.
- Tabs are `Comments`, `Activity`, and `Runs`; default tab is `Comments`.
- Comments tab has composer, list, edit/delete, and count badge.
- Activity tab is compact timeline with no mutation controls and no count badge.
- Runs tab contains runs, worktree/session info, and telemetry when too dense for header; it has a run count badge.
- Required identity/status fields: task ID, title, body rendered as Markdown, project, workflow, source workspace, current node/status, completion/done/cancel state, and server action flags.
- Conditional fields: worktree path, agent role/run status, session ID/name, source URL, assignee/column ownership when server provides it.
- Missing-field policy: hide expected-not-yet-created fields, show continuity fields empty/unassigned where useful, and render unexpected meaningful missing fields as unavailable/error states.
- Task detail allows title/body edit only while still in Backlog. Source URL is shown read-only in Properties and is never editable: valid `http(s)`/`mailto` values render as a compact link labeled with the bare host (e.g. `github.com`) opening in the system browser, and other values fall back to plain `Source: <text>`.
- Task detail self-refreshes live while open: it subscribes to its project's workflow events and refetches its own read models (detail, activity, comments, pending asks) whenever a server event mutates the task — status, runs, transitions/approvals, comments, questions, or title/body — independent of the hosting surface (board sidebar, Home inbox, or standalone window). Refreshes reuse cached data so the update is flicker-free and never collapses the surface to a loading state.
- Live refresh never overwrites unsaved edits: a clean surface follows server updates, but in-progress title/body edits take priority and are preserved until the user saves or reverts them.

## Comments, Activity, Inbox

- Comments support full create/edit/delete in MVP.
- Activity feed uses server read model as source of truth and never loads full transcripts or `events.jsonl`.
- Activity feed is newest-to-oldest and paginated for older entries.
- Deleted comments are hidden unless backend later adds explicit delete audit rows.
- Home Inbox lists/deep-links attention items. Answer/approval actions happen in task detail Inbox.
- Task detail sidebars opened from the Home Inbox expose live Previous/Next controls that step through the attention feed order. Navigation reflects the live inbox; after the open task is resolved and leaves the inbox, Next advances to the item that took its place. Controls are Inbox-only — board/standalone task detail has no Previous/Next.
- Top detail action opens or focuses next/highest-priority unresolved attention item.
- If multiple unresolved attention items exist, all get inline controls.
- Question UI preserves ask functionality with options, blank commentary/freeform field, recommended marker, click or arrows plus Enter, and standard Tab focus. Do not show source-origin label.
- Approval UI exposes Approve only. Reject is deferred until negative semantics are defined; use Interrupt or Cancel for negative paths.
- Approval UI shows stored approval snapshot: source node, transition label/id, target nodes, required provision fields/values, commentary, workflow version, stale warning.
- Cancel requires confirmation and no reason.
- Interrupt acts immediately with no confirmation.
- Standalone task detail opened from Home attention stays open after resolution; feed/status update and Home row is removed or resorted in background.

## Connection Loss

- Mutating actions are disabled while disconnected.
- Last cached state may remain visible.
- Global disconnected status remains visible until reconnect.
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
- Production web UI hosting is deferred.
- Future direction is Go server serving built SPA assets under an explicit route prefix without taking over server root or conflicting with `/rpc`, `/healthz`, and `/readyz`.
- Exact web UI route prefix, cache policy, SPA fallback, opt-in/always-on behavior, and CLI command naming are deferred.

## Q/A Decisions Preserved

- Q: What is the minimal task creation form? A: Required title, optional body/details, hidden source URL, workflow picker outside the form.
- Q: Does creating a task start automation? A: No; it creates a Backlog task and user drags to first active node.
- Q: What status vocabulary appears on cards? A: Backend-native status verbatim, not compact UI aggregation.
- Q: What is canonical board order? A: Backlog fixed left, workflow-defined nodes, Done fixed right.
- Q: Where are completed tasks shown? A: Same board in fixed-right Done with per-node infinite scroll.
- Q: What task fields are required if backend data is missing? A: Hide expected-not-yet-created fields, show continuity fields empty/unassigned, unexpected meaningful missing fields as unavailable/error.
- Q: Where are workflow questions and approvals answered? A: Home Inbox lists/deep-links; task detail Inbox owns action controls.
- Q: Should cancel require a reason? A: No; confirmation only.
- Q: Should Interrupt confirm? A: No.
- Q: Should drag-to-start confirm? A: No.
- Q: What format do body/details/comments use? A: Plain multiline Markdown, no WYSIWYG.
- Q: How does desktop find server endpoint? A: Kent config/default host and port only.
- Q: How should workflow groups render? A: Implementation-led first pass, initial preference group islands.
- Q: What happens to drafts during disconnect? A: Keep local drafts, disable submit, refresh on reconnect, user manually saves and overwrites.
- Q: What should the task detail CLI action do? A: Copy `kent --session=<session-id>` to clipboard and show a success toast. Do not launch terminals from the GUI.
- Q: How does project creation map directory picker result to Kent project/workspace binding? A: Bound workspace opens existing project; unbound workspace opens project creation with editable project name and key.
- Q: Should board search become current scope? A: No; keep it deferred.
