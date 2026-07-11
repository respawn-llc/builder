# TUI Transcript And Interaction Spec

## Modes

- TUI modes are `ongoing` and `detail`, toggled by `Shift+Tab` or `Ctrl+T`.
- Ongoing is the default long-running mode.
- Detail is a fullscreen pager-style transcript overlay where input, queues, and pickers are hidden.
- Mode-toggle events are UI-ephemeral and are not persisted.

## Ongoing Mode

- Ongoing remains minimal: command start previews, file hint previews, lower-contrast syntax-highlighted shell previews, no thinking traces, no preambles, no outputs, and no diffs.
- **Ongoing does not own a transcript viewport or restore app-managed scroll. Native terminal scrollback owns committed history navigation.**
- Main UI startup stays in the normal buffer because ongoing-mode replay must remain visible in terminal scrollback.
- **ongoing normal-buffer history is append-only.** 
- **Once a transcript line is emitted into scrollback, it is immutable: no retroactive restyling, no in-place rewrites, no clear-and-replay, and no emitted-history reconciliation.**
- Compaction is same-session committed transcript progression, not a same-session transcript rewrite.
- User-visible transcript history is never truncated by compaction or handoff. It is durable on disk and served by streaming the persisted event log through a windowed projector (page or recent-tail), never by holding the full transcript in memory; the live conversation storage keeps only the bounded model working set.
- Latest-compaction boundary/floor is tail/model metadata only; detail paging and rendering ignore it.
- Rollback/fork is navigation or attachment to a different session target, not same-session transcript mutation.
- **Assistant streaming in ongoing mode uses source-backed Markdown promotion. Stable rendered assistant lines append during streaming after being styled as markdown; the volatile tail that cannot be styled deterministically stays in the mutable area until it can be promoted into immutable scrollback.**
- Runtime-control feedback rows that appear in the transcript are committed by the runtime. Runtime-backed clients must not emit optimistic or transient transcript echoes for those rows. No local transcript fallback exists; a committed-append failure surfaces as an error.
- Connectivity/subscription continuity loss discards transient live viewport immediately and recovers by hydrating authoritative committed transcript state.
- Transcript-affecting transport failures must not be swallowed or converted to fake empty/idle state.
- Terminal width changes after emitted transcript content exists, an event-seq gap or connection/subscription loss, and navigation/event buffering overflow trigger scratch rehydration through the same in-process session navigation/open hydration path used when the TUI opens a session. Height-only changes repaint the mutable normal-buffer area without scratch rehydration. Scratch rehydration erases only the mutable normal-buffer area, requests the active transcript segment since the latest compaction boundary or conversation start, and appends the hydrated chunk below existing immutable scrollback.
- Scratch rehydration never restarts the TUI process, clears immutable scrollback, compares against emitted lines, or suppresses duplicate-looking output.
- Scratch rehydration emits committed assistant final answers from their full saved text, preserving multi-line answer content. The compact ongoing assistant preview rule applies to live/normal rendering, not to scratch rehydration of already-final assistant answers.
- Assistant finalization while native streaming is active matches by the committed entry's carried stream/step identity and compares only against the in-memory stream source. If the final saved assistant text extends the streamed source, ongoing emits only the missing suffix and finalizes; if it does not and there was no real connection gap, the TUI treats it as an invariant/API failure rather than rehydrating.
- **Pending tool-call activity lives only in the volatile live region. When tool execution is in progress and no tool completion event has been issued, the tool calls render a loading spinner in a separate terminal area that is refreshed every frame to enable animations, but when tools are completed, they use regular `steer` path to get committed to permanent native terminal scrollback.**
- Messages in TUI use icon-like, single-symbol glyphs: `@` for web search, `§` for reviewer status/suggestion entries, `⇄` for file edits (edit/patch tools), `$` for shell tool calls, `⚠` for all warnings, `!` for all errors, `ℹ` for all ongoing-visible neutral notices (such as goal, worktree messages), and `?` for questions.
- **Pending tool-call previews in live region use the same rendering/layout as committed tool-call previews, with no pending-only labels.**
- **Tool completion appends exactly one final committed line in server emission order. Ongoing never recolors/mutates an earlier emitted tool line.**
- **A shell invocation that moves to the background renders both its committed tool row and volatile background-activity row with a secondary `$`, faint foreground command, and `• backgrounded` suffix. The command truncates before the suffix so the complete suffix remains visible whenever the terminal can fit it; these rows never label the state as `running`.**
- **Parallel tool calls commit in server emission order with no ordering guarantee among concurrent calls.**
- **In main-input mode, `Up`/`Down` are reserved for prompt-history recall at whole-buffer boundaries or multiline cursor movement. They do not scroll ongoing transcript.**
- `PgUp`/`PgDn` also do not scroll ongoing transcript state.
- **Ongoing mouse capture is disabled to preserve native text selection.**
- **Ongoing mode never enables terminal alternate-scroll `?1007`.**

