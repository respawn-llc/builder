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
- Text input is plain multiline Markdown. Task Detail and Workflow Editor content use the approved rich Markdown presentation with sanitized raw-HTML and link behavior. Board previews are flattened text previews: they strip Markdown formatting and raw HTML without rendering rich structure or controls, preserve readable text labels, and remain bounded for dense boards. Completed supported code is syntax-highlighted and selectable in rich content; incomplete code remains selectable plain text.
- When focus is in a Desktop text field outside the Workflow editor, Command+Enter on macOS and Ctrl+Enter on Windows or Linux must invoke that field's existing submit, save, or selection action. The shortcut must follow the same validation, disabled state, and confirmation behavior as that action.
- The shortcut must do nothing when the focused text field has no existing submission action.
- The shortcut must not change the field's ordinary Enter behavior.
- Desktop uses localized user-facing text, accessible controls, standard compact loading, error, and empty states, and motion that respects reduced-motion preference. macOS, Linux, and browser presentation use a contrast fade for readable top chrome; Windows uses progressive blur without a darkening fade.
- Dialogs, popups, confirmation flows, and dropdowns only collect an operator
  result. They close before returning that result to their parent destination.
  The parent destination owns navigation, server requests, pending state,
  failures, and retries through its existing action paths.
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

- Shared Project-workspace relationships and detach safety follow the [Projects And Workspaces](project-workspaces.md) specification.
- Choosing a directory already attached to a Project opens that Project. Choosing an unattached directory opens Project creation with an editable name and Project Key; the default name is the directory basename.
- A Project Key is editable at any time, is unique, uses 2–8 uppercase letters or digits, begins with a letter, and is the prefix for future Task Short IDs. Existing Task Short IDs remain unchanged and resolvable.
- Project name is one line, 1–80 visible characters, and has no leading or trailing whitespace. Name and key save together; an unchanged persisted key, including an empty one, does not block a name-only save. Back discards unsaved name and key edits.
- Changing the default workspace or attaching or detaching a workspace applies immediately. Workspace lists use infinite scroll, contain at most 100 entries per request, show the default first when it belongs to the bounded collection, then use newest attachment first, and stop at the global Project Workspace collection limit.
- A workspace row shows path, default status, and unlink action. Choosing an already attached path focuses its existing row or gives equivalent feedback.
- Unlink confirmation explains that files remain on disk, retained Sessions remain readable, and active work blocks removal. It requires no typed confirmation.
- A Project without a linked default Workflow shows a blocker and disables New Task while directing the operator to configure a Workflow. An invalid linked Workflow remains visible and permits Backlog Task creation.

## Workflow Boards

- A board is one Project and one Workflow, and shows Tasks from all attached workspaces. Workspace is context, not the board scope.
- Workflow selection orders the Project default first, then recently used Workflows, then display name. Backlog is fixed left, Workflow Nodes follow their defined order, and Done is fixed right. Join Nodes are not columns. Groups preserve their Workflow grouping.
- A Task card shows Task Short ID, title, server-native status text, and workspace context only when the Project has multiple workspaces and the Task source differs from the default workspace. It does not show Execution Target facts.
- A Task card with one or more direct Blocker Tasks shows a dependency-progress
  chip before its Labels.
- The dependency-progress chip uses a circular progress indicator and
  `satisfied blockers / total blockers` text.
- The chip uses the primary tone while any direct dependency is unsatisfied and
  the success tone when every direct dependency is satisfied.
- The chip's accessible name is `Dependencies: N of M complete.`.
- Hovering or focusing the chip shows `N of M dependencies satisfied`.
- When every direct dependency is satisfied, the tooltip appends
  `. Time to cook!`.
- A Task card with no direct Blocker Tasks omits the chip.
- The server supplies both dependency-progress counts. Desktop never derives
  them from loaded relationship rows.
- Selecting the dependency-progress chip opens Task Detail focused on
  Dependencies.
