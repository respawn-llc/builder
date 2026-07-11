# TUI Slash Overlays

The daily interactive overlay surfaces opened from the main chat UI: `/status`, `/goal`, `/ps`, `/worktree`, and the Esc-Esc rollback picker. Covers surface conventions, keys, states, and flows.

Excludes (owners authoritative): slash-command-mode mechanics and per-command run-safety (tui-transcript :: Slash Commands), goal domain semantics (core-runtime-tools :: Goals), worktree domain semantics — create-dialog field order, smart target resolution, aliases, delete safety, setup script (tui-transcript :: Worktree Management), rollback arming key (tui-chat-core :: Interrupts And Exit), fork semantics (tui-transcript :: Ongoing Mode).

Bullets marked (owner: …) restate decisions owned by another spec for one-place readability; the owner spec is authoritative for them.

## Shared Overlay Conventions

- Overlays are bounded alt-screen surfaces composed through the shared render layer per the ratified dual-arch decision; opening pushes the surface, closing restores the transcript surface underneath.
- `Esc` and `q` close a read-only overlay; inside a nested dialog `Esc` steps back one level (dialog → parent overlay) instead of closing the whole flow.
- `Ctrl+C` inside any overlay routes to the single global runtime Ctrl+C handling (interrupt while busy / exit while idle), closing the overlay surface first — overlays never swallow or reinterpret it.
- List/scroll keys are uniform: `Up`/`Down` move (plus `j`/`k` on list surfaces), `PgUp`/`PgDn` page, `Home`/`End` jump.
- Button groups are uniform: `Tab`/`←`/`→` (plus `h`/`l`) move selection, `Enter` activates, Cancel is the default selection for destructive confirms.
- While an overlay mutation is in flight the overlay's input is frozen (keys ignored until completion); a failed action surfaces its error inline in the open overlay, or as a status notice + error transcript entry when the overlay is closed.
- List selections are identity-stable: refreshes and live updates re-match the selected item by ID, never by index.

## /status

- Read-only detail overlay refreshing progressively. (owner: tui-transcript :: Slash Commands)
- Content is organized in sections — Session, Git, Context, Auth (account + subscription windows with usage bars and reset times), Config (override sources, supervisor, questions), Skills, AGENTS.md inspection, Warnings — each section loads independently with a loading placeholder; section failures become warnings listed in the Warnings section, never a blank overlay. The section set is behavior-level; copy/layout is presentation.
- Keys: scroll only (shared conventions); no actions.
- There is NO "Server: owned by this CLI" line: under the server-only client model the TUI never owns a server.

## /goal

- `/goal` (bare or `show`) opens the overlay; `set <objective>`/`pause`/`resume`/`clear` act directly; goal-while-busy and workflow-session rules are owned by core-runtime-tools :: Goals.
- Overlay shows goal status, goal ID, and the objective rendered as Markdown; a no-goal state shows a hint to start one; load errors render inline in the overlay.
- Setting an objective while a goal is active opens a Replace confirmation (current vs new objective); clearing an active goal opens a Clear confirmation. Both use a Cancel/Confirm button group (Cancel default) with `y`/`n` shortcuts; confirming issues the mutation and closes the confirm state. Paused goals clear without confirmation.
- Goal mutations are single-flight per session with latest-wins coalescing: a mutation requested while one is in flight replaces the queued desired state; intermediate states are never replayed.

## /ps

- Overlay lists background processes; rows carry state, command, and workdir facts served by the server; the list refreshes on a timer while open and on backend push events for changes. There is NO manual-refresh key.
- Actions on the selected process: `Enter`/`i` paste the process's recent output into the composer (appending below any existing draft) and close the overlay; `k` sends terminate and refreshes the list; `o` opens the log file via the system opener, falling back to `$VISUAL`/`$EDITOR`. Each action is single-flight; results and failures surface as status notices.
- The paste action is draft-safe: if the composer draft changed between request and response, the stale output is discarded rather than spliced into the newer draft.
- With no processes, actions produce a "nothing selected" notice; the overlay stays open.

## /worktree

- Domain semantics — smart-target create dialog, field order (`Branch or ref` before `Base ref`), async ref resolution with `new branch`/`existing branch`/`detached ref` outcomes, aliases, main-workspace protection, delete blocking on running processes, conservative branch cleanup — are all owned by tui-transcript :: Worktree Management.
- List phase: first row is the create-new entry (`Enter` on it opens the create dialog, as do `c`/`n`); `Enter` on a worktree row switches to it (`Already current worktree` notice on the current row); `d` opens delete confirmation, `x` opens it with the delete-branch action preselected; `r` refreshes. Main-workspace rows reject deletion with an error notice.
- Create dialog: `Tab`/`Down` and `Shift+Tab`/`Up` cycle fields; `Enter` on a text field advances focus; `Enter` on the action row submits (or cancels); typed `Branch or ref` input resolves asynchronously with a short debounce, never per-keystroke blocking; `Esc` returns to the list.
- Delete confirmation: shows a preview of what will be removed with warnings; button group is Cancel / Delete / Delete + Branch (the branch action present only when branch provenance allows); `Esc` returns to the list.

## Rollback Picker

- Entry: Esc-Esc on empty idle input (owner: tui-chat-core). Candidates are the transcript's user messages with rollback targets; the newest is preselected.
- The picker uses detail rendering inside alt-screen, does not enable alternate-scroll, and ignores mouse events. (owner: tui-transcript :: Detail Mode)
- The selected candidate is highlighted and centered in the transcript. `Up`/`Down` move between candidates; at the window edge they page across transcript windows (bounded pages — never a full-transcript load), re-anchoring the selection.
- `Enter` begins editing: the composer is seeded with the selected message's text and the status line shows the `editing` label (owner: tui-status-line). `Esc` while editing returns to selection; `Esc` while selecting exits the picker and restores the prior transcript mode.
- Submitting the edited message creates a rollback fork targeted at the selected message and opens it with the edited text as its first prompt — navigation to a new session target, never same-session transcript mutation. (fork nature owner: tui-transcript :: Ongoing Mode)
- If the transcript has no rollback candidates, arming does nothing visible.

## Known Drift (Go TUI, frozen)

- `/ps` binds `r` as a manual-refresh key — an agent-invented affordance, not spec.
- `/status` renders a "Server: owned by this CLI" line — embedded-server relic.