## Detail Mode

- Detail mode is an expandable transcript inspector.
- Collapsed detail is default presentation mode.
- User and assistant messages show at most the first 3 rendered lines when collapsed.
- Tool calls show the same first input line used by ongoing previews.
- Detail projects only completed tool results. The result row carries the typed input metadata and output; raw persisted tool-call input records are not shown.
- Ask-question entries show only the question when collapsed.
- Known developer/context reminders use typed compact labels.
- Expanding reveals full entry content verbatim.
- Detail compact labels are metadata-first. Runtime/client projection preserves source message type, source path, compact content/label, and tool presentation metadata.
- Unknown roles, unknown message types, and invalid/missing metadata remain visible and expandable when recoverable text exists.
- Detail tool calls with error results stay collapsed by default but may show compact input plus structured error summary.
- Detail scrolling is line-oriented.
- `Up`/`Down` move by one rendered line when viewport can scroll.
- `Enter` toggles the selected expandable entry's expansion; it is a no-op for a non-expandable selection. `PgUp`/`PgDn` scroll by a viewport page; `Tab` inside detail returns to ongoing mode.
- Raw wheel movement and terminal alternate-scroll cursor keys follow the same one-line state machine as `Up`/`Down`.
- `PgUp`/`PgDn` pass a signed viewport-page delta through compact detail's generic scroll operation: they move the camera by that delta and select the center owner when possible; at a camera edge they attempt visible selection movement using that same signed delta before requesting an adjacent cursor page.
- The detail transcript is a bounded window over the session transcript: scrolling at the loaded edge requests the adjacent page; no full-transcript load ever occurs.
- In the scrollable interior, line, page, wheel, or alternate-scroll movement moves the camera and selects the visible selectable item nearest viewport center.
- At the top or bottom camera edge, line movement continues through visible selectable items beyond the center anchor. Reverse movement walks an off-center selection back to the center anchor before camera scrolling resumes.
- Detail requests an adjacent cursor page only after movement in that direction cannot move either the loaded camera or the visible local selection.
- A transition into detail renders the same-session cached bounded page and UI-local detail position immediately, then requests the newest page without a cursor.
- When the cached window reaches the newest edge, the refreshed newest page preserves the response-time selection and camera only if the selected committed row has one unambiguous surviving identity match. Otherwise, the refreshed page anchors the camera and selection at its end.
- When the cached window has newer content beyond its bottom edge, the refreshed newest page replaces it and anchors the camera and selection at the refreshed page's end.
- Session-target replacement clears the previous session's cached bounded page and UI-local detail state before hydrating the new target.
- Tall expanded entries remain selected while their body crosses the center anchor.
- Detail rows do not use dedicated collapsed/expanded glyphs. First rendered line keeps normal role/tool symbol; continuations use faint tree guides.
- Compact detail replaces selected expandable item's role symbol with `▶` or `▼`. The affordance is selected-only.
- The selected expansion affordance inherits the replaced role symbol's semantic foreground and faintness.
- Detail status line mirrors selected action as `Enter to expand` or `Enter to collapse`.
- Detail items use blank-line role-group separators. Consecutive tool rows form dense chunks.
- The selected lens extends through its adjacent visual spacer lines. Selection spacers are render-only and do not change transcript line ownership, viewport scroll, selection, or paging.
- Detail reserves a one-cell lens rail before transcript content. Unselected lines use a blank rail cell; every line owned by the selected item uses the primary `▎` rail and `App.ModeBg` content fill, except added/removed patch lines whose diff background takes priority over the lens fill.
- The rail owns the first terminal cell at every viewport width. A one-cell viewport shows only the selected rail or unselected blank rail; wider viewports render transcript content within the remaining cells using normal truncation.
- Detail selection does not change semantic foreground colors. Expanded detail entries render content and role symbols full-strength while retaining other semantic styling over the resolved lens or diff background; continuation guides remain faint structural chrome.
- Detail loads stale bounded cursor pages from the server. Scrolling at a loaded edge requests the adjacent page, which may prepend or append server-backed page membership. Runtime and live transcript events never self-update, append to, reconcile, or refresh that membership.
- An adjacent-page request shows a request-scoped `info` status notice using replace delivery and the request UUID as its notice identity. A later notice may replace it and it does not resurface. Matching completion clears only that loading notice; matching failure replaces it through the ordinary error-notice path. The selected expansion action remains the independent detail help-slot segment.
- Detail owns only UI-local expansion, selection, and scroll state.
- Mid-step entries are absent until a loaded page contains their committed snapshot.
- Detail rendering is a flat continuous stream with no grouped sections.
- Step-end markers appear in detail only.
- Detail transcript overlay always uses terminal alt-screen `?1049`.
- Detail does not enable terminal mouse capture.
- Detail may enable alternate-scroll `?1007` only while active and must disable it on exit.
- Full-screen overlay surfaces (`/status`, `/goal`, `/worktree`, `/ps`) follow the same rule as detail: they enable alternate-scroll `?1007` while active and disable it on exit, regardless of which transcript mode they were opened from, so the mouse wheel scrolls overlay content.
- Rollback/edit picker uses detail rendering inside alt-screen but does not enable alternate-scroll and ignores mouse events.