- Board cards use infinite scroll in both directions, 25 cards per page, and retain at most three nearby pages per active column. Cards outside the nearby area release their loaded pages; returning starts at that column's newest page without changing its expanded state.
- Card bodies are previews, not full bodies: outer whitespace is removed, content is limited to 512 Unicode code points, and truncation is explicit. Only visible cards render Markdown previews. An ellipsis indicates either truncated content or insufficient card space.
- Questions and Approvals have distinct semantic card emphasis. Card selection opens Task Detail.
- Resume appears only when the server says it is available. Interrupt appears in the same action position only for exactly one interruptible live agent Session and acts immediately. Several live agent Sessions use Task Detail for per-Session control; scripts use the Task-wide action.
- Board states include Backlog, idle, queued, running, interrupted, Approval-blocked, Question-blocked, and done.
- Dragging a Backlog Task to its first executable Node starts it immediately without confirmation; that target says `Drag here to start automation`. A drop onto Done is a manual archive move, not normal Workflow completion.
- When an otherwise valid Start or executable Manual Move has unsatisfied Task
  Dependencies, Desktop opens dependency confirmation before Execution Target
  selection or another continuation dialog.
- The dependency-confirmation title is `Start task ahead of deps?`.
- The dependency-confirmation body is
  `This task has N unsatisfied dependencies. Do you still want to start it?`.
- Dependency confirmation has a corner Close control, outline `View deps`, and
  primary `Start`.
- Dependency confirmation does not list Blocker Tasks.
- Close and ordinary dialog dismissal leave the Task unchanged.
- `View deps` closes the confirmation and returns that result to the board. The
  board opens the Blocked Task's own Task Detail focused on Dependencies.
- `Start` closes the confirmation and returns one proceed intent to the board.
  The board applies that result through its existing start or move action path.
- Dismissing a later continuation leaves the Task unchanged and discards that
  proceed intent.
- Every manual workflow override requires confirmation. Submitting required values confirms a move that needs them; a move without required values uses a generic manual-override confirmation.
- When a Task has several Current Nodes, dragging any one card copy represents moving the whole Task. Dropping onto any Node that is already Current is a no-op.
- A Manual Move drop asks the server to evaluate that Task and destination without changing the Task. The board does not receive or retain a per-Task list of executable Manual Move destinations, and dragging over a destination makes no server request.
- Columns remain neutral while dragging. Red marks only destinations that available authoritative or structural facts already prove ineligible; the desktop does not predict eligible destinations before a drop.
- An ineligible drop makes no workflow change and shows a reason-specific Toast. Unexpected failures use the generic move-failure treatment.
- When exactly one usable incoming Transition contains the destination, Kent selects it automatically. When several are usable, the first dialog phase shows unselected radio choices using Transition labels; duplicate labels append their source Node names.
- The second dialog phase shows every required value as editable. Resolved values are prefilled and may be overridden; earlier-Node values show only their output field name and description.
- Advancing to the values or confirmation phase animates the content and dialog size, subject to reduced-motion preference. The dialog has no Back action; Cancel closes it, and choosing another Transition requires another drop.
- Selecting a Fan-Out Transition moves to every target Node. The dialog does not list sibling destinations, and dropping onto one fan-out member never starts that branch alone.
- Selecting an Approval-gated Transition in the Manual Move dialog acts as the Approval and does not open another Approval surface.
- Starting or manually moving to executable work opens Execution Target selection when the Workflow asks on first execution. Manual Move also opens it when its configured target is unavailable; its dialog closes before Execution Target selection opens. A usable fixed policy is not overridden.
- Execution Target selection offers no managed worktree, source `HEAD`, repository default branch, and custom Git ref, defaulting to repository default branch. An unavailable configured target explains the failure and preserves the useful prior selection and custom ref where possible.
- Closing Execution Target selection leaves the Task unchanged. Manual Move does not interrupt live work until required target selection succeeds. During Manual Move resolution or setup, preserve the selection, prevent duplicate submission, and keep actionable failure with Retry and Cancel in the same dialog.
- Desktop acknowledges Task Start after durable placement without waiting for preparation. Preparation failure uses the existing interrupted-Current-Node error display and Resume action. Resume opens the ordinary Execution Target selection flow when the interrupted Task is still unlocked.
- Board movement, Done permission, paging, status, Resume, and Interrupt follow server-authoritative live execution facts. The desktop never infers blockers from stored Task state.
- Submitting a Manual Move revalidates it. If the Task or Workflow changed while its dialog was open, the desktop uses the ordinary move error and provides no dedicated stale-preflight recovery flow.
- Invalid and default-Node-only Workflows remain visible with their Tasks. Invalid Workflows permit Backlog creation, editing where allowed, and comments, but disable drag, Start, Resume, manual move, and Done. Existing executable Nodes created under an earlier valid definition retain their server-provided Resume and Interrupt actions.
- A non-startable Backlog Task remains visible.
- Dragging near a board or hovered-column edge scrolls that surface with increasing speed. Horizontal and vertical scrolling can run together; horizontal takes priority if both cannot be reliable.

