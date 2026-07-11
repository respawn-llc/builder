# Product Specs

These docs are the authoritative, searchable product-decision source for Kent agents.

Rules:

- Specs contain product and architecture decisions only.
- Implementation plans, progress logs, checklists, audits, and temporary research notes do not belong here.
- Later decisions override older decisions when current code also backs them up.
- Missing code is not evidence that a decision was removed; implementation drift is possible.
- Track unimplemented product work in GitHub issues and implementation cleanup in `docs/dev/techdebt/techdebt.md`, not in temporary migration review files.

Area specs:

- `core-runtime-tools.md`: product scope, architecture boundaries, sessions, auth, config, tools, headless mode, and compaction.
- `tui-transcript.md`: terminal modes, transcript visibility, rendering, input, slash commands, worktrees, notifications.
- `tui-startup.md`: interactive TUI launch — server attach, auth gate, pickers, project binding, session selection, startup surface architecture.
- `tui-chat-core.md`: main-input composer — editing model, prompt history, path autocomplete UX, queue/steering pane, interrupts, clipboard paste.
- `tui-status-line.md`: main-surface status bar — space-priority ladder, activity indicator, segments, notice system.
- `tui-slash-overlays.md`: overlay surfaces — shared conventions, `/status`, `/goal`, `/ps`, `/worktree` UX, rollback picker.
- `tui-onboarding.md`: first-time setup wizard — step graph, keys, server-side finalize.
- `tui-terminal-environment.md`: resize, too-small guard, theming, color roles, terminal control modes.
- `tui-ask-prompts.md`: agent question/approval prompt UI — lifecycle, prompt kinds, keys.
- `workflow-orchestration.md`: workflow domain, scheduler/runtime behavior, persistence, schema, CLI, and workflow Q/A.
- `desktop-gui.md`: desktop GUI stack, Home/board/task-detail behavior, workflow MVP, native bridge, connection loss, and GUI Q/A.
- `workflow-editor.md`: workflow editor, draft editing, library/linking, sidebar, and save/conflict decisions.
- `release-distribution.md`: release, installer, and distribution decisions.
- `terminology.md`: DDD terms agents should use in specs, code names, APIs, and UI.