## Transcript Visibility

- Transcript visibility is defined by one product matrix, not ad hoc filters.
- Visibility is projection metadata from the runtime output stream, not a separate conversation mutation path.
- Visibility values are `O` full ongoing+detail, `OC` collapsed/short ongoing plus full detail, `D` detail-only, and `X` hidden.
- Unknown/malformed entries with recoverable text are `O`; empty unknown/malformed entries are `D` diagnostics.
- Locked message-type visibility:
- `agents.md`: `D`
- `skills`: `D`
- `subagents`: `D`
- `environment`: `D`
- `compaction_summary`: `O`, using compact label in ongoing/collapsed detail and full summary on expansion.
- `interruption`: `O`
- `error_feedback`: `O`
- `compaction_soon_reminder`: `D`
- `reviewer_feedback`: represented by reviewer transcript roles, effective `OC` or `O` depending on reviewer verbosity.
- `background_notice`: `OC`
- `custom_tool_call_output`: follows the tool call/result row it belongs to.
- `handoff_future_message`: `D`
- `manual_compaction_carryover`: `D`
- `headless_mode`: `D`
- `headless_mode_exit`: `D`
- `workflow_mode`: `OC`
- `worktree_mode`: `O`
- `worktree_mode_exit`: `O`
- `goal`: `O`
- Thinking-level feedback from `/thinking` is not rendered as a transcript row in ongoing or detail. The TUI surfaces thinking level through the status-line model label/reasoning segment instead of neutral transcript notices.
- Locked non-message roles:
- user turns: `O`
- final assistant turns: `O`
- assistant commentary/thinking turns: `D`
- tool calls: `OC`
- reviewer suggestions/status: `OC` or `O`
- reasoning-summary progress updates: `D`; their live first bold span is projected into the status-line reasoning slot while the model is reasoning. Ongoing scrollback contains neither reasoning-summary rows nor assistant commentary/thinking rows.
- Runtime projection decides whether persisted/runtime messages become transcript entries and which role they use.
- TUI rendering decides how transcript roles behave in ongoing and detail.
- When a concept already has a dedicated transcript role, do not also render its raw developer/request artifact.

## Rendering Pipeline