## Board Task Search

- A board has a `Search` chip immediately after the Labels chip. It uses the
  same visual treatment and height as the Labels chip.
- Selecting Search opens one centered command-palette dialog over a blurred
  backdrop. The dialog uses a frosted-glass surface.
- Opening Search animates the backdrop from unblurred to blurred. Its island
  fades in while moving upward by 30 pixels into place.
- Closing Search reverses that motion: the backdrop unblurs while the island
  fades and moves 30 pixels downward. Search remains mounted until the complete
  exit finishes. Search uses the fast motion duration. These motions are subject
  to reduced-motion preference.
- Search and its blurred backdrop appear above an open Task sidebar. Opening
  Search does not close or otherwise change the sidebar.
- The search input is an inline top row separated from the results by one thin
  divider. The input is not a nested island.
- The dialog focuses the input when it opens.
- `Command-S`, `Control-S`, and `Alt-Space` open Search from a board. These
  shortcuts suppress their platform or browser default action while the board
  is open.
- Search uses the existing case-insensitive literal Task Search contract.
- Search includes Task titles, complete bodies, and Comments.
- Search is scoped to the board's Project and spans every Workflow linked to
  that Project.
- Desktop submits a nonblank searchable query 300 milliseconds after the last
  edit.
- A blank query, a literal query without a searchable trigram, or a searchable
  query with no matching Tasks collapses the dialog to the Search field without
  explanatory copy.
- Initial Search loading expands a centered loading indicator beneath the
  input. Replacement loading keeps prior results visible without adding a
  loading indicator to the input.
- The dialog grows downward from a stable top edge as results appear and
  shrinks back to the Search field as they disappear. Size changes animate
  subject to reduced-motion preference.
- While a replacement query is debouncing or loading, the prior results remain
  visible and usable.
- Desktop retains one search query in process memory across Projects. Opening a
  different Project reruns that query with the new Project scope. Restarting
  Desktop clears the query.
- Closing and reopening Search retains the selected Task group while its result
  set remains current.
- Results use infinite scroll and preserve the server's Task grouping, hit
  pagination, and ordering. Desktop does not deduplicate repeated Task groups
  across pages.
- Each selectable result represents one returned Task group. It shows a status
  icon, Task Short ID, and title.
- Search uses the same status-icon mapping and semantics as related-Task rows
  in Task Dependencies. The status icon appears immediately before Task Short
  ID.
- Hovering a Search status icon shows its expanded localized status name.
- Task Search does not return Node or column display names. Desktop uses the
  localized status kind, such as `Done` or `Running`, and does not derive a
  display name from Node IDs.
- Task Short ID uses the ordinary foreground color. The title uses the same
  typographic hierarchy as a Task card.
- A result previews at most the first three hits in their server-provided
  order. Desktop does not rerank hits.
- Each hit preview shows the server-provided matching fragment and emphasizes
  the matching text. It does not show a text source-kind label or a general
  horizontal inset.
- Comment-hit previews use a message-bubble icon in the same muted foreground
  color as the surrounding hit text. They have no connector bar or additional
  horizontal inset.
- When the Task has undisplayed hits after the last preview, the result shows a
  plain muted `…N more hits` line using the server-provided total hit count and
  hit ordinals.
- When a result set arrives, its first Task group is selected unless a retained
  selection still identifies a group in that result set.
- Up Arrow and Down Arrow move selection without moving focus from the input.
- Actual pointer movement over a result moves selection without moving input
  focus or scrolling the result list. Results moving beneath a stationary
  pointer do not change selection.
- Arrow navigation preserves the list position while the selected result
  remains visible. When selection crosses a visible edge, the list scrolls only
  enough to reveal that result and animates the scroll smoothly, subject to
  reduced-motion preference.
- At the last loaded result, Down Arrow is a no-op until another result arrives.
  At either loaded boundary, repeated navigation never wraps or resets
  selection to the first result.
