# TUI Chat Core (Composer, History, Autocomplete, Queueing)

## Composer Surface

- The composer is part of the Ongoing Mode Mutable Band and uses the native terminal cursor.
- All TUI text fields use the same editing behavior.
- The editor determines its rendered lines and cursor position.
- Kent can use a drawn cursor only when native cursor placement cannot avoid verified cursor drift, wrap mismatch, or Alternate Screen corruption.

## Editing Model

- Movement: `Left`/`Right` by grapheme cluster; `Ctrl+Left`/`Ctrl+Right` and `Option+Left`/`Option+Right` by word; `Home`/`Ctrl+A` to line start; `End`/`Ctrl+E`/`Ctrl+End` to line end. At the typed terminal-input boundary, an Alt-modified single-rune `b`/`f` event is a compatibility encoding for previous-word/next-word movement rather than inserted text. `Up`/`Down` move across wrapped/multiline content when the cursor is not at a whole-buffer boundary (boundary behavior is prompt history, below).
- Deletion: `Backspace`/`Ctrl+H` deletes backward; `Delete` deletes forward; `Ctrl+W` deletes the previous word.
- Kill/yank: `Ctrl+K` kills to line end; `Ctrl+U` kills to line start (macOS: `Ctrl+U` deletes the current line); `Ctrl+Y` yanks the last killed text. One editor-local kill buffer.
- `Shift+Enter` inserts a newline. Known Shift+Enter and Ctrl+Enter terminal encodings map to their canonical actions. `Ctrl+J` always inserts a newline.
- The input area grows with wrapped content. No other part of the TUI inserts content into the editor's rendered lines.

## Prompt History

- `Up` and `Down` recall prompt history only at whole-buffer boundaries. Failed navigation emits terminal BEL and no transient notice.
- Recall replaces the whole buffer. Navigating below the newest entry restores the in-progress draft the user was typing before navigation began.
- Editing a recalled entry detaches it from history navigation: it becomes the live draft, and further `Up` starts from the newest entry again.
- Opening a Session supplies its 100 most recent recorded prompts. The TUI keeps only that bounded history.

## Path Autocomplete

- Kent prepares and caches a workspace-relative path list asynchronously. Typing never waits for a new filesystem scan.
- The path list includes hidden paths, excludes `.git`, respects normal ignore files, and derives directories from included files.
- A failed path-list load can be retried in the same workspace.
- The picker appears when the cursor is in an `@` query made from Unicode letters or numbers plus `/`, `.`, `_`, and `-`. It disappears when the cursor leaves the query or the query is deleted. It has no separate modal state.
- Candidates whose repo-relative path changes under terminal-safe projection are excluded before fuzzy matching and result limits. `Up`/`Down` navigate eligible candidates; `Tab` or `Enter` accepts the selection, replacing the query with the exact chosen repo-relative path; terminal controls are never inserted into the composer, and while the picker is visible these keys never submit or queue.
- The picker renders between transcript and input, sized to available height; typing is never blocked by picker state or corpus readiness.

## Queueing And Steering

- `Tab` queues or sends, and `Ctrl+Enter` is an alias.
- While the Agent Turn is busy, `Enter` adds another Steer without locking the composer.
- Steer operations take effect at safe Agent Step boundaries.
- Pending Queue and Steer messages use FIFO order.
- Several human messages delivered at one boundary become one user message separated by blank lines. Each steer issued from another Session remains a separate message.
- Pending messages have no fixed count limit and survive only until delivery or process exit.
- Pending messages render as a visible pane between transcript and input until drained. The pane shows both queued post-turn messages and pending steering messages, each in FIFO order; queued messages render above steering messages.
- There is no standalone per-item removal or reordering affordance. The only user-facing removal is the busy `Ctrl+C` interrupt, which drains both pending queues into the main input (see Interrupts And Exit).
- When the live TUI observes an interrupt, it best-effort restores pending Queue and Steer messages to the composer in submission order, followed by any existing composer draft.
- Pending Queue and Steer messages are not persisted for restoration. Process exit before the TUI observes the interrupt loses them.
- The following creation-failure behavior applies to every queued message or Steer, including Allow commentary.
- If Kent cannot create the queued message or Steer, the failed message returns to the composer and requires an explicit user action to send again. The failed message does not remain pending or retry automatically.
- The restored text is the exact message Kent attempted to submit. If the composer already contains a newer draft, Kent keeps that draft first, inserts one blank line, appends the failed message, and places the cursor at the end.
- The failure appears as a transient status-line error using the ordinary submission failure detail. It does not change the activity indicator. The TUI does not create a transcript feedback row for this failure.
- Each `/compact` submission retains its exact submitted text and whether it was sent directly or drained from the post-turn Queue until that request completes.
- If Kent does not accept a `/compact` request, the TUI restores that request's exact text through the ordinary creation-failure behavior. A post-turn Queue drain stops at that rejected request.
- If Kent accepts a `/compact` request, success or a later failure consumes the command without restoration. A post-turn Queue drain continues only after that request completes.
- Repeated `/compact` submissions remain independent requests. The TUI does not reject a submission because another compaction request is pending or running.
- Server-published compaction lifecycle is the TUI's only compaction-activity authority. Dispatch and request completion do not change compaction activity locally.
- If the failed message is Allow commentary, Kent delivers the Approval answer independently while the transient notice is active. Successful Allow commentary creation still precedes the Approval answer.

## Interrupts And Exit

- `Ctrl+C` interrupts only an active Agent Turn. It stops the current Agent Step and active tool, keeps the Session available, adds the Detail Mode control message `User interrupted you`, returns to idle with input ready, and requires explicit user text to resume.
- `Ctrl+C` does not cancel a submission before its Agent Turn starts.
- The interrupt also drains pending messages into the main input so the user can edit or resend them.
- `Ctrl+C` without an active `Agent Turn` exits the TUI. A submission already sent to the server may start or continue after the client detaches.
- Graceful exit through `Ctrl+C` or `/exit` saves the current composer draft before releasing the Session attachment.
- `/exit` detaches the client and does not interrupt the Active Session Runtime. Active work continues after this TUI releases its attachment.
- Session-navigation commands persist the outgoing draft, resolve the typed transition, release the originating attachment, and only then plan or attach the destination. A release failure aborts navigation before destination attachment; an `/exit` release failure is reported after terminal teardown and exits nonzero.
- In the rollback picker, `Ctrl+C` closes the overlay before it applies the same busy-interrupt or idle-exit behavior as the main transcript.
- `Shift+Tab` and `Ctrl+T` toggle between Ongoing Mode and Detail Mode. The selected mode is not saved.
- Arming the rollback flow requires two consecutive `Esc` presses on empty idle input within a short window (Esc-Esc); a single `Esc` does nothing visible. Rollback behavior itself is owned by its own family spec.

## Clipboard Paste

- `Ctrl+V`, `Ctrl+D`, `Alt+V`, and `Alt+D` read the system clipboard. Images are saved as temporary PNG files and their path is inserted. Text is inserted unchanged at the cursor.
- Terminal bracketed paste follows ordinary text-input handling and never causes a system clipboard read.
- Paste failures, including unsupported or empty content and a missing host capability, appear as a transient status-line error that identifies the unavailable clipboard capability. Paste never fails silently.