- Transcript rendering stages are ordered: content render, low-level semantic transform, wrap, line layout, final decoration.
- Chroma owns syntax foregrounds and text attributes through `catppuccin-latte` in light mode and `onedark` in dark mode. Shell commands, file-read source results, and structured patch source share the same syntax projection path; Markdown formatting remains independent.
- Transcript rendering owns role styling, faint shell preview styling, and diff semantics.
- Layout owns prefixes, indentation, and wrapping only.
- Semantic color tokens are centralized in `shared/theme`.
- Syntax-highlighted output must not emit backgrounds unless explicitly intended, such as diff add/remove decoration.
- Formatted text uses app foreground as base text color.
- Faint text always uses the transcript foreground token plus the terminal faint attribute; there is no separate subdued/gray transcript foreground token.
- User turns render their full submitted text in ongoing, including multiline prompts that invoke slash commands. Final assistant turns render their full text in ongoing. User and assistant rows use compact text in collapsed detail and full text in expanded detail, with foreground text plus Markdown styling.
- Shell tool calls use the shared Chroma syntax projection, faint styling, and OS-dependent shell syntax selection.
- Non-shell tool calls use foreground text, no syntax highlighting, and faint styling.
- Patch/edit tools use `⇄` in ongoing, detail, and native replay. Patch paths and neutral text use foreground; source lines use the shared Chroma syntax projection; diff add/remove counts use semantic add/remove colors. Diff-line backgrounds blend 20% of the Success/Error token over the active detail surface background.
- Compaction summaries and manual compaction carryover use secondary text.
- Handoff future-agent context rows use the faint foreground system-notice style.
- Goal-related rows use primary text.
- Workflow-related rows use primary text and `OC` visibility.
- Worktree-enter rows use the faint foreground system-notice style.
- Worktree-exit rows use full-strength foreground text.
- `subagents` developer-context rows use the faint foreground system-notice style.
- Supervisor/reviewer-related non-error rows use success text. Supervisor/reviewer error rows use error text.
- Cache warnings and non-interrupting warnings use warning text.
- Error rows use error text, including interruption rows.
- Background shell completion notices use full-strength foreground text and remain separate from shell tool call/result rows.
- Moving a shell to the background ends its mutable live-tool presentation. The backgrounded tool row remains in immutable ongoing scrollback, and completion is represented by a separate immutable notice.
- The rendering matrix applies to ongoing and detail modes. Mode-specific compact/full rules may change which content is selected, but not the semantic style roles for the selected content.
- Role symbols/icons are styled independently from row body text when specified: successful tool-call symbols use success, shell tool-call symbols with raw output requested use warning, shell invocations moved to the background use secondary, failed tool-call symbols use error, supervisor/reviewer symbols use the row's success/error color, compaction symbols use secondary, goal symbols use primary, workflow symbols use primary, and background shell completion symbols use primary regardless of exit status; clients never infer status from display text. Warning symbols use warning, and error symbols use error. Unspecified symbol color behavior requires an explicit spec decision before implementation.
- Tool previews are input-first. Shell previews show the typed command from tool metadata. Patch/edit previews show structured patch paths and diff add/remove counts or lines. Other tool previews show typed compact/input metadata. Tool result summaries and error summaries do not replace the input preview.
- Tool-call errors in ongoing and detail keep the failed tool input visible with an error-colored symbol. Patch/edit errors render the patch/edit input shape, including file path and diff add/remove lines when structured patch metadata exists, instead of replacing the row with only error text.
- No timestamps are shown in UI.
- Streaming paint cadence is 16ms with token coalescing per flush tick.
- Main status line is compact and fixed: activity indicator, optional git branch, model label, process metadata, transient warning, and right-aligned context meter. Composition, priority, and notice semantics are owned by tui-status-line.md.
- Goal mode does not add persistent goal text. The primary-blue `goal` progress word is visible when the runtime goal SSOT reports an active goal, including at startup, between goal-loop turns, or while runtime-local suspended. Paused, completed, and cleared goals do not show the indicator. Reviewer and compaction indicators keep precedence over goal because they describe immediate blocking activity.
- Context meter is a 10-char bar plus `% ctx window`, green/yellow/red at `<50%`, `50-<80%`, `>=80%`.

## Input And Queueing

