# Product Specs

These docs are the authoritative, searchable product-decision source for Kent agents.
Implementation can drift from them.

Before writing, rewriting, reviewing, or validating a spec, follow the
[spec-writing skill](../../../.kent/skills/spec-writing/SKILL.md).

Area specs:

- `core-runtime-tools.md`: product scope, sessions, authentication, configuration, tools, headless operation, and compaction.
- `project-workspaces.md`: Project-workspace relationships, detach safety, API selection, and CLI detach/default contracts.
- `tui-transcript.md`: terminal modes, transcript visibility, rendering, input, slash commands, worktrees, notifications.
- `tui-startup.md`: interactive TUI launch, authentication, project binding, session selection, and attach behavior.
- `tui-chat-core.md`: main-input composer — editing model, prompt history, path autocomplete UX, queue/steering pane, interrupts, clipboard paste.
- `tui-status-line.md`: main-surface status bar — space-priority ladder, activity indicator, segments, notice system.
- `tui-slash-overlays.md`: overlay surfaces — shared conventions, `/status`, `/goal`, `/ps`, `/worktree` UX, rollback picker.
- `tui-onboarding.md`: first-time setup wizard, navigation, completion, and cancellation.
- `tui-terminal-environment.md`: resize, too-small guard, theming, color roles, terminal control modes.
- `tui-ask-prompts.md`: agent question/approval prompt UI — lifecycle, prompt kinds, keys.
- `workflow-orchestration.md`: workflow behavior, task lifecycle, execution, concurrency, worktrees, search, and CLI contracts.
- `desktop-gui.md`: desktop navigation, projects, workflow boards, task detail, connection loss, and native capabilities.
- `workflow-editor.md`: workflow editor, draft editing, library/linking, sidebar, and save/conflict decisions.
- `release-distribution.md`: supported releases, installers, update channels, and compatibility behavior.
- `terminology.md`: domain terms used consistently across specs, product surfaces, and public contracts.