- Selecting a row with the pointer or pressing Enter for the selected row
  closes Search and opens that Task in the board's Task Detail sidebar.
- Escape and backdrop selection close Search without opening a Task.

## Labels

- Boards have one transparent filter row. It provides Labels and Unblocked filters, but no status, attention, column, or sort filter.
- The Unblocked filter uses a two-state chip labeled `Unblocked`. Its inactive state applies no dependency restriction. Its selected state includes only Tasks with no direct Task Dependencies or no unsatisfied direct Task Dependencies.
- The Unblocked chip uses the same styling and padding as the other filter chips. It appears after the other filters and before search.
- The Unblocked filter applies to every board column and every column count. It combines with the Labels filter so a shown Task must satisfy both active filters.
- The Unblocked selection belongs only to the current board route. Desktop resets it when the operator leaves the board or selects another Project or Workflow. Desktop does not persist it across relaunches.
- Unblocked filter changes apply immediately through server filtering. Existing cards and counts remain visible until their corresponding replacement arrives, and each authoritative result applies as it arrives. If a replacement fails, Desktop retains the affected prior result and shows a persistent Retry error.
- The trigger says `Labels` with no filter, `Labels · N` with named Label conditions, and `No labels` for the unlabeled filter. N counts included and excluded Label conditions. A clear action appears only for an active filter.
- One Label filter and its OR/AND mode apply to every board in a Project and persist for that Project in the desktop installation. They are not shared with other clients. OR is the default.
- Board Label filtering uses the shared Label-expression semantics in the Workflow orchestration specification.
- `No labels` clears all named Label conditions and disables OR/AND. Selecting it again removes that filter without losing the remembered named mode. Selecting a Label clears `No labels` and restores the remembered mode. Clear removes the filter and restores OR.
- Filter changes apply immediately through server filtering while the chooser remains open. There is no Apply step. Existing cards remain visible without a replacement loading state until new content arrives. Active filters change each column count to the matching Task count.
- Deleting a participating Label removes its included or excluded condition from the saved filter. Removing the last named Label condition clears the named restriction; deleting another Label does not change an active `No labels` filter.
- One chooser manages filtering, Task Label assignment, and Label creation, renaming, and deletion. There is no separate Project Label page.
- Search is case-insensitive substring matching and preserves the Project's manual Label sequence. While search has text, the chooser hides reorder handles and does not permit reordering. When no exact case-insensitive name exists, offer `Create “…”`.
- A Project permits at most 100 Labels. At the limit, search and selection remain available and creation explains its unavailability; deletion restores creation.
- The chooser shows at most 10 scrolling result rows, keeps search and context controls visible, remains open through selection and management actions, and discards an uncommitted rename on close.
- In board filtering, activating a named Label row cycles from neutral to included, from included to excluded, and from excluded to neutral. Included shows a green checkmark. Excluded shows a red X. Neutral shows neither state icon. A Label created from the filter chooser remains neutral.
- In the board filter chooser, `No labels` remains fixed before the Project Labels and has no reorder handle. Each Project Label has a six-dot reorder handle when at least two Project Labels exist.
- Only the reorder handle starts a drag. Pointer dragging scrolls the result list near its vertical edges. Keyboard reordering keeps the destination in view and supports start, movement, drop, and cancellation.
- Dragging previews the requested sequence. Dropping persists it once. The chooser immediately projects the requested sequence, disables Label catalog mutation controls and reorder handles while saving, adopts the authoritative response on success, and reloads the catalog with a reorder failure notification on failure.
- While create, rename, delete, or reorder is pending in an open chooser, Label selection remains available but that chooser's Label catalog mutation controls and reorder handles are unavailable. Separate choosers and windows do not coordinate their requests.
- Rename edits in place and can be committed or cancelled; validation failures remain inline. Deleting a Label requires confirmation and removes it from all Tasks.
- Assignment omits OR/AND and `No labels` and keeps binary row selection. It otherwise has the same chooser search and Label-management behavior. A Label created from an assignment chooser appears and becomes selected after creation succeeds. Labels are neutral chips ordered by the Project's manual Label sequence in the chooser, Task Detail, and board cards.
- Board cards show fitting complete Labels in their footer and replace the last fitting position with `+N` when needed. Task Detail places Labels directly after Task ID; the entire Label value opens the chooser.
- A board card lays out its dependency-progress chip before Label chips. Labels
  use only the remaining width and retain their existing `+N` behavior.