- Kent input fields use one shared editor implementation with a real terminal cursor by default across ongoing and alt-screen surfaces.
- `InputField.Render(width)` owns rendered lines and cursor coordinates; callers must not splice unwrapped content into those lines.
- Fallback to soft cursor is allowed only for verified cursor drift, wrap mismatch, or alt-screen corruption that cannot be solved in the renderer adapter.
- Startup/onboarding/project/worktree input fields use `cli/tui/input.Editor` and `cli/tui/input.Field`, not Bubble `textinput.Model`, app-local wrappers, or additional text-input components.
- In-turn user messaging queues typed steering intents for later safe-boundary delivery and supports queued post-turn send.
- Queue/send hotkey is `Tab`; `Ctrl+Enter` is a compatibility alias.
- Known `Ctrl+Enter` CSI encodings normalize to the same queue action.
- Clipboard paste hotkeys are `Ctrl+V`, `Ctrl+D`, `Alt+V`, and `Alt+D`; explicit system clipboard reads save images to temporary PNG files and insert the path, or insert text at the active cursor. Terminal bracketed paste remains ordinary text input and never causes a system clipboard read.
- Mid-run steering is soft-insert only at safe boundaries after current tool completion.
- Steering submissions never lock the input box; each `Enter` while busy queues another steering message.
- Pending steering and pending user messages are strict FIFO.
- Live-band queued inputs use secondary/faint styling; live-band steering inputs use primary styling.
- Multiple queued user steering messages flushed at one boundary coalesce into one user message separated by blank lines.
- Pending queues are unbounded and in-memory only.
- Injected mid-run messages persist only on delivery boundary.
- Ctrl+C interrupt is turn-local: stop current model step and active tool process, keep app/session alive.
- Interrupt injects detail-only developer-role control message `User interrupted you`.
- Post-interrupt state returns idle with input ready.
- Resume after interrupt requires explicit user text.
- Crash recovery is bifurcated: mid-step crash resumes via interrupt flow; otherwise restore normal state.
- Failed prompt-history navigation emits plain terminal BEL with no transient UI notification.

## Path Autocomplete

- Main-input `@` path autocomplete uses a cached repo-relative corpus built asynchronously from `rg --no-config --files -0 --hidden -g '!.git'`.
- Corpus prewarming starts through Bubble Tea startup commands, not unmanaged constructor goroutines.
- Live matching never shells out per keystroke.
- Query tracking is cursor-local and accepts Unicode letters/digits plus `/`, `.`, `_`, and `-`.
- Hidden paths are included, `.git` is excluded, and normal ignore-file handling remains enabled.
- Non-empty directory candidates are derived from file paths; empty directories are intentionally excluded in v1.
- Corpus-build failures are retryable later in the same workspace and do not permanently disable path autocomplete.

## Startup And Session Selection

- Startup shows recent sessions with pick-or-new flow.
- Startup session list is scrollable with no cap.
- If no sessions exist, startup goes directly to new-session setup.
- When CLI startup cwd does not resolve to a registered project/workspace/worktree, startup enters project picker/registration instead of auto-registering.
- That flow may create a project and attach current workspace as first workspace/main worktree, or attach current workspace to an existing project.
- Outside that flow, CLI remains workspace-first.
- When a session selected from the picker has stored workspace root different from current root, startup shows `Workspace changed` confirmation. `Yes` retargets; `No` returns to picker.

## Worktree Management

