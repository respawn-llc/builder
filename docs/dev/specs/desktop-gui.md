# Desktop GUI

## Authority, Connection, And Shared Behavior

- Desktop is a thin remote-control client of an already-running Kent server. The server is authoritative for Projects, workspaces, Workflows, Tasks, runtime, Workflow Execution live state, validation, Approvals, Questions, comments, worktrees, and durable state.
- Desktop never starts or bundles the Kent server. It connects using Kent's configured host and port and does not store a separate endpoint. It maps configured listener host `0.0.0.0` to `127.0.0.1` and `::` to `::1`, preserves the configured port, leaves concrete hosts unchanged, and does not edit Kent configuration.
- A compatible, ready server is required before feature content opens. If protocols are incompatible, show `Update Kent`, the client and server protocol values, instructions to update the service and desktop from the same build, and Retry. Use the same blocker whichever side is newer.
- If the server is unavailable or authentication is not ready, show a concise failure and next action, including instructions to run the server when unreachable.
- A safe application shell remains available when startup fails. Home omits endpoint, version, authentication mode, and other runtime identity.
- On connection loss, disable mutations while retaining cached content where available. Show persistent disconnected status until reconnection; closing that notice does not change connection state.
- Keep unsent local drafts for new Tasks, comments, and editable Task or Project text while the window stays open. Do not queue or replay mutations. After reconnection, refresh server state and let the operator submit preserved drafts manually; a save overwrites remote changes.
- Local capabilities such as clipboard, directory selection, separate windows, window controls, and notifications are distinct from server readiness. When unavailable, explain the unavailable action; cosmetic shell behavior may be absent in a browser presentation.
- Text input is plain multiline Markdown. Raw HTML is unavailable. Links allow only safe protocols and open externally. Code is styled distinctly.
- Desktop uses localized user-facing text, accessible controls, standard compact loading, error, and empty states, and motion that respects reduced-motion preference. macOS, Linux, and browser presentation use a contrast fade for readable top chrome; Windows uses progressive blur without a darkening fade.
- Cards are reserved for board Task cards. Navigation, browsing, and selection collections use list rows.

## Home And Navigation

- Home opens on Inbox unless a valid previously open Project or Workflow destination can be restored. Back and forward use available navigation history; otherwise Back returns Home.
- Home has compact navigation and content side by side when both remain usable, otherwise navigation stacks above content. It has no resizable splitter or temporary navigation drawer.
- Navigation pins global Inbox above the infinite-scrolling Projects list. Inbox is not a Project and has no required attention badge.
- Selecting Inbox shows aggregate Inbox. Selecting a Project opens its workspace, whose `Workflows` and `Sessions` tabs share one window-local last-used selection while moving between Projects; relaunch defaults to `Workflows`.
- Selecting an Inbox item leaves Inbox visible and opens Task Detail as an overlay. It does not replace Home content or navigate away from Home.
- The sticky Project workspace header shows Project name and key, the two tabs, and `Link workflow` or `New Session` as appropriate.
- Workflow links are an infinite-scrolling list. Each shows reusable Workflow identity, Project-default state, and validation state. Selecting one opens that Project's Workflow board.
- `Manage workflows` is available from `Link workflow` for reusable Workflows, including those not linked to the selected Project. Project Task and board destinations remain Project-scoped.
- Board actions and Workflow selection use a non-modal menu that may be pinned. An unpinned menu closes when it loses hover; opening and closing respects reduced motion. Desktop has no default browser-style hover effects beyond explicitly designed interaction feedback.
- An empty Inbox says `All caught up` and does not show recent Projects or choose a Project automatically.
- Project navigation rows show Project identity and editing. Project name and default-workspace path use at most two lines. Selection is communicated accessibly as well as visually.

## Projects And Workspaces

- Choosing a directory already attached to a Project opens that Project. Choosing an unattached directory opens Project creation with an editable name and Project Key; the default name is the directory basename.
- A Project Key is editable at any time, is unique, uses 2–8 uppercase letters or digits, begins with a letter, and is the prefix for future Task Short IDs. Existing Task Short IDs remain unchanged and resolvable.
- Project name is one line, 1–80 visible characters, and has no leading or trailing whitespace. Name and key save together; an unchanged persisted key, including an empty one, does not block a name-only save. Back discards unsaved name and key edits.
- Changing the default workspace or attaching or detaching a workspace applies immediately. Workspace lists use infinite scroll, put the default first, then use newest attachment first, and contain at most 100 entries per request.
- A workspace row shows path, default status, and unlink action. A path may belong to multiple Projects but only once in one Project. Choosing an already attached path focuses its existing row or gives equivalent feedback.
- Attaching or unlinking never deletes files. Unlink removes only the Project-workspace relationship and never deletes Task, Session, or worktree history.
- Unlink is blocked for a default or sole workspace, non-terminal dependent Tasks, live Session execution, Kent-managed worktree dependencies, or missing retained Session location information. It is allowed when references are only terminal Tasks and retained Sessions whose locations remain readable.
- Unlink confirmation explains that files remain on disk, retained Sessions remain readable, and active work blocks removal. It requires no typed confirmation.
- A Project without a linked default Workflow shows a blocker and disables New Task while directing the operator to configure a Workflow. An invalid linked Workflow remains visible and permits Backlog Task creation.

