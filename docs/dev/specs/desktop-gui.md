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
- Text input is plain multiline Markdown. Rich Markdown preserves every source newline as a visible line break. Rich Markdown remains within its available surface width; only a code block may scroll horizontally inside its own block. Task Detail and Workflow Editor content use the approved rich Markdown presentation with sanitized raw-HTML and link behavior. Board previews are flattened text previews: they strip Markdown formatting and raw HTML without rendering rich structure or controls, preserve readable text labels, and remain bounded for dense boards. Completed supported code is syntax-highlighted and selectable in rich content; incomplete code remains selectable plain text.
- Task Description and Goal objective use one shared large Markdown field. Desktop does not maintain feature-specific copies of its read or edit presentation.
- The shared Markdown field has one base read-and-edit presentation and one optional collapsible presentation. The collapsible presentation adds overflow detection, a fade, and an accessible Expand action without creating another Markdown editor.
- Each destination configures the editor's minimum height and the collapsible presentation's height clamp.
- The destination owns the field's Markdown Draft, editing state, expanded state, dirty-Draft reconciliation, and server mutation. The field reports user editing, expansion, Draft-change, and submission intents without saving.
- The destination supplies the field's accessible label, placeholder, disabled state, and mutation error. The field presents no built-in visible label, associates a supplied error with the editor, and contains no Task or Goal copy.
- In enabled read mode, the field renders selectable rich Markdown. When the destination enables task-list interaction, selecting a Markdown task-list checkbox updates the destination-owned Draft without saving it.
- A plain click or tap with no text selection enters editing. Dragging selects rendered Markdown without editing. Links and task-list checkboxes perform their own actions. Keyboard focus keeps the rendered presentation, and Enter or Space enters editing.
- In disabled mode, the field renders read-only rich Markdown or its empty placeholder. It offers no editing focus or task-list interaction, and it does not change destination-owned presentation state.
- Leaving the active editor returns the field to rendered Markdown without discarding its Draft.
- When focus is in a Desktop text field outside the Workflow editor, Command+Enter on macOS and Ctrl+Enter on Windows or Linux must invoke that field's existing submit, save, or selection action. The shortcut must follow the same validation, disabled state, and confirmation behavior as that action.
- The shortcut must do nothing when the focused text field has no existing submission action.
- The shortcut must not change the field's ordinary Enter behavior.
- Desktop uses localized user-facing text, accessible controls, standard compact loading, error, and empty states, and motion that respects reduced-motion preference. macOS, Linux, and browser presentation use a contrast fade for readable top chrome; Windows uses progressive blur without a darkening fade.
- Dialogs, popups, confirmation flows, and dropdowns only collect an operator
  result. They close before returning that result to their parent destination.
  The parent destination owns navigation, server requests, pending state,
  failures, and retries through its existing action paths.
- Cards are reserved for board Task cards. Navigation, browsing, and selection collections use list rows.
- Workflow browsing rows show the Workflow name, description, version, and an Edit action. Selecting the row opens the Workflow editor. Edit opens Workflow settings without loading the Workflow graph.

## Home And Navigation

