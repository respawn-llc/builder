# Desktop Sessions And Chat Initiative

This dossier supports product design, contract analysis, and task decomposition for bringing interactive Kent sessions and chat to the desktop app.

## Authority

- Product and architecture decisions are authoritative only after they are recorded in `docs/dev/specs/`.
- This folder contains research, recovered context, inventories, and unresolved design work. It does not override a spec.
- Session-local interview notes may live under `docs/tmp/desktop-chat/`; decisions must be promoted out of scratch notes after each interview batch.
- Implementation plans belong to the Kent task that owns the implementation.

## Desired Outcome

The initiative is intended to let desktop users discover resumable sessions, open a session chat, inspect bounded transcript history, observe live runtime activity, and control the session without depending on terminal-shaped interaction.

The July 18 request expands the earlier task-detail-only concept to include a Sessions destination on Home and a deliberate desktop replacement for slash-command-driven capabilities.

The dossier covers the complete implementation with 100% TUI capability parity. Task boundaries describe dependency order only; they do not define an MVP or optional parity backlog.

## Working Documents

- [Recovered Claude work](./recovered-claude-work.md) distinguishes prior user decisions from Claude proposals and stale assumptions.
- [Current feature inventory](./feature-inventory.md) records the shipped TUI/server behavior and the existing desktop substrate.
- [Contract gap analysis](./contract-gap-analysis.md) separates reusable server contracts from product decisions and missing API surfaces.
- [Respawn chat reference](./respawn-reference.md) records reusable layout/state patterns and mobile-specific behavior to avoid.
- [Question map](./question-map.md) orders the design interview so dependent decisions are resolved before implementation tasks are filed.
- [Transcript item audit](./transcript-item-audit.md) lists every active transcript/live classification for individual visibility and collapse decisions.

## Delivery Sequence

1. Reconcile recovered decisions with the expanded Sessions/Home direction.
2. Complete the feature and contract inventories.
3. Resolve the question map with the product owner.
4. Record ratified decisions in a desktop chat/session spec.
5. Decompose the complete approved design into independently shippable Kent tasks without dropping capabilities.
6. Order tasks by contract, UI-kit, navigation, data, interaction, and integration dependencies.
