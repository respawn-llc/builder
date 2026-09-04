# TUI Startup And Attach

## Startup Surfaces

- Startup surfaces use the Alternate Screen and leave it before Ongoing Mode begins. Ongoing transcript history therefore enters the Normal Buffer and terminal Scrollback.
- Startup surfaces enable Alternate Scroll while active and disable it on every exit path.
- Startup surfaces never enable Mouse Capture.
- Text fields use the native terminal cursor.
- Every asynchronous gate appears immediately with a loading state. Startup never leaves the terminal blank or frozen while it waits.
- The authentication picker and Session picker use the same status-line treatment for operation errors and notices. This also applies when `/resume` reopens the Session picker.
- Startup remembers the previous visible surface. `Esc` returns to it unless the active surface defines another key behavior.

## Startup Sequence

Ordered gates; each gate is skipped when its condition does not apply, never bypassed by flags:

1. **Server attach.** The TUI is a client and contains no server. It attaches to the configured endpoint. Explicit `server_host` and `server_port` values are authoritative. A connection failure exits with the endpoint and reason. Kent provides no embedded-server fallback or retry screen.
2. **Authentication.** This gate blocks only when the selected provider requires Kent-managed authentication.
3. **First-time setup.** After the first successful authentication, a missing `config.toml` opens setup before Session selection.
4. **Workspace resolution.** Startup begins from the current workspace. An unregistered current directory opens Project binding and is not registered automatically.
5. **Session selection.** If no Sessions exist, startup opens new-Session setup directly.
6. **Workspace-change prompt.** This prompt appears when the selected Session's available workspace root differs from the current root.
7. **Lazy open and handoff.** Kent creates and initializes the Session only when the first user message or another Agent Turn trigger requires it.

- `Esc` on a startup surface navigates back one gate where a previous gate exists; on the first visible surface it exits the TUI cleanly. The session picker is an explicit exception: its `Esc` key is a no-op.

## Auth Gate

- The picker offers browser OAuth, device-code OAuth, `No auth`, and environment-key adoption when available.
- Browser OAuth accepts a local callback or pasted callback URL or code.
- Environment keys are used only after the user selects them.
- OAuth failure does not fall back to an API key.
- `/login` and `/logout` reopen authentication selection without clearing credentials first.
- There is no env-vs-saved conflict prompt at startup: the last saved auth choice wins, including an explicit `No auth` choice. Env keys enter only through the chooser-backed adoption path.
- Successful auth shows a brief confirmation state on the auth surface before advancing to the next gate.
- Authentication failures and 401 responses appear on the authentication surface with retry or method-selection actions. They never cause a silent exit.

## Session Picker

- The picker shows recent Sessions and a new-Session action. Infinite Scroll provides older Sessions. An empty state opens new-Session setup.
- The picker has `Sessions` and `Subagents` tabs and opens on `Sessions`. Ordinary interactive sessions and interactive forks start in `Sessions`; sessions created for headless or workflow-agent execution start in `Subagents`. Every interactive open of a Subagent session, including opening by explicit session ID, permanently promotes it to `Sessions`. Picker selection alone does not promote: workspace lookup failure returns to the picker with a generic retry error in the shared status line, and declining a workspace change returns to the picker; neither changes the session artifact, recency, or category. An accepted retarget completes before open and promotion. Automated headless/workflow resumes and renames never change category. Sessions without a recorded category appear in `Sessions`; category is never guessed from a session's name, parent, or current activity.
- The tabs appear directly below the status header using the horizontal bracketed button-row treatment. The selected tab uses the primary bold treatment, the other tab is muted, and incremental lists do not show total counts.
- When both full labels do not fit horizontally, the same buttons stack on adjacent lines rather than clipping or changing labels.
- The picker opens immediately on `Sessions` and loads both tabs' first windows concurrently. A tab shows its own loading spinner until its first window arrives. If both first windows are empty, startup advances to new-session setup.
- The picker requests update status without delaying Session loading.
- Checking and up-to-date states add no row.
- An available update adds a persistent Success-colored `Update available: v<latest>` row below the current Kent version.
- A failed check adds a persistent Error-colored `Update check failed: <cause>` row in the same location.
- The row remains while the picker stays open and reappears while the update result remains fresh.
- Only the Session picker shows update status.
- The `Kent v...` title shows the local client version. Update discovery evaluates the attached server version.
- A tab data-load failure replaces that tab's list, including any stale rows, with an error notice because failed loading leaves no valid list to present. `Enter` retries the selected failed tab, and switching into a failed tab also retries. The shared startup status line separately surfaces the operation error: the active tab's outstanding failure wins, otherwise the newest outstanding tab failure is shown. Retry keeps the previous failure visible until that tab succeeds; recovery clears only that tab, reveals another outstanding failure when present, and clears the status after both recover.
- Retry performs a fresh load from that tab's newest window and resets its old pagination position, selection, and scroll.
- Each tab retains its selected row and scroll position while the picker remains open. Reopening the picker starts each tab at its newest session.
- `Create a new session` appears only in `Sessions`; `n` starts a new ordinary session from either tab.
- An empty `Subagents` tab remains selected and shows `No subagent sessions yet`; tab navigation and `n` remain available.
- When both tabs are empty, startup skips the picker and goes directly to new-session setup.
- Each tab requests 50 Sessions per page in recency order with infinite scroll and keeps at most two bounded pages resident. Traversal loads older pages and reloads evicted newer pages when navigating back; neither client nor server holds or requests the full session set.
- Initial load and fresh retry replace the affected tab body. Loading an older or newer offset page keeps resident rows, selection, and viewport visible, shows a loading affordance at the requested edge, and blocks only crossing that pending edge.
- Each row shows session title and relative age.
- Keys: `Up`/`Down` and `j`/`k` move selection; `PgUp`/`PgDn` page; `Tab`/`Shift+Tab`, `Left`/`Right`, and `h`/`l` switch tabs without opening a session; `Enter` opens; `n` starts a new session. `Esc` is a no-op, `q` has no binding, and `Ctrl+C` is the sole picker exit key.
- Opening a session seeds the next main input from the session's persisted draft verbatim (byte-for-byte, including whitespace and newlines).

