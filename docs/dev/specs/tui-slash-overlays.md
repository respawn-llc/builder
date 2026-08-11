# TUI Slash Overlays

## Shared Overlay Conventions

- Overlays are bounded Alternate Screen surfaces. Closing one restores the transcript surface underneath.
- `Esc` and `q` close a read-only overlay; inside a nested dialog `Esc` steps back one level (dialog → parent overlay) instead of closing the whole flow.
- `Ctrl+C` inside any overlay routes to the single global runtime Ctrl+C handling (interrupt while busy / exit while idle), closing the overlay surface first — overlays never swallow or reinterpret it.
- List/scroll keys are uniform: `Up`/`Down` move (plus `j`/`k` on list surfaces), `PgUp`/`PgDn` page, `Home`/`End` jump.
- Button groups are uniform: `Tab`/`←`/`→` (plus `h`/`l`) move selection, `Enter` activates, Cancel is the default selection for destructive confirms.
- While an overlay change is pending, the overlay ignores input. A failed action shows its error inline while the overlay is open. If the overlay closed, the error appears as a status notice and transcript entry.
- List selections are identity-stable: refreshes and live updates re-match the selected item by ID, never by index.

## /status

- `/status` is read-only and loads its sections progressively.
- Content is organized in sections — Session, Git, Context, Auth (account + subscription windows with usage bars and reset times), Config (override sources, supervisor, questions), Skills, AGENTS.md inspection, Warnings — each section loads independently with a loading placeholder; section failures become warnings listed in the Warnings section, never a blank overlay. The section set is behavior-level; copy/layout is presentation.
- Keys: scroll only (shared conventions); no actions.
- `/status` does not show server ownership because the TUI is always a client.

## /goal

- `/goal` and `/goal show` open the overlay. `set <objective>`, `pause`, `resume`, and `clear` act directly.
- Overlay shows goal status, goal ID, and the objective rendered as Markdown; a no-goal state shows a hint to start one; load errors render inline in the overlay.
- Setting an objective while a goal is active opens a Replace confirmation (current vs new objective); clearing an active goal opens a Clear confirmation. Both use a Cancel/Confirm button group (Cancel default) with `y`/`n` shortcuts; confirming issues the mutation and closes the confirm state. Paused goals clear without confirmation.
- Only one Goal change applies at a time for a Session. If the user requests another state while one is pending, the newest requested state replaces the older pending state. Kent does not replay intermediate states.

## /ps

- The overlay lists background processes with their state, command, and working directory. It refreshes while open and when process state changes. It has no manual-refresh key.
- Actions on the selected process: `Enter`/`i` paste the process's recent output into the composer (appending below any existing draft) and close the overlay; `k` sends terminate and refreshes the list; `o` opens the log file via the system opener, falling back to `$VISUAL`/`$EDITOR`. Each action is single-flight; results and failures surface as status notices.
- The paste action is draft-safe: if the composer draft changed between request and response, the stale output is discarded rather than spliced into the newer draft.
- With no processes, actions produce a "nothing selected" notice; the overlay stays open.

## /worktree

- `/worktree` uses the Worktree Management behavior defined in `tui-transcript.md`, including target resolution, deletion safety, aliases, setup, and branch cleanup.
- List phase: first row is the create-new entry (`Enter` on it opens the create dialog, as do `c`/`n`); `Enter` on a worktree row switches to it (`Already current worktree` notice on the current row); `d` opens delete confirmation, `x` opens it with the delete-branch action preselected; `r` refreshes. Main-workspace rows reject deletion with an error notice.
- Create dialog: `Tab`/`Down` and `Shift+Tab`/`Up` cycle fields; `Enter` on a text field advances focus; `Enter` on the action row submits (or cancels); typed `Branch or ref` input resolves asynchronously with a short debounce, never per-keystroke blocking; `Esc` returns to the list.
- Delete confirmation: shows a preview of what will be removed with warnings; button group is Cancel / Delete / Delete + Branch (the branch action is present for every branch-backed worktree); `Esc` returns to the list.

## Rollback Picker

- Esc-Esc on empty idle input opens the picker. Candidates are user messages with rollback targets. The newest candidate is selected first.
- The picker uses Detail Mode rendering on an Alternate Screen. It does not enable Alternate Scroll and ignores mouse events.
- The selected candidate is highlighted and centered in the transcript. `Up`/`Down` move between candidates; at the window edge they page across transcript windows through bounded requests, re-anchoring the selection.
- Candidate-free pages encountered during edge navigation are traversed automatically through sequential bounded reads while remaining transient. Each navigation attempt has a 20-second deadline; timeout stops pagination, keeps the current candidate selected, and surfaces an error in the status line.
- `Enter` creates and opens a rollback fork at the selected message. The fork retains history before the selected user message and excludes that message.
- The rollback fork inherits the parent Session's complete Goal unchanged, including its objective, state, identity, and original creation and update times.
- The fork opens in Ongoing Mode with the selected user message text restored as unsent composer input.
- `Esc` while selecting closes the picker and restores the prior Transcript Mode.
- Rollback never changes the original Session transcript.
- If the transcript has no rollback candidates, arming does nothing visible.
