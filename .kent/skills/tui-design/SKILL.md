---
name: tui-design
description: How to design TUI frontends. Use when requests involve TUI layout, spacing, colors, UX/UI and working on user-facing surfaces.
---

## Core principles
1. Optimize for scanability first.
- Users should identify the screen, major sections, and key state in a few seconds.
- Prefer compact summaries over verbose dumps, use color to convey meaning.
- Put the most decision-relevant information first.

2. Design for terminal constraints.
- Width is scarce and volatile.
- Height is also volatile; assume partial visibility and build scrolling support for every surface you create.
- Avoid layouts that depend on exact pixel positioning or mouse interaction.

3. Use structure, not decoration, to create clarity.
- Good grouping, alignment, color, emphasis should be used wherever possible.
- Follow the 60/30/10 rule: 60% foreground color or faint variation of it, 30% primary color, 10% other colors (success/error/warning/secondary etc)
- Add chrome only when it explains the screen or improves navigation.
- Remove labels and lines that repeat information already obvious from context.

4. Show cached/known state immediately, then hydrate progressively.
- Open the screen instantly.
- Render placeholders only for genuinely missing parts.
- Refresh slow sections asynchronously.

5. Meet the visual bar before shipping, not after feedback.
- A polished, styled surface is the requirement, not a follow-up. "Make it work, leave it ugly" is a defect.
- Build every bounded surface to this standard in the same change that introduces it. Do not ship an unstyled text-block surface intending to style it later, and do not wait for the operator to complain before meeting the bar.
- Reactive styling is banned: never bolt color/spacing/borders onto an already-shipped raw-text surface after the fact. Layout and emphasis are designed into the surface from the start.
- Compose surfaces through the render framework's layout/style/widget primitives. Do not hand-assemble screens by concatenating strings, splicing ANSI escapes, or computing cursor row/column by manual line arithmetic — that path is what produces unstyled-then-hacked surfaces.
- Run the screen-review checklist (bottom of this skill) before the surface is considered done.

## Surface choice

### Use native scrollback for ongoing/log-style surfaces
Use the terminal's main screen and native scrollback when the UI is fundamentally append-only:
- chat transcripts
- logs
- command output streams
- timelines
- activity feeds
- Only ONE native scrollback surface is allowed per entire app. Avoid re-emission of the scrollback history as much as possible.

Rules:
- Once a line is emitted, do not rewrite or repaint history unless resize absolutely requires reflow.
- Prefer appending new information instead of replaying the whole screen.
- This mode should feel like a high-quality terminal session, not a canvas app.

### Use alt-screen for full-screen destinations
Use the terminal alternate screen for surfaces that conceptually replace the current screen:
- pickers
- dashboards
- settings screens
- dedicated status pages
- process lists
- inspectors
- modal navigation destinations

Rules:
- Treat alt-screen as navigation to a separate destination, not as transcript detail.
- Preserve the main-screen scrollback underneath; returning should restore it intact.
- Full-screen destinations should not depend on replaying main-screen history on entry.
- If the screen is not append-only, it probably belongs in alt-screen.

## Layout rules

### Hierarchy
Use a consistent hierarchy:
- screen title or primary section labels
- primary value lines
- secondary metadata lines
- grouped subsections
- warnings or failure states near the affected section

### Section structure
A good section usually looks like:
- title in a strong accent color
- 1-3 high-value lines
- optional grouped items under subheaders
- one blank line between major sections

Avoid:
- walls of labels
- repeated prefixes
- raw debug dumps unless explicitly requested
- explicit empty states where they can be inferred by vacuous truth, such as "0 overrides" or "no problems detected"

### Padding and spacing
- Use vertical spacing more than horizontal indentation.
- Use one blank line between major sections.
- Use two blank lines between groups of major sections (highest level of separation)

### Alignment
- Align repeated structures to the same column when it improves scanability.
- For rows like `label | bar | value | meta`, pad the labels so bars align vertically.
- Keep grouped items visually parallel.
- When width gets tight, shorten the least important part first.

## Text emphasis

### Bold
Use bold for:
- titles
- primary values
- important state words like `clean`, `dirty`, `on`, `off`, `fast`

Do not bold entire dense blocks unless the whole line is a key summary.