## Project Binding Flow

- An unregistered current directory offers new-Project creation first and an existing-Project picker below.
- Creating a Project attaches the current workspace as its first workspace and main worktree.
- Selecting an existing Project attaches the workspace to it.
- The existing-Project Workspace picker uses the canonical Project Workspace catalog with infinite scroll. It preserves server order, shows Workspace name and path, and shows no activity timestamp.
- The Workspace picker requests 50 Workspaces per page and keeps at most two pages resident.
- When the selected Project has exactly one attached Workspace, startup continues without showing the Workspace picker.
- The Workspace picker appears immediately with a loading state.
- A first-page load failure replaces the list with a retry notice. `Enter` retries the failed request.
- Loading or failure at an older or newer edge keeps resident rows, selection, and viewport visible. The picker shows a spinner while loading or retry text after failure at the affected edge and blocks only movement across that edge.
- The picker prefetches an adjacent page when selection enters the first or last visible screenful of the resident window.
- Up/Down and `j`/`k` move by one row. PgUp/PgDn move by one visible screenful. Row movement across a loaded boundary selects the first row of the next page or the last row of the previous page. PgUp/PgDn movement across a loaded boundary lands one visible screenful into the loaded page.
- Adjacent-page loading or failure does not move selection. Loading adds no copy beyond the spinner. The shared startup status line shows the operation failure, and the affected list edge shows retry text.
- `Enter` retries a failed first-page request. When valid resident Workspaces remain visible after an adjacent-page failure, `Enter` selects the current Workspace and moving toward the failed edge retries that page automatically.
- A successful adjacent-page request with no rows leaves the resident information visible without an additional read or special status.
- A successful retry immediately clears that operation's failure cause and retry affordance, then shows the loaded rows or the normal catalog boundary.
- `Esc` returns to Project selection. Ctrl+C exits startup. The Workspace picker has no `q` binding.
- Workspace catalog loads use the shared client request lifecycle without a picker-specific timeout or automatic retry.
- An empty Workspace catalog shows `no workspace is attached to this project.` followed by `Please attach workspace before continuing.` Both lines use the normal foreground treatment without warning or error color. `Enter` reloads the catalog. `Esc` returns to Project selection.
- Returning from the Workspace picker preserves the Project picker's selection and scroll. Entering a Project's Workspace picker again starts a fresh first-page load.
- Server-browsing mode can open existing Projects and workspaces but cannot create or attach them.
- Kent supplies the Project-name suggestion from the workspace. The TUI does not inspect the filesystem to derive it.
- An empty Project name shows an inline error.

## Workspace-Change Prompt

- If a selected Session has an available attached workspace root different from the current root, startup shows `Workspace changed`.
- `Yes` retargets the Session. `No` returns to the picker.
- A detached workspace-location record cannot trigger or supply the retarget.
- Retargeting always requires explicit user action.

## Multi-Client Attach

- Opening a Session that another client uses gives this client Equal Full-Control Attach. Startup has no read-only or reduced-control mode.
- When an open Session moves to another Workspace, the TUI closes its current view and automatically reopens and rehydrates the same Session in its new location.

## Errors

- Preflight server-connection failure at TUI startup exits the TUI with an actionable error message (endpoint + reason). No retry affordance, no per-surface handling.
- After startup, connection loss shows one persistent `server connection lost` status-line notice across all surfaces.
- A successful request after reconnection clears the notice.
- If the TUI cannot preserve a coherent connection-loss state, it exits with a clear error.
- Any startup exit path (success, cancel, failure) restores the terminal best-effort: alt-screen exited, cursor restored, no residual control state. Ongoing-mode output already written to the normal buffer is permanent by design and is not restored.
- Debug mode fails fast with diagnostics on startup invariant violations. Normal operation surfaces the error and recovers or exits clearly.