- Home opens on Inbox unless a valid previously open Project or Workflow destination can be restored. Back and forward use available navigation history; otherwise Back returns Home.
- Home has compact navigation and content side by side when both remain usable, otherwise navigation stacks above content. It has no resizable splitter or temporary navigation drawer.
- Navigation pins global Inbox above the infinite-scrolling Projects list. Inbox is not a Project and has no required attention badge.
- Selecting Inbox shows aggregate Inbox. Selecting a Project opens its workspace. When Desktop Chat is available, the workspace has `Tasks`, `Sessions`, and `Subagents` tabs. `Tasks` is the default. The selected tab remains local to the mounted Project workspace and resets when that workspace remounts.
- Switching among a Project workspace's `Tasks`, `Sessions`, and `Subagents` tabs crossfades overlapping outgoing and incoming content and respects reduced-motion preference.
- When Desktop Chat is unavailable, the Project workspace shows the Task list as its only selectable view. It omits the complete tab bar and reserves no tab-bar space.
- Selecting an Inbox item leaves Inbox visible and opens Task Detail as an overlay. It does not replace Home content or navigate away from Home.
- An Inbox item shows at most five lines of its detail message before truncation. Selecting the item still opens its complete Task Detail.
- Home opens sidebars by shifting content only when the measured main pane can retain at least 400 pixels after reserving the sidebar's preferred width, and as an overlay otherwise. Shift-mode sidebar opening, closing, and resizing keep the sidebar and Home content movement synchronized.
- When Desktop Chat is available, the sticky Project workspace header shows Project name and key, the three tabs, and `Link workflow` or `New Session` as appropriate.
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
- Changing the default workspace or attaching or detaching a workspace applies immediately.
- Workspace catalogs use infinite scroll, contain at most 100 entries per request, retain a bounded page window, show the default first, and then use newest attachment first.
- A workspace-catalog page-edge failure retains loaded rows and offers Retry at that edge. A first-page failure uses the standard retryable error state.
- Raw Project Settings catalog pages are not deduplicated or reconciled. Workspace mutations do not refresh retained pages, which may overlap or remain stale.
- A workspace row shows the existing shortened-path presentation, default status, and unlink action. Choosing an already attached path focuses its existing row or gives equivalent feedback.
- Choosing an already attached path outside the retained pages keeps the current list and scroll position and shows success-style feedback without adding or finding its row.
- Project Settings loads Project metadata and the Workspace catalog independently.
- Project Settings metadata contains no Workspace rows or Workspace pagination. Project Settings and New Task obtain Workspace rows from the same Project Workspace catalog.
- A Workspace-catalog first-page failure leaves Project name and Project Key editable and saveable, keeps Attach available, and gives the Workspace area its own Retry state.
- A recoverable Project-metadata failure gives the metadata area its own Retry state while loaded Workspace attach, default, and unlink actions remain available. A missing Project retains the Back behavior.
- Unlink confirmation explains that files remain on disk, retained Sessions remain readable, and active work blocks removal. It requires no typed confirmation.
- Project-scoped New Task uses the only linked Workflow when exactly one is linked, regardless of whether it is the Project default. With two or more linked Workflows, it uses the linked default Workflow. With no linked Workflow, or with two or more and no linked default, New Task is unavailable and directs the operator to Link Workflow. An invalid selected Workflow remains visible and permits Backlog Task creation.

## Project Task List

