# TUI Startup And Attach

Covers interactive TUI launch end-to-end: process start → server attach → auth gate → first-time-setup check → workspace resolution → project binding → session selection → workspace-change prompt → lazy session open/attach → handoff to the main UI.

Excludes: onboarding wizard content (own spec), headless startup (core-runtime-tools :: Headless Mode), main-UI/ongoing behavior (ongoing-scrollback-buffer.md, tui-transcript.md).

Bullets marked (owner: …) restate decisions owned by another spec for one-place readability; the owner spec is authoritative for them.

## Surface Architecture

- The future Rust TUI composes all startup surfaces through the shared Ratatui render layer: typed layout, styled spans, chrome, focus/selection affordances. No hand-assembled string frames, no raw terminal-control/SGR escape strings.
- The shipping Go TUI keeps its existing startup rendering and lifecycle architecture. BUI-41 changes only the Go session picker and does not modify the Rust stub or migrate unrelated Go startup surfaces.
- Startup surfaces run in terminal alt-screen (`?1049`) and exit alt-screen before the main UI begins ongoing replay, so all ongoing history lands in the normal buffer and remains in native scrollback. This is consistent with tui-transcript's "Main UI startup stays in the normal buffer", which governs the main chat surface, not startup selection.
- Alt-screen startup surfaces enable alternate scroll (`?1007`) while active and disable it on every exit path; they never enable mouse capture. (owner: tui-terminal-environment :: Terminal Control Modes)
- Text entry on startup surfaces uses the native/hardware terminal cursor positioned by the render adapter; never a drawn cursor glyph.
- Startup surfaces do not enable terminal mouse capture.
- Every asynchronous gate renders its surface immediately with a loading affordance; startup never leaves the terminal blank or frozen while waiting on the server.
- The auth picker and session picker share one status-line treatment for operation errors and notices, including a session picker reopened by `/resume`; this requirement does not apply to other startup surfaces.
- The future Rust TUI startup surfaces form an explicit navigation stack with owned UI lifecycles (Compose/Decompose-style component model): surfaces are pushed/popped, `Esc` pops except where a surface owns a different explicit key contract, and navigation state is owned by the pure model layer, never by render widgets.

## Startup Sequence

Ordered gates; each gate is skipped when its condition does not apply, never bypassed by flags:

1. **Server attach.** The TUI is a pure client and embeds no server, like `kent run`. It attaches to the configured server endpoint; explicit `server_host`/`server_port` overrides stay authoritative. When the preflight connection fails, the TUI exits with an actionable error (endpoint + reason) — no embedded-server fallback, no retry UI.
2. **Auth gate.** Blocks only when the resolved provider path requires Kent-managed auth. (owner: core-runtime-tools :: Auth)
3. **First-time setup.** After first successful auth, missing `config.toml` triggers first-time setup before session selection. (owner: core-runtime-tools :: Configuration)
4. **Workspace resolution.** Workspace-first. Unregistered cwd enters the explicit post-auth binding flow; no auto-registration. (owner: core-runtime-tools :: Sessions And Persistence; tui-transcript :: Startup And Session Selection)
5. **Session selection** (or directly new-session setup when no sessions exist). (owner: tui-transcript :: Startup And Session Selection)
6. **Workspace-change prompt** when the picked session's stored root differs from the current root. (owner: tui-transcript :: Startup And Session Selection)
7. **Lazy open and handoff.** Session creation/initialization is lazy — first user message or loop trigger. (owner: core-runtime-tools :: Sessions And Persistence)

- `Esc` on a startup surface navigates back one gate where a previous gate exists; on the first visible surface it exits the TUI cleanly. The session picker is an explicit exception: its `Esc` key is a no-op.

## Auth Gate

- Picker exposes browser OAuth, device-code OAuth, `No auth`, and env-key adoption when available. (owner: core-runtime-tools :: Auth)
- `No auth` semantics, hybrid OAuth callback (local callback or pasted URL/code), env key as chooser-backed source, no OAuth→API-key auto-fallback, `/login`/`/logout` reopening selection without clearing credentials: all as locked. (owner: core-runtime-tools :: Auth)
- There is no env-vs-saved conflict prompt at startup: the last saved auth choice wins, including an explicit `No auth` choice. Env keys enter only through the chooser-backed adoption path.
- Successful auth shows a brief confirmation state on the auth surface before advancing to the next gate.
- Auth failures and 401s surface as actionable UX on the auth surface itself (retry/choose another method), never as a silent exit. (owner: core-runtime-tools :: Auth)

## Session Picker

