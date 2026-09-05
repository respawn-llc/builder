# TUI Terminal Environment (Resize, Theming, Degradation)

## Resize

- In Ongoing Mode, a width change that cannot use terminal-native repaint starts Scratch Rehydration after a one-second debounce.
- Bounded Alternate Screen surfaces relayout immediately for each resize. They do not use Scratch Rehydration.
- Resize never loses state: selections, scroll positions (clamped to new bounds), input drafts, and in-flight operations all survive a resize on every surface.

## Size Degradation

- Below 40 columns by 10 rows, the TUI leaves its owned frame blank. Scrollback remains unchanged.
- Rendering resumes when the terminal reaches the minimum size.
- The same size rule applies to every TUI surface.
- At or above the minimum, every surface remains usable. Content that exceeds the height can scroll. Every unbounded list uses Infinite Scroll and never loads the complete list. Horizontal content wraps and does not overflow or disappear off-screen. The status line follows its priority ladder.

## Theme And Color

- Theme setting is `dark`, `light`, or auto (default). Auto resolves through terminal background detection at startup and preselects the onboarding theme.
- All colors come from the shared theme palette's role system (primary/secondary/muted/status roles); surfaces never hardcode colors. Palette colors are adaptive to the resolved theme.
- Theme resolution happens once at startup; there is no live re-detection mid-session.

## Terminal Control Modes

- Ongoing Mode must not use Alternate Scroll (`?1007`).
- Alternate Screen surfaces enable Alternate Scroll while active and disable it on exit. The rollback picker is the exception.
- No surface enables Mouse Capture.

## Native Progress

- The interactive TUI emits OSC 9;4 as a best-effort terminal-native progress signal when `tui_native_progress_bar` is enabled.
- `tui_native_progress_bar` is a global or workspace config-file boolean, defaults to `true`, and has no environment override or command-line flag.
- Kent does not identify terminal brands or probe for OSC 9;4 support. An unsupported terminal may ignore the signal.
- Disabling native progress suppresses only OSC 9;4 output. It does not change in-TUI spinners, loading text, status labels, or the context meter.
- Compaction, Reviewer activity while `invoking`, Detail Mode transcript page loading, Worktree creation including its setup script, and Worktree deletion activate native progress.
- The main Agent Turn, Reviewer activity while `addressing_feedback`, Worktree list refresh, Worktree target lookup, Worktree switch scheduling, Session listing, Project loading, Session opening, workspace retargeting, startup surfaces, and onboarding do not activate native progress.
- The eligible operations listed above use indeterminate native progress because they expose no trustworthy completion fraction.
- Kent may emit numeric native progress only when the operation supplies an authoritative percentage. Kent must not derive a percentage from elapsed time or arbitrary phase weights.
- Kent waits 500 milliseconds after aggregate eligible activity begins before it emits native progress.
- If all eligible activity ends during the delay, Kent emits no native progress signal.
- Once visible, native progress remains continuously active while at least one eligible operation remains.
- Starting or ending an overlapping eligible operation does not restart the delay, flash, or clear native progress while another eligible operation remains.
- Success, failure, interruption, cancellation, Session transition, and TUI exit clear native progress immediately unless another eligible operation remains active.
- This feature does not emit the OSC 9;4 error state.
- Headless commands, Workflow commands, JSON output, redirected output, Desktop, and the server do not emit native progress.
- The interactive TUI clears native progress best-effort whenever it exits.

## Markdown Hyperlinks

- Kent emits OSC 8 for valid Markdown link destinations on every terminal.
- Exact terminal whitelisting controls only destination visibility. Ghostty and libghostty embedders (`TERM_PROGRAM=ghostty`), kitty (`KITTY_WINDOW_ID` or `TERM=xterm-kitty`), iTerm2, WezTerm, Alacritty, Windows Terminal, VS Code-compatible terminals, and Zed render an explicit Markdown link as its clickable label. Version strings do not participate in detection.
- Terminal.app, terminals outside the whitelist, and sessions inside tmux, Zellij, or GNU screen render an explicit Markdown link as clickable `label destination`; unsupported terminals ignore OSC 8 and retain the visible destination.
- GFM table layout remains library-owned. Explicit links inside table cells render clickable `label destination` on every terminal, independent of the terminal whitelist.
- Autolinks render the destination once in both modes.

## Patch File Hyperlinks

- Kent must emit OSC 8 on every terminal for a patch path that identifies a syntactically absolute local path. Patch file hyperlinks must work on every operating system supported by the TUI.
- Kent must resolve a relative patch path against the working directory used for that patch before deriving its destination. A compact row must continue to display the workspace-relative label while its destination uses the resolved absolute local-file URI. A Detail header must display the available absolute path and use the same destination.
- Local-file URIs must use forward slashes on every operating system, including for Windows drive paths.
- Only displayed path characters may be part of the hyperlink. Counts, the Detail selection rail, trailing whitespace, and truncation markers must remain outside it, including when a path wraps. Every displayed path fragment must remain linked across wrapping and truncation.
- Unsupported terminals must ignore OSC 8 and retain the visible path without additional destination text. Added, deleted, unsuccessful, and nonexistent files must remain linked when their paths identify absolute local paths. Moved files must link to their destination paths. Patch links must keep the existing styling. When context does not identify an absolute path or file rows cannot be identified, Kent must keep the visible path or content and leave it unlinked.
