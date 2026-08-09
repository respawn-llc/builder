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
- The following creation-failure behavior applies to every queued message or Steer, including Allow commentary.
- If Kent cannot create the queued message or Steer, the failed message returns to the composer and requires an explicit user action to send again. The failed message does not remain pending or retry automatically.
- The restored text is the exact message Kent attempted to submit. If the composer already contains a newer draft, Kent keeps that draft first, inserts one blank line, appends the failed message, and places the cursor at the end.
- The failure appears as a transient status-line error using the ordinary submission failure detail. It does not change the activity indicator. The TUI does not create a transcript feedback row for this failure.
- If the failed message is Allow commentary, Kent delivers the Approval answer independently while the transient notice is active. Successful Allow commentary creation still precedes the Approval answer.
- Across interrupt recovery and a late creation result, Kent must not lose message content. Duplicate editable text is allowed.

## Interrupts And Exit

- `Ctrl+C` interrupts only an active Agent Turn in an ordinary Session. It stops the current Agent Step and active tool, keeps the Session available, adds the Detail Mode control message `User interrupted you`, returns to idle with input ready, and requires explicit user text to resume.
- Every fresh user-initiated Session activation may Resume an interrupted retained Workflow Current Node or join the Run that already won. This includes initial `--session`, picker selection, and in-app Session navigation.
- While retained activation waits for capacity, preparation, launch, or Exact registration, no TUI attachment exists. The same Run remains visible as queued through Task surfaces; once launch becomes potentially blocking, those surfaces may Interrupt it. Activation cancellation returns without canceling the Run, and launch failure returns the typed error after durable interruption.
- Automatic runtime/transcript recovery uses technical reattachment. It never Starts or Resumes Workflow work. It attaches to the matching Exact execution live when Kent handles the request, or returns unavailable and creates no TUI attachment when none is live.
- The TUI opens only after Authority atomically attaches it to the expected still-current Exact Execution Scope and ready Resource Generation. Retirement or replacement during attachment fails the open instead of creating an ordinary Runtime.
- In an opened retained Workflow Session, `Ctrl+C` delegates interruption of the matching Exact Execution Scope and durable Current-Node interruption to Workflow Execution. If persistence fails, the TUI surfaces the operation failure and leaves Exact execution active.
- The retained Workflow attachment may remain open after that Exact ends. The next explicit TUI operation that ordinarily starts execution—message, user shell, or compaction—seamlessly creates or joins the Current-Node Run and executes through Workflow authority. It never starts an ordinary Session-owned scope; automatic reconnect still remains attach-only.
- If that later operation is still in interruptible launch before Exact registration, `Ctrl+C` targets its creator operation and Workflow Run through the same durable interruption-before-cancel path as Task Interrupt. The server reports canceled-not-committed and restores editable input only after durable interruption succeeds; failure leaves the operation and Run unchanged.
- In an ordinary Session, `Ctrl+C` does not cancel a submission before its Agent Turn starts.
- The interrupt also drains pending messages: queued and steering queue contents populate the main input, so nothing typed is lost and the user can edit or resend.
- `Ctrl+C` without an active ordinary Agent Turn or interruptible retained Workflow Run exits the TUI. A submission or non-interruptible queued Workflow Run already accepted by the server may start or continue after the client detaches.
- Draft recovery does not depend on a graceful shutdown callback. Closing the terminal window or otherwise losing the TUI process preserves the current main-input draft; opening the session later seeds the input from that draft verbatim.
- Structured draft-recovery entries preserve only their recovery category and text. They carry no runtime operation, request, or queue identity.
- Recovered entries return as editable composer text and are never reconstructed into operational queues, resumed, or replayed automatically; sending them again requires an explicit user action.
- Older recovery metadata that contains operation or queue identity remains readable. Kent ignores the obsolete identity while preserving the recovery category and text, without an upgrade warning.
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