- Shows recent sessions, pick-or-new; scrollable with no cap; empty state goes directly to new-session setup. (owner: tui-transcript :: Startup And Session Selection)
- The picker has `Sessions` and `Subagents` tabs and opens on `Sessions`. Ordinary interactive sessions and interactive forks start in `Sessions`; sessions created for headless or workflow-agent execution start in `Subagents`. Every interactive open of a Subagent session, including opening by explicit session ID, permanently promotes it to `Sessions`. Picker selection alone does not promote: workspace lookup failure or declining a workspace change returns to the picker without changing the session artifact, recency, or category; an accepted retarget completes before open and promotion. Automated headless/workflow resumes and renames never change category. Legacy sessions without a recorded category appear in `Sessions`; category is never guessed from a session's name, parent, or current activity.
- The tabs appear directly below the status header using the existing horizontal bracketed button-row treatment. The selected tab uses the primary bold treatment, the other tab is muted, a faint helper exposes the switching keys, and incremental lists do not show total counts.
- When both full labels do not fit horizontally, the same buttons stack on adjacent lines rather than clipping or changing labels.
- The picker opens immediately on `Sessions` and loads both tabs' first windows concurrently. A tab shows its own loading spinner until its first window arrives. If both first windows are empty, startup advances to new-session setup.
- A tab data-load failure replaces that tab's list, including any stale rows, with an error notice because failed loading leaves no valid list to present. `Enter` retries the selected failed tab, and switching into a failed tab also retries. The shared startup status line separately surfaces the operation error: the active tab's outstanding failure wins, otherwise the newest outstanding tab failure is shown. Retry keeps the previous failure visible until that tab succeeds; recovery clears only that tab, reveals another outstanding failure when present, and clears the status after both recover.
- Retry performs a fresh load from that tab's newest window and resets its old pagination position, selection, and scroll.
- Each tab retains its selected row and scroll position while the picker remains open. Reopening the picker starts each tab at its newest session.
- `Create a new session` appears only in `Sessions`; `n` starts a new ordinary session from either tab.
- An empty `Subagents` tab remains selected and shows `No subagent sessions yet`; tab navigation and `n` remain available.
- When both tabs are empty, startup skips the picker and goes directly to new-session setup.
- Each tab is served as its own recency-ordered window with infinite scroll and keeps at most two bounded pages resident. Traversal loads older pages and reloads evicted newer pages when navigating back; neither client nor server holds or requests the full session set.
- Initial load and fresh retry replace the affected tab body. Older/newer continuation keeps resident rows, selection, and viewport visible, shows a loading affordance at the requested edge, and blocks only crossing that pending edge.
- Each row shows session title and relative age. Detail facts for the selected session (workspace path, git branch, auth method, model) load asynchronously with loading placeholders; selection is never blocked on them.
- Keys: `Up`/`Down` and `j`/`k` move selection; `PgUp`/`PgDn` page; `Tab`/`Shift+Tab`, `Left`/`Right`, and `h`/`l` switch tabs without opening a session; `Enter` opens; `n` starts a new session. `Esc` is a no-op, `q` has no binding, and `Ctrl+C` is the sole picker exit key.
- Opening a session seeds the next main input from the session's persisted draft verbatim (byte-for-byte, including whitespace and newlines).

## Project Binding Flow

- Unregistered cwd offers create-new-project first, existing-project picker below. May create a project and attach the current workspace as first workspace/main worktree, or attach the current workspace to an existing project. (owner: core-runtime-tools :: Sessions And Persistence)
- Server-browsing mode opens existing server projects/workspaces only; no binding or creation offered there. (owner: core-runtime-tools :: Sessions And Persistence)
- The project-name prompt's pre-fill is supplied by the server (derived from the workspace it owns); the client never reaches into the filesystem to derive names. Empty names are rejected with an inline error.

## Workspace-Change Prompt

- Picking a session whose stored workspace root differs from the current root shows a `Workspace changed` confirmation: `Yes` retargets the session, `No` returns to the picker. Rebinding is always explicit user action. (owner: tui-transcript :: Startup And Session Selection; core-runtime-tools :: Sessions And Persistence)

## Multi-Client Attach

- Picking a session that another client is attached to attaches with equal full control; there is no read-only or lesser-control startup mode. (owner: terminology :: Equal Full-Control Attach)

## Errors

- Preflight server-connection failure at TUI startup exits the TUI with an actionable error message (endpoint + reason). No retry affordance, no per-surface handling.
- After startup, server-connection loss is handled by exactly one global centralized connection handler that surfaces `server connection lost` through the persisted status line. Individual surfaces never implement their own connection-error handling, widgets, or designs; if centralized handling is impossible in some path, the TUI crashes cleanly rather than growing ad-hoc handlers.
- Any startup exit path (success, cancel, failure) restores the terminal best-effort: alt-screen exited, cursor restored, no residual control state. Ongoing-mode output already written to the normal buffer is permanent by design and is not restored.
- Debug builds fail fast on startup invariant violations; release builds surface the error and recover or exit with a clear message. (owner: core-runtime-tools :: Configuration)

## Known Drift (Go TUI, frozen)

The shipping Go TUI predates these decisions and diverges: it has an embedded-server fallback, an env-vs-saved auth conflict picker, and client-side project-name derivation. These are drift, not spec.