- Task Label assignments can change in every Task state. Assignment changes update immediately, then adopt the server result; failures restore the prior state and show a persistent Retry error.

## Tasks

- New Task requires title and accepts optional body, Project Labels, hidden source information, and source workspace. Workflow selection is outside the form. The workspace defaults to the opened workspace or Project default workspace. With one workspace, selection is shown but unavailable.
- Selected existing Labels are assigned atomically with Task creation. Creating a Label during Task creation selects it after Label creation succeeds; Create Task is unavailable while Label creation is pending. The Label remains in the Project if Task creation is cancelled or fails.
- Creation makes a Backlog Task; it does not start automation. Title, body, and source workspace are editable only in Backlog. A managed Execution Target remains tied to its original source workspace; no-managed-worktree execution uses the Task's current source workspace.
- Task creation and editing show server validation errors.
- Task Detail can appear inline, in a separate window when supported, or as a standalone destination. Reopening an already separate Task Detail focuses it rather than duplicating it. Closing it after a mutation refreshes visible content.
- Long descriptions start collapsed only when they overflow, at roughly half the available height and never fewer than about five or more than about ten rendered lines, with an expand action. Expansion lasts until that Task Detail closes, keeps the description top anchored, grows downward, and occurs automatically for editing.
- A Markdown task-list item uses one product-styled checkbox in place of its list bullet.
- Selecting a checkbox in an editable Task description updates the local Markdown body Draft without saving it. The existing Task Save action persists the changed body.
- From an editable Task description, the text-field submission shortcut must save the current Task title and body together.
- From an editable Task description, the text-field submission shortcut must close description editing.
- Task Detail begins with Inbox, which contains current blockers and answer, Approval, and Resume controls. Comments have composer, list, edit, delete, and count. There is no completed Workflow movement or execution-history view.
- Task Detail shows Task Short ID, title, Markdown body, Project, Workflow, source workspace name and root, all Current Nodes and states, completion state, and available actions including Task Delete. When available, it also shows Execution Target, managed worktree, requested revision, resolved commit, branch, Agent role and execution state, Session identity, source URL, and assignee or column.
- Source root and Execution Root are not separate facts. Unavailable expected facts are hidden; useful continuity facts may be empty or unassigned; unexpected meaningful absence is an unavailable or error state. Unavailable managed worktrees have no managed-worktree fact.
- Visible values copy by selecting the value itself, with clipboard feedback that identifies the copied value on success and includes the error on failure. Short commit display copies the complete commit. Actions that copy deliberately hidden content remain explicit controls.
- Source URL is read-only. Valid web, secure web, and mail links use their host as the label and open externally; other values are plain source text.
- Core Task Detail, Task attention, and comments load independently. Attention has its own loading and retry state and never blocks core detail. Opening from Inbox focuses its requested attention item once available. Live server changes update open Task Detail without replacing unsaved title or body edits or collapsing the surface.
- A non-attention Task Detail failure uses the standard error state; reopening or refreshing Task Detail is its recovery path. Deleted comments are hidden.

## Task Dependencies

- Desktop follows the Task Dependency behavior in the Workflow orchestration
  specification.
- Task Detail places one flat Dependencies area after the description and
  metadata islands.
- The Dependencies header shows the exact dependency-progress chip used by Task
  cards.
- The Dependencies header omits the chip when the Task has no direct Blocker
  Tasks.
- Dependencies contains `Blocked by` first and `Blocks` second, separated by
  one divider.
- Each subsection header is a separate row and includes its relationship count
  and an icon-only Add control.
- Add uses the circular icon-control presentation.
- Empty subsections remain visible so their Add controls remain reachable.
- The `Blocked by` and `Blocks` lists are complete and do not paginate.
- Each related-Task row shows one status icon, Task Short ID, and one-line title.
- A related-Task title truncates with an end ellipsis through layout. Desktop
  does not shorten or rewrite the source title.
- A related-Task row does not show its Workflow name.
- Related-Task rows put Tasks whose status is not `done` first, then order each
  group by Task Short ID.
