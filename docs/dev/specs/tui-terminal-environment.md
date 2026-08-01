# TUI Terminal Environment (Resize, Theming, Degradation)

## Resize

- In Ongoing Mode, a width change that cannot use terminal-native repaint starts Scratch Rehydration after a one-second debounce.
- Bounded Alternate Screen surfaces relayout immediately for each resize. They do not use Scratch Rehydration.
- Resize never loses state: selections, scroll positions (clamped to new bounds), input drafts, and in-flight operations all survive a resize on every surface.

## Size Degradation

- Below 40 columns by 10 rows, the TUI leaves its owned frame blank. Existing Scrollback remains unchanged.
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

## Markdown Hyperlinks

- Kent emits OSC 8 for valid Markdown link destinations on every terminal.
- Exact terminal whitelisting controls only destination visibility. Ghostty and libghostty embedders (`TERM_PROGRAM=ghostty`), kitty (`KITTY_WINDOW_ID` or `TERM=xterm-kitty`), iTerm2, WezTerm, Alacritty, Windows Terminal, VS Code-compatible terminals, and Zed render an explicit Markdown link as its clickable label. Version strings do not participate in detection.
- Terminal.app, terminals outside the whitelist, and sessions inside tmux, Zellij, or GNU screen render an explicit Markdown link as clickable `label destination`; unsupported terminals ignore OSC 8 and retain the visible destination.
- GFM table layout remains library-owned. Explicit links inside table cells render clickable `label destination` on every terminal, independent of the terminal whitelist.
- Autolinks render the destination once in both modes.

## Patch File Hyperlinks

- Kent emits OSC 8 on every terminal when a patch file row's metadata contains a syntactically absolute path.
- Patch file hyperlinks work on every operating system supported by the TUI.
- Kent resolves a relative path from a current patch tool call against that tool call's working directory before creating the hyperlink destination.
- A compact patch row displays its workspace-relative path. When linked, its hyperlink destination is the file's absolute local-file URI.
- A current patch file header in Detail mode displays its absolute path. Its hyperlink destination is the same absolute local-file URI.
- Local-file URIs use forward slashes, including for Windows drive paths.
- Only the displayed path characters are part of the hyperlink. Change counts, the Detail selection rail, and trailing row whitespace are not part of the hyperlink.
- When a path wraps or is truncated, every displayed path character remains linked. A truncation marker is not part of the hyperlink.
- Unsupported terminals ignore OSC 8 and retain the visible path without additional destination text.
- Added files, deleted files, unsuccessful patch attempts, and paths that do not currently exist remain linked.
- A moved file links to its destination path.
- Kent does not query the filesystem or validate a patch link destination before emitting it.
- Patch links keep the existing path styling without a link-specific underline or other decoration.
- A structured legacy patch row remains visible and unlinked when its original working directory is unavailable and its rebuilt metadata path is relative.
- Raw patch content without an identified file row remains unlinked.
