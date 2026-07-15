# TUI Terminal Environment (Resize, Theming, Degradation)

Terminal-level cross-cutting behavior: resize, size degradation, theme detection, color capability, terminal control modes. Excludes behavior owned elsewhere: errors/exit (tui-startup :: Errors), notices (tui-status-line :: Notices), ongoing write failures and rehydration (ongoing-scrollback-buffer), debug fail-fast (core-runtime-tools :: Configuration), notifications (tui-transcript :: Notifications).

Bullets marked (owner: …) restate decisions owned by another spec for one-place readability; the owner spec is authoritative for them.

## Resize

- Ongoing-mode resize is owned by ongoing-scrollback-buffer :: Scratch Rehydration: visually-broken emitted content triggers rehydration, debounced 1 second. (owner: ongoing-scrollback-buffer)
- Bounded alt-screen surfaces relayout immediately on every resize event — no debounce, no rehydration concept; the surface re-renders from its typed model at the new geometry.
- Resize never loses state: selections, scroll positions (clamped to new bounds), input drafts, and in-flight operations all survive a resize on every surface.

## Size Degradation

- Future Rust TUI too-small guard: below the minimum size, the TUI renders nothing — a literally blank owned frame (alt-screen surface or ongoing mutable band; emitted scrollback is immutable and untouched). Rendering resumes normally once the terminal is at or above the minimum again. Minimum: 40 columns × 10 rows.
- In the future Rust TUI, the guard is app-global: implemented once as a global override at the renderer/navigation-stack level, in front of every alt-screen navigation destination and the ongoing mutable band. Individual Rust surfaces and page layouts never check terminal size or implement their own too-small handling.
- The shipping Go TUI applies the same 40×10 blank behavior to the BUI-41 session-picker lifecycle and the existing main-TUI ongoing ownership boundary. Other Go startup surfaces retain their existing size behavior.
- At or above the minimum, every surface must remain crash-free and render sensibly. The floor for every page: vertical scrolling when content exceeds the height; infinite-scroll pagination for every unbounded list (never a full in-memory load); horizontal content wraps — never overflows or gets clipped off-screen. The status line follows its ratified priority ladder.

## Theme And Color

- Theme setting is `dark`, `light`, or auto (default). Auto resolves via terminal background detection at startup; detection also pre-selects the onboarding theme step default.
- All colors come from the shared theme palette's role system (primary/secondary/muted/status roles); surfaces never hardcode colors. Palette colors are adaptive to the resolved theme.
- Theme resolution happens once at startup; there is no live re-detection mid-session.

## Terminal Control Modes

- Ongoing mode must not use `?1007` (alternate scroll). (owner: terminology :: Alternate Scroll)
- Alt-screen surfaces enable alternate scroll while active and disable it on exit; the rollback picker is the exception (detail rendering without alternate scroll). (owner: terminology :: Alternate Scroll; tui-transcript :: Detail Mode)
- No surface enables mouse capture, on any surface. (extends tui-startup :: Surface Architecture app-wide)

## Shipping Go TUI

- Go startup surfaces use independent lifecycles; the shared navigation stack is a future Rust TUI requirement.
- Outside the BUI-41 session picker and main-TUI ongoing ownership boundary, Go startup surfaces retain their existing size behavior.
