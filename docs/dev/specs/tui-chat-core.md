# TUI Chat Core (Composer, History, Autocomplete, Queueing)

Covers the main-input composer end-to-end: editing model, multiline behavior, prompt history, `@` path autocomplete UX, queueing/steering UX, interrupts, clipboard image paste, draft persistence at exit.

Excludes: transcript rendering (ongoing-scrollback-buffer.md, tui-transcript.md), slash-command semantics (tui-transcript :: Slash Commands), the Esc-armed rollback flow (own family spec — only its entry key is defined here).

Bullets marked (owner: …) restate decisions owned by another spec for one-place readability; the owner spec is authoritative for them.

## Surface Architecture

- The composer is part of the ongoing normal-buffer live region: it renders on the crossterm direct-output path, not as a Ratatui bounded surface, and uses the native hardware terminal cursor.
- One shared editor implementation with a real terminal cursor across ongoing and alt-screen surfaces; the editor render owns rendered lines and cursor coordinates; soft-cursor fallback only for verified drift/wrap/corruption cases. (owner: tui-transcript :: Input And Queueing)
- The shared editor's grapheme/width/cursor semantics are pinned by the executable fixture in `tui-core` (width-cursor semantics test); that fixture, not prose, is the authority for cluster-level behavior.

## Editing Model

- Movement: `Left`/`Right` by grapheme cluster; `Ctrl+Left`/`Ctrl+Right` and `Option+Left`/`Option+Right` by word; `Home`/`Ctrl+A` to line start; `End`/`Ctrl+E`/`Ctrl+End` to line end. At the typed terminal-input boundary, an Alt-modified single-rune `b`/`f` event is a compatibility encoding for previous-word/next-word movement rather than inserted text. `Up`/`Down` move across wrapped/multiline content when the cursor is not at a whole-buffer boundary (boundary behavior is prompt history, below).
- Deletion: `Backspace`/`Ctrl+H` deletes backward; `Delete` deletes forward; `Ctrl+W` deletes the previous word.
- Kill/yank: `Ctrl+K` kills to line end; `Ctrl+U` kills to line start (macOS: `Ctrl+U` deletes the current line); `Ctrl+Y` yanks the last killed text. One editor-local kill buffer.
- `Shift+Enter` inserts a newline; known Shift+Enter/Ctrl+Enter CSI encodings normalize to their canonical actions (owner: tui-transcript :: Input And Queueing). `Ctrl+J` always inserts a newline as a universal fallback. Shift+Enter recognition follows the codex reference implementation; the Go TUI's recognition is known-broken drift.
- The input area grows with wrapped content and the editor render is the only source of line layout and cursor position — callers never splice content into rendered lines. (owner: tui-transcript :: Input And Queueing)

## Prompt History

- `Up`/`Down` recall prompt history only at whole-buffer boundaries; failed navigation emits a plain terminal BEL, no transient UI. (owner: tui-transcript :: Ongoing Mode; :: Input And Queueing)
- Recall replaces the whole buffer. Navigating below the newest entry restores the in-progress draft the user was typing before navigation began.
- Editing a recalled entry detaches it from history navigation: it becomes the live draft, and further `Up` starts from the newest entry again.
- History content is server-supplied at attach and capped at the 100 most recent entries; the client does not own a separate history store. Which entries get recorded (e.g. typed slash commands vs generated prompt bodies) is owned by tui-transcript :: Slash Commands.

## Path Autocomplete

- Corpus and matching semantics: cached repo-relative corpus from `rg --files`, async prewarm, no per-keystroke shell-outs, cursor-local query charset, hidden-paths-in/`.git`-out, derived directories, retryable corpus failures. (owner: tui-transcript :: Path Autocomplete)
- The picker is derived UI: it appears when the cursor sits in a parseable `@` query and disappears when the query no longer parses (cursor moved away, query deleted). No modal open/dismiss state exists.
- `Up`/`Down` navigate candidates; `Tab` or `Enter` accepts the selection, replacing the query with the chosen repo-relative path; while the picker is visible these keys never submit or queue.
- The picker renders between transcript and input, sized to available height; typing is never blocked by picker state or corpus readiness.

## Queueing And Steering

- Queue/send hotkey `Tab` with `Ctrl+Enter` alias; busy `Enter` queues another steering message and never locks the input; soft-insert at safe boundaries; strict FIFO; multi-message boundary coalescing with blank-line separators; unbounded in-memory pending queues; persistence only at delivery boundary. (owner: tui-transcript :: Input And Queueing)
- Pending messages render as a visible pane between transcript and input until drained. The pane shows both queued post-turn messages and pending steering messages, each in FIFO order; queued messages render above steering messages.
- There is no standalone per-item removal or reordering affordance. The only user-facing removal is the busy `Ctrl+C` interrupt, which drains both pending queues into the main input (see Interrupts And Exit).

## Interrupts And Exit

- `Ctrl+C` while busy: turn-local interrupt — stop current step and active tool, keep session alive, detail-only `User interrupted you` control message, idle with input ready, resume needs explicit user text. (owner: tui-transcript :: Input And Queueing)
- The interrupt also drains pending messages: queued and steering queue contents populate the main input, so nothing typed is lost and the user can edit or resend.
- `Ctrl+C` while idle exits the TUI.
- Exiting the TUI (Ctrl+C or `/exit`) persists the current main-input draft to the server before release; opening the session later seeds the input from that draft verbatim (owner: tui-startup.md :: Session Picker).
- `Shift+Tab`/`Ctrl+T` toggle ongoing↔detail transcript mode; mode-toggle ephemerality is owned by tui-transcript :: Modes.
- Arming the rollback flow requires two consecutive `Esc` presses on empty idle input within a short window (Esc-Esc); a single `Esc` does nothing visible. Rollback behavior itself is owned by its own family spec.

## Clipboard Paste

- `Ctrl+V`, `Ctrl+D`, `Alt+V`, and `Alt+D` explicitly read the system clipboard. Images save to temporary PNG files and insert the path; text inserts unchanged at the active cursor. (owner: tui-transcript :: Input And Queueing)
- Terminal bracketed paste follows ordinary text-input handling and never causes a system clipboard read.
- Paste failures (unsupported or empty clipboard content, missing host tool) surface as a transient status-line error naming the missing capability; never a silent no-op.

## Known Drift (Go TUI, frozen)

- Go's Shift+Enter recognition does not work correctly in this repo; the codex implementation is the reference for the Rust client.
