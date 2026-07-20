# Respawn Chat Reference

Respawn's mobile chat was inspected as a design and state-management reference. It is not authoritative for Kent desktop.

## Reusable Patterns

- A message row fills the available width; role alignment positions a content-sized island at the start or end.
- The island uses intrinsic content width under a parent maximum. The apparent “first line expands the island” behavior is an outcome of layout constraints, not text parsing or manual measurement.
- Persisted paged history and transient pending/streaming items are separate state collections joined by stable message identity.
- Streaming deltas replace immutable transient state; rendering consumes the newest projection.
- Markdown is owned by one shared renderer and remains selectable.
- The measured input-surface height becomes transcript bottom padding so a growing composer never obscures messages.

## Not Present In Respawn

- Contextual inline sidebars for file contents, plans, diffs, tool results, questions, file trees, or summaries.
- Rich tool-result payload presentation.
- Per-message expansion state.
- Desktop history-prepend anchoring.

Kent must use its own shared sidebar architecture and transcript contracts for these capabilities.

## Mobile-Specific Behavior Not To Copy

- Reverse mobile list assumptions as a substitute for explicit desktop scroll anchoring.
- Bottom sheets, IME/navigation-bar insets, touch-target sizing, haptics, sounds, and audio controls.
- Mobile-only haze and system-bar treatment.
- Auto-scrolling only after a completed message.

## Current Respawn Timestamp Pattern

Respawn places a muted timestamp inside the completed assistant island's footer row beside feedback actions. It does not implement the proposed Kent behavior of a timestamp below every conversational island, so Kent's timestamp layout requires an explicit decision.

## Jump To Latest

Respawn shows a 40dp circular glass `ArrowDown` control at the bottom end of the transcript/input overlay, inset 12dp from the end and 12dp above the input. Its icon is 24dp. Scale enter/exit motion is driven by `firstVisibleItemIndex > 1`, so the control appears after scrolling beyond the two newest reverse-list items. It has no count. Activation animates directly to the newest item.