- Worktree-management product language uses `workspace`, not `repo`.
- `/worktree` management keeps session identity stable and changes only execution target `(workspace_id, worktree_id?, cwd_relpath)`.
- First `/worktree` slice has no separate teleport-root abstraction.
- `/worktree`, `/worktree new`, and `/worktree create` enter one smart-target create dialog. Raw `/worktree create <branch> [path]` bypass is unsupported.
- Create dialog auto-suggests target name only from sanitized session name. It does not fall back to current branch, main, or generic placeholder.
- Create dialog has no explicit new/existing selector. Kent resolves typed `Branch or ref` asynchronously and shows `new branch`, `existing branch`, or `detached ref`.
- `Branch or ref` appears before `Base ref`. `Base ref` defaults to `HEAD` and is required only for new branch creation.
- Worktree transitions store the latest pending typed developer-context steering intent and materialize it at normal steering priority before the next user/model turn.
- Worktree transitions do not append synthetic transcript notes.
- Git remains source of truth for topology. Kent stores additive metadata and blocks deleting worktrees still targeted by another session.
- Existing non-Kent git worktrees remain manageable and should be visually marked where feasible.
- Supported aliases preserve safety semantics: `/worktree status`, `/worktree ls`, `/worktree remove`, `/worktree rm`.
- Worktree delete is rebind-first cleanup and is blocked while background shell processes still run under that worktree.
- Branch cleanup is conservative/best-effort. Kent only auto-attempts branch deletion when provenance proves it created the branch. Force delete is not part of the first slice.
- New worktrees default under `worktrees.base_dir`, rooted under Kent persistence state by default.
- Live worktree retarget rebinds runtime-local tool handlers to the new effective root.
- Optional post-create setup script is `worktrees.setup_script`, runs as a blocking part of every new worktree creation path, and receives args/stdin JSON/env. Blocking setup prevents sessions and workflow runs from locking context before setup-provided local skills, docs, or other worktree files are present.
- Worktree setup progress is live client operation state, not a model-visible transcript entry and not durable transcript history.
- Manual worktree creation switches the current session to the new worktree only after setup succeeds. Setup failure leaves the session on its previous worktree, keeps the created worktree available for inspection or manual repair, and surfaces a foreground error.
- Worktree setup timeout is configured by `worktrees.setup_timeout_seconds`. The default is 60 seconds. Unset timeout uses the default; a configured timeout of zero or less disables the timeout.

## Slash Commands

- Leading slash enters command mode when first non-space char is `/`.
- Picker matches only first token and updates continuously.
- After whitespace, command enters argument mode and picker hides.
- `Enter` runs the selected slash command, including default first match for partial input.
- `Tab` on partial selected command autocompletes it and inserts trailing space.
- Unknown slash commands are sent to the model as normal user prompts.
- Built-ins: `/logout`, `/login`, `/exit`, `/new`, `/resume`, `/compact`, `/name`, `/thinking`, `/fast`, `/review`, `/init`, `/supervisor`, `/autocompaction`, `/questions`, `/status`, `/goal`, `/ps`, `/worktree` (alias `/wt`), `/copy`, `/back`.
- Exact known slash commands use the normal queued-input drain path when queued; they are never sent as plain user prompts.
- Run-safe commands execute immediately while busy.
- Non-run-safe known commands while busy are rejected with transient status-line error.
- `/copy` copies latest committed assistant `final_answer` and stays hidden until one exists.
- `/review` auto-submits embedded review rubric; it stays in-place for empty sessions and forks fresh child session after a visible user prompt.
- `/back` reopens parent session when available.
- `/supervisor` toggles current-session reviewer invocation and does not persist to config.
- `/autocompaction` toggles runtime auto-compaction for current session and does not persist to config.
- `/fast`, `/supervisor`, and `/questions` toggle feedback is a committed runtime transcript entry in runtime-backed sessions.
- `/status` opens a read-only detail overlay and refreshes progressively.
- Built-in prompt commands use embedded Markdown templates.
- File-backed prompts come from local/global `.kent/prompts` and `.kent/commands`; scan is non-recursive `.md`, namespace precedence is local over global and prompts over commands.
- File command ID is `prompt:<filename-without-extension>` and submits file content verbatim as user message.

## Notifications

- Ring terminal bell when a new `ask_question` is shown.
- Ring on turn end only if the turn executed at least two tool calls.
- Turn-end notification is deferred until queued prompt drain is fully idle.
- Turn-end text includes assistant preview when available, else `<session title>: turn complete`.
- Ask notifications include `<session title>: Question: <question>` or `<session title>: Action required: <question>`.
- `auto` notification method prefers OSC 9 on supported terminals and falls back to BEL.
- OSC 9 notifications still emit a separate BEL.
- OSC 9 is disabled when `WT_SESSION` is set.

## Reviewer

- Post-turn reviewer exists behind config and defaults to `reviewer.frequency = "edits"`.
- Reviewer runs only after completed assistant final handoff and only if the turn executed at least one tool call.
- Reviewer uses more aggressive tool-output truncation than the main-agent path.
- Reviewer contract is minimal JSON `{"suggestions":["..."]}`; invalid payloads are ignored non-fatally.
- If suggestions exist, runtime appends them as developer message and runs one extra main-agent follow-up pass.
- Follow-up noop token is exact `NO_OP`; if emitted, runtime keeps original assistant final answer.
- Reviewer pass is single-shot with no recursive review.