- `done` uses a success-colored circular checkmark.
- `backlog` uses an empty foreground-colored circle.
- `active` uses a static primary-colored progress circle.
- `queued` and `running` use a spinner.
- `waiting_approval` and `interrupted` use a secondary-colored circle.
- `waiting_question` uses a primary-colored circle.
- Selecting a related-Task row replaces the current Task Detail with that Task
  in the same sidebar or Task Detail presentation.
- Dependency navigation has no sidebar-local Back or Forward action.
- Closing Task Detail after dependency navigation closes the current Task Detail
  as usual.
- Each relationship row has an accessible trailing Remove action rendered as a
  minimal uncircled red `X`.
- Remove acts immediately without confirmation.
- Dependency actions use icon-only controls outside confirmation dialogs.
- `Blocked by` Add opens the ordinary New Task form for a new Blocker Task.
- `Blocks` Add opens the ordinary New Task form for a new Blocked Task.
- Related-Task creation uses the open Task's Project and Workflow.
- Related-Task creation defaults source workspace to the open Task's source
  workspace and keeps the ordinary source-workspace selector available.
- The New Task form shows no dependency field, picker, relationship copy, or
  other dependency-specific visible state.
- Submitting related-Task creation atomically creates the Backlog Task and the
  directed Task Dependency.
- A related-Task creation failure creates neither Task nor relationship and
  preserves the ordinary New Task recovery path.
- Canceling related-Task creation creates neither Task nor relationship.
- Desktop has no existing-Task dependency picker.
- The Add control is unavailable with an accessible explanation when its
  relationship direction has reached the 50-Task limit.
- Kent rechecks the limit when related-Task creation is submitted.
- Dependency changes refresh open Task Detail and visible board cards from
  server-authoritative state.

## Inbox, Questions, Approvals, And Notifications

- Inbox lists the global infinite-scrolling attention feed. Task Detail owns Question and Approval actions through its bounded Task attention view.
- Inbox-opened Task Detail can move through the live Inbox order with Previous and Next. After resolution removes the open Task, Next advances to the replacement item. These controls are unavailable outside Inbox.
- The top Task Detail action opens or focuses the highest-priority unresolved attention. All unresolved items retain inline controls.
- Home Inbox shows only task-scoped attention. It has no Workflow-validity badge or section.
- Questions support suggestions, freeform commentary, recommendations, pointer or keyboard selection, and ordinary focus navigation. An option Question selects its valid recommended option by default and otherwise selects option 1; malformed recommendation metadata follows the same option-1 fallback. Live refresh preserves the user's selection and draft. Selection and recommendation remain distinct states. A Question with no suggestions has only freeform response and does not offer `Neither`.
- Runtime Approvals use the actual prompt, approval-specific choices, select the one-time allow choice when offered and otherwise the first offered choice, and do not offer `Neither`; Deny requires commentary. Workflow transition approval offers only Approve and shows source Node, Transition Key and label, target Nodes, required values, commentary, Workflow Version, and stale warning.
- Approving a transition that first needs an Execution Target uses the same selection dialog. Dismissing it leaves Approval pending. Interrupt acts immediately without confirmation.
- Task Detail opened from Inbox remains open after resolution while Inbox updates in the background.
- Desktop attention includes workflow Questions, workflow Approvals, ordinary Session Questions and Approvals with a Desktop response surface, and executable Nodes interrupted by errors. It excludes user interruptions.
- A focused window shows a persistent in-app attention notification. Selecting it opens Task Detail or Session Chat at the relevant attention item. Every notification can be closed regardless of duration. An ordinary Session prompt does not show a duplicate notification while its owning Session Chat and picker are already the focused destination.
- An unfocused window attempts a system notification when available. Activation focuses the main window and opens the matching Task Detail or Session Chat target. Question notifications select the first unresolved Question in their batch; Approval and error-interruption notifications select their matching item.
- Notification batches are server-authoritative. Resolved notifications dismiss matching persistent in-app notifications. Notification delivery never changes Workflow state; development failures fail immediately with diagnostics, while production continues without delivery.
- Notification messages are optional structured context. Desktop owns and localizes fallback copy when a message is absent; the server never supplies fallback UI copy.

## Logs

- The desktop log is stored under the Kent persistence root and is limited to 10 MB.
- By default, logs redact authentication headers, tokens, environment values, and request bodies.