- The Project Task list is the unified Project-wide view of Tasks across every linked Workflow. The Home Project workspace's `Tasks` tab and the standalone Project Tasks destination use the same surface.
- The Workflow chip strip remains above the list and includes every linked Workflow and `Link Workflow`. Selecting a Workflow opens its board.
- The Workflow chip strip owns its loading and error state independently from Task counts and rows. A Workflow-list delay or failure does not block already available Task-list content.
- The Task list uses the Project workspace backdrop directly without its own border, island backdrop, or custom surface color. It has no repeated title, action strip, search, filter, or sort controls.
- The fixed column order is Status, Short ID, Title, Dependencies, Labels, and Workflow. Users cannot resize, reorder, add, or remove columns.
- Every Task is one compact, single-line row with no divider or zebra striping. Hover, focus, press, and selection use rounded animated row treatments.
- Status uses a fixed-size icon column. Dependencies, ID, Workflow, and Labels reserve only the width required by the widest retained row, within bounded display limits. Their retained high-water widths do not shrink while bounded pages rotate. Title uses the remaining width and gives up width first.
- ID shows the complete Task Short ID when it fits. When space is constrained, start ellipsis preserves its numeric suffix. Its complete value remains available in a tooltip and to assistive technology.
- Title, Workflow name, and Labels do not wrap. Workflow name uses end ellipsis.
- Labels use the same fitting-chip and `+N` overflow presentation as board cards, with compact vertical padding that remains readable in a single-line row.
- A Task with direct Blocker Tasks shows the board's dependency-progress chip. A Task without direct Blocker Tasks leaves the Dependencies cell empty.
- The server supplies dependency progress and assigned Label display data in each bounded Task-list row. Desktop does not derive dependency progress or load a separate Project Label catalog to render rows.
- Each section status icon has the accessible name `Status` and opens a tooltip listing every canonical state icon and name without group subheadings or duplicate descriptions.
- Rows are grouped in the order `Active`, `Backlog`, and `Done`. Active contains Waiting for question, Waiting for approval, Interrupted, Running, Queued, and Active. Backlog contains Backlog. Done contains Done.
- Active starts expanded. Backlog and Done start collapsed. Disclosure state remains local while the Project workspace stays mounted and resets when it remounts.
- A group with zero Tasks is hidden. Each visible group header shows the server-supplied exact count, including while collapsed.
- A group header's vertically centered chevron, name, count, and remaining row area form its disclosure control. Its separately interactive, vertically centered status icon opens the shared flattened status legend without toggling disclosure.
- The Task list currently omits the global column header. Its column-header presentation remains available for a future product pass without reserving list space. Group headers scroll normally.
- The list is one virtualized scroll area. Workflow chips, exact group counts, and the first 25-Task page of every initially expanded group begin loading in parallel.
- Initial loading requests only each expanded group's first page. Infinite-scroll continuation becomes eligible only after the operator scrolls the Task list, then paginates each expanded group independently and retains only a bounded nearby page window.
- Collapsing a group unmounts its Task rows and releases its loaded pages. Expanding it starts loading its first bounded page again. Collapsed groups do not load Task rows.
- Each group requests Updated-descending order from the server and preserves the server's deterministic order for equal Updated timestamps. Desktop does not sort rows.
- The list surface renders while exact counts load, with the standard loading state in the row area. Expanded first pages do not wait for counts or Workflow chips before requesting. Once counts arrive, all non-empty group headers appear together and already loaded rows render immediately.
- An expanded group's first-page loading or failure boundary appears beneath that group header and retries only that group. Later page failures preserve loaded rows and use the standard retry boundary at the affected paging edge.
- During refresh, each group header retains its last exact count and loaded rows remain visible. Counts and rows may briefly disagree while separate bounded requests converge.
- Project updates refresh exact counts and retained bounded pages, move Tasks between groups, and restore Updated-descending order without a manual refresh action.
- Live refresh preserves the leading visible Task and its screen position when possible. If that Task becomes hidden, the list retains the nearest visible position and does not expand a group.
- An open Task Detail remains open when its Task moves off-screen or into a collapsed group. Live reordering does not scroll to keep the selected Task visible.
- The visual vertical scrollbar is hidden while wheel, trackpad, touch, and keyboard scrolling remain available. The Task list never scrolls horizontally.
- Responsive Task columns hide Workflow first, before otherwise-fitting Label chips collapse into their `+N` counter. Labels then hide, followed by Dependencies, as available width narrows. Title receives the remaining width and truncates; it hides only when less than seven characters would remain. Status and Short ID never hide. Group headers continue to span the full list width.
- Every Interrupted status icon offers Resume through the board's existing operation, including its pending, error, dependency-confirmation, and Execution Target continuation behavior. The server remains authoritative, and an unavailable Resume uses the ordinary failure treatment. Other status icons are informational. Activating Dependencies, ID, Title, or Workflow opens the Task's general Task Detail. The Dependencies chip does not focus the Dependencies section from this list.
- Activating Labels opens the existing Task Label assignment chooser without opening Task Detail. Successful assignment updates the row; pending and failure behavior follows the existing assignment flow.
- An open Task Detail or Label chooser gives its Task row the selected treatment. Only the most recently opened interaction shows selection. Closing a Label chooser restores an already-open Task Detail's selection when its row is visible.
- Task Detail uses the containing destination's sidebar mode. Home shifts content only when the measured main pane can retain at least 400 pixels after reserving the sidebar's preferred width; it overlays otherwise. An open sidebar keeps the mode chosen when it opened until it closes.
- A shifted sidebar cannot be widened far enough to reduce Home's main pane below 400 pixels. Resizing any sidebar below 400 pixels closes it completely instead of retaining a zero-width or negative-width pane.
- Opening Task Detail adds no route or browser-history entry. Closing it clears its selected treatment.
- The Task table exposes accessible grid, row, grid-cell, disclosure, selection, tooltip, and loading/error labels. Its hidden column-header presentation retains accessible labels for a future product pass. This release uses ordinary browser focus and activation behavior and adds no table-specific keyboard-navigation model.
- Leaving and returning to the Tasks tab while the same Project workspace remains mounted restores its vertical pixel offset. It does not load special pages or reconcile the former position by Task identity. Remounting resets this state.
- When exact counts show no Tasks, the list groups are replaced with an empty state.
- With no linked Workflows, the empty state's primary action is `Link Workflow`.
- With linked Workflows but no Tasks, the empty state says `No tasks yet`. Its primary action is `New Task` when exactly one Workflow is linked or when multiple are linked with a default; otherwise it is `Link Workflow`.
- A successful Link Workflow action opened from the Tasks empty state returns to Tasks rather than opening the Workflow board. Cancel uses the existing close behavior. Failure keeps the linking page open with its entered state and existing error behavior.
- A successful New Task action closes creation and refreshes the authoritative list. It preserves the current Backlog disclosure state and does not programmatically reveal the created Task or open Task Detail.
- Canceling New Task uses the existing close behavior. Creation failure keeps the form open with its Draft and existing error behavior.
- The Project Task list has no persistent New Task action outside its empty state.

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
- Board cards use infinite scroll in both directions, 25 cards per page, and retain at most three nearby pages per active column. Cards outside the nearby area release their loaded pages; returning starts at that column's first page in the selected order without changing its expanded state.
- Card bodies are previews, not full bodies: outer whitespace is removed, content is limited to 512 Unicode code points, and truncation is explicit. Only visible cards render Markdown previews. An ellipsis indicates either truncated content or insufficient card space.
- Questions and Approvals have distinct semantic card emphasis. Card selection opens Task Detail.
- Resume appears only when the server says it is available. For a Task queued
  by the automatic Agent concurrency limit, Resume promotes its queued Current
  Nodes into explicit admission and starts them immediately despite the limit.
  The board card renders that queued Resume action as a warning-colored stop
  sign with the tooltip `Waiting due to concurrency limits`; Task Detail keeps
  the ordinary Resume button. Interrupt appears in the same action position
  only for exactly one interruptible live agent Session and acts immediately.
  Several live agent Sessions use Task Detail for per-Session control; scripts
  use the Task-wide action.
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
- Closing Execution Target selection leaves the Task unchanged. Manual Move does not interrupt live work until required target selection succeeds. During Manual Move resolution or setup, preserve the complete Move input and whether it uses configured policy or an explicit target, prevent duplicate submission, and show one generic pending state rather than setup-attempt progress. An actual typed setup failure keeps its diagnostic and retained worktree in the originating dialog with Retry current target, Choose another Execution Target, and Cancel. Other Manual Move failures use the ordinary error surface.
- Desktop acknowledges Task Start after durable placement without waiting for preparation. [Workflow Orchestration](workflow-orchestration.md#execution-targets-and-worktrees) owns setup-recovery timing and actions: Desktop focuses the exact canonical setup interruption, gives only that item the Resume recovery control, and leaves sibling setup interruptions informational.
- Board movement, Done permission, paging, status, Resume, and Interrupt follow server-authoritative live execution facts. The desktop never infers blockers from stored Task state.
- Submitting a Manual Move revalidates it. If the Task or Workflow changed while its dialog was open, the desktop uses the ordinary move error and provides no dedicated stale-preflight recovery flow.
- Invalid and default-Node-only Workflows remain visible with their Tasks. Invalid Workflows permit Backlog creation, editing where allowed, and comments, but disable drag, Start, Resume, manual move, and Done. Existing executable Nodes created under an earlier valid definition retain their server-provided Resume and Interrupt actions.
- A non-startable Backlog Task remains visible.
- Dragging near a board or hovered-column edge scrolls that surface with increasing speed. Horizontal and vertical scrolling can run together; horizontal takes priority if both cannot be reliable.

## Desktop Task Search

- The main window's application chrome always has a Search icon at the outer edge of its navigation controls. Search is the rightmost navigation control on macOS and the leftmost navigation control on other platforms. It is adjacent to the back and forward controls when they are visible.
- Selecting the application-chrome Search icon opens global Search from every ordinary page in the main window.
- `Command-S`, `Control-S`, and `Alt-Space` open global Search from every ordinary page in the main window. These shortcuts suppress their platform or browser default action.
- Global Search covers every Project.
- Separate native windows do not provide global Search.
- A board has a `Search` chip immediately after the Unblocked chip. It uses the same visual treatment and height as the Labels chip.
- The board Search chip opens Project-scoped Search that spans every Workflow linked to that Project.
- Selecting either Search entry point opens the single centered command-palette dialog over a blurred backdrop. The board entry point filters results to the current Project, while the application-chrome entry point searches every Project. The dialog uses a frosted-glass surface.
- Global Search applies no Project or status condition.
- Project-scoped Search applies only the current Project and no status or other condition.
- Opening either entry point while Search is open replaces the dialog's navigation scope instead of opening another dialog.
- Opening Search animates the backdrop from unblurred to blurred. Its island fades in while moving upward by 30 pixels into place.
- Closing Search reverses that motion: the backdrop unblurs while the island fades and moves 30 pixels downward. Search remains mounted until the complete exit finishes. Search uses the fast motion duration. These motions are subject to reduced-motion preference.
- Search and its blurred backdrop appear above the main content and an open Task sidebar. Opening Search does not close or otherwise change the sidebar.
- The search input is an inline top row separated from the results by one thin divider. The input is not a nested island.
- The dialog focuses the input when it opens.
- Search uses the existing case-insensitive literal Task Search contract.
- Search includes Task Short IDs, titles, complete bodies, and Comments.
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
- Desktop retains one search query in process memory across global and Project-scoped Search. Changing scope reruns that query within the selected scope. Restarting Desktop clears the query.
- Closing and reopening Search retains the selected Task group for that scope while its result set remains current.
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
- A result previews at most the first three returned non-Short-ID hits in their server-provided order. Desktop does not rerank hits.
- A returned Short ID hit uses the existing Task Short ID header without additional emphasis or a duplicate preview and does not consume a preview position.
- Desktop applies the preview allowance independently to each returned Task group, including a repeated group on a continuation page.
- Each hit preview shows the server-provided matching fragment and emphasizes
  the matching text. It does not show a text source-kind label or a general
  horizontal inset.
- Comment-hit previews use a message-bubble icon in the same muted foreground
  color as the surrounding hit text. They have no connector bar or additional
  horizontal inset.
- When the Task has undisplayed hits after the highest ordinal represented by the Short ID header or a preview, the result shows a plain muted `…N more hits` line using the server-provided total hit count and hit ordinals.
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
- Selecting a row with the pointer or pressing Enter for the selected row closes Search and opens that Task in the Task Detail sidebar.
- Escape and backdrop selection close Search without opening a Task.

## Labels And Board Sorting

- Boards have one transparent filter-and-sort row. Its controls appear in this order: `Labels`, `Sort`, `Unblocked`, `Search`. It has no status, attention, or column filter.
- `Sort` uses an icon followed by text and opens a popover styled like the Label chooser.
- The Sort popover lists `Updated`, `Created`, `Labels`, and `Short ID`, in that order. An `Asc`/`Desc` segmented selector controls the direction.
- The Sort popover has no Apply, Done, Clear, or Reset action. Sort changes apply immediately while the popover remains open, and changing the field retains the selected direction.
- The default is `Updated Desc`. At that default, the chip is neutral and says `Sort`.
- Any custom order makes the chip primary and changes its text to `Sort · Field · Asc` or `Sort · Field · Desc`.
- Each newly opened Workflow board starts at `Updated Desc`. Switching away and back or relaunching Desktop resets the sort.
- One selected sort applies inside every board column. Board field comparison and tie-breaking follow the Workflow orchestration specification.
- Label filtering, Unblocked filtering, and sorting never change each other's selected state. Every active board filter combines with logical AND before the server sorts.
- A sort change keeps rendered cards visible while replacement pages load. If replacement fails, Desktop keeps the selected sort and rendered cards and shows the existing retryable board or column error.
- A sort replacement keeps each column mounted and uses the board's existing card movement animation, subject to reduced-motion preference. Desktop makes a best effort to retain the visible position, but that position may move as replacement card heights settle or normal bounds clamp it.
- The Unblocked filter uses a two-state chip labeled `Unblocked`. Its inactive state applies no dependency restriction. Its selected state includes only Tasks with no direct Task Dependencies or no unsatisfied direct Task Dependencies.
- The Unblocked chip uses the same styling and padding as the other filter chips. It appears after the other filters and before search.
- The Unblocked filter applies to every board column and every column count.
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
- After a Project Label reorder succeeds locally or arrives from another client, active boards refresh their card pages and adopt the resulting server order while retaining rendered cards until replacement pages arrive.
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

- New Task requires title and accepts optional body, Project Labels, hidden source information, and source workspace. Workflow selection is outside the form.
- The source-workspace selector uses the Project Workspace catalog with infinite scroll and bounded page retention.
- Source-workspace options keep the existing Workspace-name presentation.
- The source workspace defaults to the opened attached Workspace or Project default Workspace. A detached initiating Workspace is omitted and the Project default is selected.
- An attached initiating Workspace outside the retained pages remains one pinned option for the dialog lifetime and appears before loaded rows.
- The selector presents one option per Workspace identity even when raw catalog pages overlap.
- If page retention discards the selected Workspace's page, the selector retains one compact selected-Workspace summary. A loaded row for the same Workspace replaces that summary.
- While an initiating Workspace read is pending, Desktop does not automatically select the catalog default.
- If the initiating Workspace read fails, Desktop keeps that failure distinct from detachment, offers an independent Retry, preserves loaded catalog pages and form Drafts, and does not automatically select the catalog default.
- Retrying the initiating Workspace read does not restart the Workspace catalog.
- An explicit source-Workspace selection commits immediately and a later initiating Workspace result or Retry does not replace it.
- With one workspace, source-workspace selection is shown but unavailable.
- Selected existing Labels are assigned atomically with Task creation. Creating a Label during Task creation selects it after Label creation succeeds; Create Task is unavailable while Label creation is pending. The Label remains in the Project if Task creation is cancelled or fails.
- Creation makes a Backlog Task; it does not start automation. Title, body, and source workspace are editable only in Backlog. A managed Execution Target remains tied to its original source workspace; no-managed-worktree execution uses the Task's current source workspace.
- Task creation and editing show server validation errors.
- Task Detail can appear inline, in a separate window when supported, or as a standalone destination. Reopening an already separate Task Detail focuses it rather than duplicating it. Closing it after a mutation refreshes visible content.
- On wide layouts that place Description and Metadata side by side, both islands have exactly the same height. Their shared height equals the larger intrinsic island height.
- Side-by-side Description and Metadata islands use the same surface elevation and render their complete shadows without clipping.
- Metadata property labels use one consistent font size, line height, weight, and foreground treatment. Value typography may still communicate value semantics such as code, status, or muted secondary information.
- Task Description uses the shared collapsible large Markdown field.
- Long descriptions start collapsed only when they overflow, at roughly half the available height and never fewer than about five or more than about ten rendered lines, with an expand action. Expansion lasts until that Task Detail closes, keeps the description top anchored, grows downward, and occurs automatically for editing.
- A Markdown task-list item uses one product-styled checkbox in place of its list bullet.
- Selecting a checkbox in an editable Task description updates the local Markdown body Draft without saving it. The existing Task Save action persists the changed body.
- From an editable Task description with a valid title and a dirty Draft, the text-field submission shortcut must submit the current Task title and body together.
- From an editable Task description with a clean Draft, the text-field submission shortcut must close description editing without a Task mutation.
- While a dirty Task Description Save is pending, editing must remain active and the Draft must remain complete.
- A successful dirty Task Description Save must close editing after the server operation completes.
- A failed dirty Task Description Save must keep editing active, preserve the complete Draft, and present the mutation error.
- Task Detail begins with Inbox, which contains current blockers and answer, Approval, and Resume controls. Comments have composer, list, edit, delete, and count. There is no completed Workflow movement or execution-history view.
- Task Detail shows Task Short ID, title, Markdown body, Project, Workflow, source workspace name and root, all Current Nodes and states, completion state, and available actions including Task Delete. When available, it also shows Execution Target, managed worktree, requested revision, resolved commit, branch, Agent role and execution state, Session identity, source URL, and assignee or column.
- Source root and Execution Root are not separate facts. Unavailable expected facts are hidden; useful continuity facts may be empty or unassigned; unexpected meaningful absence is an unavailable or error state. Unavailable managed worktrees have no managed-worktree fact.
- Visible values copy by selecting the value itself, with clipboard feedback that identifies the copied value on success and includes the error on failure. Short commit display copies the complete commit. Actions that copy deliberately hidden content remain explicit controls.
- Source URL is read-only. Valid web, secure web, and mail links use their host as the label and open externally; other values are plain source text.
- Core Task Detail, Task attention, Comments, and Activity load independently. Comments and Activity start their first page asynchronously and in parallel without blocking other Task Detail content. Attention has its own loading and retry state and never blocks core detail. Opening from Inbox focuses its requested attention item once available. Live server changes update open Task Detail without replacing unsaved title or body edits or collapsing the surface.
- Comments and Activity use 50 rows per page, newest first, and retain at most 10 nearby pages per feed.
- Each feed uses edge-driven infinite scroll in both directions. Loading beyond the page budget evicts the farthest page on the opposite side, and returning to an evicted side reloads it.
- Live feed changes refresh the retained bounded pages. Desktop keeps the visible row steady when it remains loaded and otherwise uses the nearest loaded position.
- A page-edge failure retains loaded rows and offers Retry at that edge. A first-page failure and an empty feed use the standard feed error and empty states.
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
- `waiting_question` uses a primary-colored question-mark circle.
- The Desktop sidebar owns a navigation stack for sidebar-local movement.
- Opening a root sidebar destination replaces the prior sidebar stack.
- Selecting a related Task pushes Task Detail onto the sidebar stack.
- Selecting a Task already retained in the stack returns to that Task and discards every later destination.
- Dependency Add opens a compact picker with project-scoped full Task search.
- The picker places a New Task plus action beside and after its search field, with compact existing-Task results below.
- The picker smoothly morphs its height as search results and state feedback appear or disappear, subject to reduced-motion preference.
- Selecting New Task pushes the ordinary New Task form with the relationship intent hidden and preconfigured.
- Selecting an existing Task adds it in the chosen `Blocked by` or `Blocks` direction without opening Task Detail.
- The picker excludes the open Task and Tasks already related to it.
- Successful related-Task creation atomically creates the Task and relationship, then replaces New Task with the created Task Detail.
- Back returns to the preceding sidebar destination.
- Back is hidden at the root.
- X closes the complete sidebar stack.
- The sidebar has no Forward action.
- The sidebar retains at most 50 destinations.
- A push beyond the limit preserves the root and evicts the oldest non-root destination.
- Only the current destination remains mounted and live.
- Back restores Task Detail scroll position, description expansion, selected Comments or Activity tab, unsaved Task title and body edits, unsaved new-comment text, and one edited-comment draft.
- Restored Task Detail refreshes server-authoritative data and layers retained unsaved input over it.
- Inactive Task Detail data follows the ordinary Desktop query-cache lifetime.
- A mounted Task or Project destination that receives a typed missing result goes Back, including closing when it is the root.
- Missing retained destinations are skipped lazily when Back reveals them.
- Leaving the Desktop screen that owns a root sidebar closes that root unless another root has replaced it.
- Completion from a replaced destination does not close or change the replacement sidebar.
- Related New Task disables header Back and X only while its atomic creation request is pending.
- Ordinary New Task keeps header Back and X available while its request is pending.
- A failed related creation restores header Back and X and preserves the form recovery path.
- Successful Project deletion from Project Edit closes that mounted Project Edit sidebar.
- Scroll restoration reuses bounded cached feed pages while the ordinary query cache retains them, refreshes them asynchronously, and resumes at the nearest available loaded pixel position. A cold cache opens the newest page. Restoration never requests additional pages solely to reach the prior offset, and ordinary edge-driven loading continues from there.
- Inbox Previous and Next replace the current Inbox Task without adding sidebar history.
- Related-Task navigation and Dependency Add are unavailable while a Task or comment save is pending.
- Relationship Remove keeps its independent availability while another Task Detail save is pending.
- Pop out opens only the current Task and closes its originating sidebar after the separate window opens.
- A pop-out completion from a replaced destination leaves the replacement sidebar open.
- Each relationship row has an accessible trailing Remove action rendered as a
  minimal uncircled red `X`.
- Remove acts immediately without confirmation.
- Dependency actions use icon-only controls outside confirmation dialogs.
- The `Blocked by` picker's New Task action opens the ordinary New Task form for a new Blocker Task.
- The `Blocks` picker's New Task action opens the ordinary New Task form for a new Blocked Task.
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
- The Add control is unavailable with an accessible explanation when its
  relationship direction has reached the 50-Task limit.
- Kent rechecks the limit when related-Task creation is submitted.
- Dependency changes refresh open Task Detail and visible board cards from
  server-authoritative state.

## Inbox, Questions, Approvals, And Notifications

- Inbox lists the global infinite-scrolling attention feed. Task Detail owns Question and Approval actions through its bounded Task attention view.
- Inbox-opened Task Detail can move through the live Inbox order with Previous and Next. After resolution removes the open Task, Next advances to the replacement item. These controls are unavailable outside Inbox.
- The top Task Detail action opens or focuses the highest-priority unresolved attention. Every unresolved item not awaiting an answer result retains its applicable inline controls; sibling setup interruptions represented by a canonical recovery item are informational as defined by [Workflow Orchestration](workflow-orchestration.md#execution-targets-and-worktrees).
- Home Inbox shows only task-scoped attention. It has no Workflow-validity badge or section.
- Questions support suggestions, freeform commentary, recommendations, pointer or keyboard selection, and ordinary focus navigation. An option Question selects its valid recommended option by default and otherwise selects option 1; malformed recommendation metadata follows the same option-1 fallback. Live refresh preserves the user's selection and draft. Selection and recommendation remain distinct states. A Question with no suggestions has only freeform response and does not offer `Neither`.
- Runtime Approvals use the actual prompt, approval-specific choices, select the one-time allow choice when offered and otherwise the first offered choice, and do not offer `Neither`; Deny requires commentary. Workflow transition approval offers only Approve and shows source Node, Transition Key and label, target Nodes, required values, commentary, Workflow Version, and stale warning.
- Task Detail sends each `Submit answer` independently and does not collect responses across Questions or runtime Approvals.
- Selecting `Submit answer` removes that prompt from local attention before Kent reports the result.
- Task Detail moves focus to the next unresolved prompt's first answer control and keeps the attention list at the submitted prompt's position.
- The next prompt accepts edits and submission while earlier answer deliveries are in progress. Answer deliveries may finish in a different order.
- After every answer attempt settles, Task Detail refetches Task attention. It restores the submitted selection and commentary only if refreshed attention still contains the exact Session, Step, and prompt identity; otherwise it discards that answer state.
- If delivery fails while the same Task Detail is present and refreshed attention still contains the prompt, Task Detail restores it in server order, surfaces the failure, and permits manual retry without moving focus or scroll away from another prompt being edited.
- If the attention refetch fails, Task Detail restores the prompt from cached attention with its submitted selection and commentary, surfaces the reconciliation failure, and permits manual retry. A retry may report that the prompt was already resolved.
- Task Detail does not replay a failed answer automatically.
- If delivery fails after the operator leaves the originating Task Detail, Desktop discards the submitted answer state and identifies the Task in the failure notification. Reopening the Task uses server-provided defaults for an unresolved prompt.
- Leaving Task Detail discards unsubmitted Question and runtime Approval answer state.
- A prompt resolved by another client disappears without feedback.
- Task Detail does not offer Decline for Questions or runtime Approvals.
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