## Workflow Boards

- A board is one Project and one Workflow, and shows Tasks from all attached workspaces. Workspace is context, not the board scope.
- Workflow selection orders the Project default first, then recently used Workflows, then display name. Backlog is fixed left, Workflow Nodes follow their defined order, and Done is fixed right. Join Nodes are not columns. Groups preserve their Workflow grouping.
- A Task card shows Task Short ID, title, server-native status text, and workspace context only when the Project has multiple workspaces and the Task source differs from the default workspace. It does not show Execution Target facts.
- Board cards use infinite scroll in both directions, 25 cards per page, and retain at most three nearby pages per active column. Cards outside the nearby area release their loaded pages; returning starts at that column's newest page without changing its expanded state.
- Card bodies are previews, not full bodies: outer whitespace is removed, content is limited to 512 Unicode code points, and truncation is explicit. Only visible cards render Markdown previews. An ellipsis indicates either truncated content or insufficient card space.
- Questions and Approvals have distinct semantic card emphasis. Card selection opens Task Detail.
- Resume appears only when the server says it is available. Interrupt appears in the same action position only for exactly one interruptible live agent Session and acts immediately. Several live agent Sessions use Task Detail for per-Session control; scripts use the Task-wide action.
- Board states include Backlog, idle, queued, running, interrupted, Approval-blocked, Question-blocked, and done.
- Dragging a Backlog Task to its first executable Node starts it immediately without confirmation; that target says `Drag here to start automation`. A drop onto Done is a manual archive move, not normal Workflow completion.
- Starting or manually moving to executable work opens Execution Target selection only when the Workflow asks on first execution or its configured target is unavailable. A usable fixed policy is not overridden.
- Execution Target selection offers no managed worktree, source `HEAD`, repository default branch, and custom Git ref, defaulting to repository default branch. An unavailable configured target explains the failure and preserves the useful prior selection and custom ref where possible.
- Closing Execution Target selection leaves the Task unchanged. During resolution or setup, preserve the selection, prevent duplicate submission, and keep actionable failure with Retry and Cancel in the same dialog.
- Board movement, Done permission, paging, status, Resume, and Interrupt follow server-authoritative live execution facts. The desktop never infers blockers from stored Task state.
- Agent and Script drop targets exist only for actual Workflow edges. Invalid and default-Node-only Workflows remain visible with their Tasks. Invalid Workflows permit Backlog creation, editing where allowed, and comments, but disable drag, Start, Resume, manual move, and Done. Existing executable Nodes created under an earlier valid definition retain their server-provided Resume and Interrupt actions.
- A non-startable Backlog Task remains visible.
- Dragging near a board or hovered-column edge scrolls that surface with increasing speed. Horizontal and vertical scrolling can run together; horizontal takes priority if both cannot be reliable.

## Labels

- Boards have one transparent label-filter row. It provides no status, attention, column, or sort filter.
- The trigger says `Labels` with no filter, `Labels · N` with selected Labels, and `No labels` for the unlabeled filter. A clear action appears only for an active filter.
- One Label filter and its OR/AND mode apply to every board in a Project and persist for that Project in the desktop installation. They are not shared with other clients. OR is the default.
- `No labels` clears named selections and disables OR/AND. Selecting it again removes that filter without losing the remembered named mode. Selecting a Label clears `No labels` and restores the remembered mode. Clear removes the filter and restores OR.
- Filter changes apply immediately through server filtering while the chooser remains open. There is no Apply step. Existing cards remain visible without a replacement loading state until new content arrives. Active filters change each column count to the matching Task count.
- Deleting a selected Label removes it from the saved filter. Removing the last selected Label clears the named restriction; deleting another Label does not change an active `No labels` filter.
- One chooser manages filtering, Task Label assignment, and Label creation, renaming, and deletion. There is no separate Project Label page.
- Search is case-insensitive substring matching with case-insensitive alphabetical results. When no exact case-insensitive name exists, offer `Create “…”`; creation immediately selects the Label for the invoking use.
- A Project permits at most 100 Labels. At the limit, search and selection remain available and creation explains its unavailability; deletion restores creation.
- The chooser shows at most 10 scrolling result rows, keeps search and context controls visible, remains open through selection and management actions, and discards an uncommitted rename on close.
- Rows toggle selection. Rename edits in place and can be committed or cancelled; validation failures remain inline. Deleting a Label requires confirmation and removes it from all Tasks.
- Assignment omits OR/AND and `No labels` but otherwise has the same chooser behavior. Labels are neutral chips, ordered case-insensitively in the chooser, Task Detail, and board cards. Renaming can reposition them.
- Board cards show fitting complete Labels in their footer and replace the last fitting position with `+N` when needed. Task Detail places Labels directly after Task ID; the entire Label value opens the chooser.
- Labels can change in every Task state. The interface updates immediately, then adopts the server result; failures restore the prior state and show a persistent Retry error.