### Faint text
Use faint for:
- timestamps
- helper metadata
- secondary IDs
- explanatory suffixes
- tree connectors

Do not use faint for:
- primary metrics
- important thresholds
- state the user must act on
- labels needed to understand the screen

### Full-strength text
Use normal or bold full-strength text for:
- actual values
- section-relevant controls/state
- directory headers that group visible items
- threshold lines like `Compaction at ...`

## Color rules

Use color semantically, not decoratively.

### Recommended roles
- Primary: section titles, subsection counters, navigation headings
- Green/Success: healthy/safe/available/clean/ahead-positive/good remaining quota
- Yellow/Warning: warnings, toggles like `fast`, medium caution, partial depletion
- Red/Error: dirty/error/behind-negative/critical depletion
- Muted: secondary metadata, timestamps, decorations (trees, ascii symbols, dividers, separators)
- Default foreground: normal readable body text

### Color discipline
- Prefer one accent color for structure and semantic colors for status.
- Avoid rainbow rows.
- Keep primary values readable in monochrome terminals.
- Never rely on color alone; the wording must still make sense.

## Symbols and line art

Prefer ASCII first for broad compatibility.

Use Unicode only when it materially improves clarity and is widely supported:
- box or tree connectors like `├─` / `└─`
- bullets like `•`. Use largest dot available by default to make it visible.
- block characters for progress bars
- Avoid Nerd fonts or icons unless the user asks or already used in codebase.
- NEVER use emoji for anything.

Guidelines:
- Tree structures are excellent for "items belong to this directory/group".
- Avoid heavy decorative box drawing unless the app really needs frames.

## Progress bars

Use progress bars for quantities that benefit from immediate spatial reading:
- quota remaining
- context usage
- task progress

Guidelines:
- Keep bars on a single line with label and summary when possible.
- Use semantic fill color based on health, if applicable.
- Clamp for narrow widths; if space is tight, reduce bar width before dropping the summary.
- Use the render framework's drawing primitives (gauge/progress, styled rows) when they simplify rendering and remain visually stable; keep interactive behavior (input, cursor, selection, navigation) in the app model rather than in behavior-owning widgets.

## Information design

- Each screen should have a way to surface errors or toasts/transient messages to the user. Prefer reusable architecture and UI for error/toast surfacing.
- Each screen should indicate loading in some way. No blank screens, no freezes, no placeholder values.

### What not to include
Remove:
- redundant section titles repeated in body lines
- implementation details unless useful to operators
- long raw command output when a summary is enough
- placeholders like `none` when silence is cleaner and no ambiguity is introduced

### Summarize aggressively

Merge information to improve density

Example:
- Use `Pro subscription` instead of repeated `Subscription` + `Plan pro`

Prefer grouped, counted lists:
- `3 skills`
- directory header
- tree items

## Responsive behavior

### Narrow width strategy
When width shrinks:
1. preserve section titles
2. preserve primary value text
3. shorten metadata
4. shrink bars
5. drop optional suffixes only last

Never let ANSI styling corrupt wrapping or truncation.
Pre-rendered styled lines must go through ANSI-aware width handling.

### Height strategy
- Assume the user may only see the top section at first.
- Put high-value sections first.
- Make all screens scrollable.
- Do not assume the entire screen fits at once.

## Loading and caching

### Progressive loading
- Open first, load second.
- Cache slow data in memory when repeated opens are expected.
- Render cached state immediately if it is still useful.
- Refresh in background and merge results by section.

## Interaction guidance
- Keep visible controls minimal.
- Hidden keybindings are acceptable only when the screen is otherwise obvious and low-risk.
- If the screen is read-only, avoid control clutter.
- Refresh affordances should exist only if they earn their screen space.

## Screen review checklist
Before shipping a TUI screen, check:
- Does it open instantly?
- Does it still look good at narrow widths?
- Are titles, values, and metadata clearly differentiated?
- Are colors semantic and restrained?
- Are repeated rows aligned?
- Is faint text only used for secondary information?
- Does scrolling reveal lower sections cleanly?
- Does alt-screen usage match the screen's purpose?
- Can the main scrollback be restored cleanly?
- Are cached and loading states honest?
