# Recovered Claude Work

The recovered work was produced on July 5, 2026 and remained in ignored `.kent/plans/` files plus a user-level Claude conversation artifact. The repository copies were:

- `.kent/plans/desktop-chat-feature-scope.md`
- `.kent/plans/desktop-chat-ui.md`

Those files remain useful historical evidence, but they predate substantial transcript, session-picker, runtime, and desktop changes. This document preserves their decisions and proposals without treating the entire artifact as authoritative.

## Explicit User Direction Recovered

The following direction came from direct user messages or user-edited scope markings:

- The long-term outcome is desktop-only agent interaction without requiring the CLI/TUI.
- The chat implementation should use TanStack Virtual's chat pattern and render streamed Markdown.
- Task detail keeps **Open in CLI** and adds a second **Open Chat** action.
- The chat surface is restricted to development builds through a compile-time gate.
- Desktop must consume the same server-owned transcript contract and cursor pagination used by the TUI. Missing or changed server API surfaces require explicit ratification.
- Desktop uses one virtualized transcript instead of reproducing the TUI's ongoing/detail mode split.
- Transcript entries support per-entry expand/collapse and a live streaming tail.
- Terminal mechanics do not transfer: native scrollback, alternate screen, ANSI rendering, terminal mouse modes, BEL/OSC notifications, terminal editor keybindings, and the ongoing/detail toggle.
- Chat initially attaches to an existing task run's session. Fresh independent interactive sessions were deferred in that design pass.
- The initial chat is an in-app route; native pop-out was deferred.
- The composer supports multiline input, send/queue/steer behavior, draft persistence, and a visible stop control.
- Pending queued and steering messages use a desktop management surface with explicit discard and edit actions rather than the TUI interrupt-drain interaction.
- Prompt-history recall uses a picker above the composer.
- Existing desktop question and approval components should be reused, with navigation across multiple pending prompts.
- Existing desktop attention notifications should be reused.
- Slash commands should not be copied literally. Their capabilities need a separate desktop-affordance design pass.
- Rollback/fork should become a per-message desktop affordance rather than the TUI Esc-Esc picker.
- Chat chrome includes runtime activity, model, context usage, session identity, compaction, and goal state. Git branch was excluded from the original chat header decision.

## Claude Proposals That Were Not Ratified As Product Contracts

- Exact route shape: `/sessions/:sessionId/chat`.
- A new `runtime.editQueuedUserMessage` RPC preserving FIFO position.
- Desktop remapping of TUI visibility classes so diagnostic entries become collapsed expandable rows.
- Exact prompt-history picker dimensions and keyboard behavior.
- Rehydrating a newest page plus a focused page as the desktop recovery model.
- A seven-ticket client sequence named C1-C7 and a server prerequisite named S1.
- Exact placement of every runtime state in header, footer, transcript, toast, or overlay.
- The claim that `import.meta.env.DEV` alone guarantees the surface is absent from production bundles.
- The claim that all required remote APIs were already present.

These are candidates for discussion, not implementation instructions.

## Earlier Ticket Shape

The abandoned draft proposed:

1. Add queue-item editing to the server contract.
2. Add desktop session/transcript/runtime RPC adapters and typed schemas.
3. Add the chat route, transcript rendering, cursor paging, and reconnect recovery.
4. Add the composer and prompt-history UI.
5. Add queue management.
6. Add inline questions and approvals.
7. Add runtime/session chrome.
8. Connect chat-specific attention behavior.

This ordering remains a useful dependency sketch, but the July 18 Home Sessions destination and current contract changes require a new decomposition.

## Stale Assumptions

- KENT-196 is complete and merged. Its transcript work is no longer merely an in-progress dependency.
- The transcript contract changed repeatedly after July 5, including typed DTO boundaries, runtime read models, compaction ownership, draft recovery, tool diagnostics, and session hydration.
- The TUI session picker gained typed Sessions/Subagents categories, bounded cursor windows, promotion semantics, and explicit workspace retargeting.
- The desktop client still lacks session/chat adapters, but the exact server-gap claim must be re-audited feature by feature.
- The installed TanStack Virtual version does not expose the new first-class chat APIs documented by TanStack.
- The earlier task-detail-only entry model does not cover the requested Home Sessions destination.
- No desktop chat spec or implementation tasks were created.
- The failed Claude architecture review produced no verdict.

## Carry-Forward Rule

Recovered explicit decisions remain design inputs. When the expanded initiative conflicts with or supersedes them, the conflict must be resolved with the product owner before a spec or task records the new direction.

## Superseded On July 18

- The earlier decision to keep `Open in CLI` beside `Open Chat` was replaced: Task Detail uses `Open Chat`.
- The earlier task-run-only session scope was expanded to include creating ordinary interactive sessions.
- The earlier single in-app-route hosting scope was expanded to standalone route, native pop-out, adaptive master-detail, and sidebar/overlay presentations.
- The proposed global Home session population was rejected; session discovery is scoped through a selected project.