## Tasks

- New Task requires title and accepts optional body, Project Labels, hidden source information, and source workspace. Workflow selection is outside the form. The workspace defaults to the opened workspace or Project default workspace. With one workspace, selection is shown but unavailable.
- Selected existing Labels are assigned atomically with Task creation. Creating a Label during Task creation selects it immediately; it remains a Project Label if Task creation is cancelled.
- Creation makes a Backlog Task; it does not start automation. Title, body, and source workspace are editable only in Backlog. A managed Execution Target remains tied to its original source workspace; no-managed-worktree execution uses the Task's current source workspace.
- Task creation and editing show server validation errors.
- Task Detail can appear inline, in a separate window when supported, or as a standalone destination. Reopening an already separate Task Detail focuses it rather than duplicating it. Closing it after a mutation refreshes visible content.
- Long descriptions start collapsed only when they overflow, at roughly half the available height and never fewer than about five or more than about ten rendered lines, with an expand action. Expansion lasts until that Task Detail closes, keeps the description top anchored, grows downward, and occurs automatically for editing.
- Task Detail begins with Inbox, which contains current blockers and answer, Approval, and Resume controls. Comments have composer, list, edit, delete, and count. There is no completed Workflow movement or execution-history view.
- Task Detail shows Task Short ID, title, Markdown body, Project, Workflow, source workspace name and root, all Current Nodes and states, completion state, and available actions including Task Delete. When available, it also shows Execution Target, managed worktree, requested revision, resolved commit, branch, Agent role and execution state, Session identity, source URL, and assignee or column.
- Source root and Execution Root are not separate facts. Unavailable expected facts are hidden; useful continuity facts may be empty or unassigned; unexpected meaningful absence is an unavailable or error state. Unavailable managed worktrees have no managed-worktree fact.
- Visible values copy by selecting the value itself, with clipboard feedback that identifies the copied value on success and includes the error on failure. Short commit display copies the complete commit. Actions that copy deliberately hidden content remain explicit controls.
- Source URL is read-only. Valid web, secure web, and mail links use their host as the label and open externally; other values are plain source text.
- Core Task Detail, Task attention, and comments load independently. Attention has its own loading and retry state and never blocks core detail. Opening from Inbox focuses its requested attention item once available. Live server changes update open Task Detail without replacing unsaved title or body edits or collapsing the surface.
- A non-attention Task Detail failure uses the standard error state; reopening or refreshing Task Detail is its recovery path. Deleted comments are hidden.

## Inbox, Questions, Approvals, And Notifications

- Inbox lists the global infinite-scrolling attention feed. Task Detail owns Question and Approval actions through its bounded Task attention view.
- Inbox-opened Task Detail can move through the live Inbox order with Previous and Next. After resolution removes the open Task, Next advances to the replacement item. These controls are unavailable outside Inbox.
- The top Task Detail action opens or focuses the highest-priority unresolved attention. All unresolved items retain inline controls.
- Questions support suggestions, freeform commentary, recommendations, pointer or keyboard selection, and ordinary focus navigation. An option Question selects its valid recommended option by default and otherwise selects option 1; malformed recommendation metadata follows the same option-1 fallback. Live refresh preserves the user's selection and draft. Selection and recommendation remain distinct states. A Question with no suggestions has only freeform response and does not offer `Neither`.
- Runtime Approvals use the actual prompt, approval-specific choices, select the one-time allow choice when offered and otherwise the first offered choice, and do not offer `Neither`; Deny requires commentary. Workflow transition approval offers only Approve and shows source Node, Transition Key and label, target Nodes, required values, commentary, Workflow Version, and stale warning.
- Approving a transition that first needs an Execution Target uses the same selection dialog. Dismissing it leaves Approval pending. Interrupt acts immediately without confirmation.
- Task Detail opened from Inbox remains open after resolution while Inbox updates in the background.
- Desktop attention includes workflow Questions, workflow Approvals, and executable Nodes interrupted by errors, not user interruptions or generic prompts without a desktop response.
- A focused window shows persistent in-app attention notification; selecting it opens Task Detail at the relevant attention item. Every notification can be closed regardless of duration.
- An unfocused window attempts a system notification when available. Activation focuses the main window and opens the matching inline Task Detail target. Question notifications select the first unresolved Question in their batch; Approval and error-interruption notifications select their matching item.
- Notification batches are server-authoritative. Resolved notifications dismiss matching persistent in-app notifications. Notification delivery never changes Workflow state; development failures fail immediately with diagnostics, while production continues without delivery.
- Notification messages are optional structured context. Desktop owns and localizes fallback copy when a message is absent; the server never supplies fallback UI copy.

## Logs

- The desktop log is stored under the Kent persistence root and is limited to 10 MB.
- By default, logs redact authentication headers, tokens, environment values, and request bodies.
