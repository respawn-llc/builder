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
- **Once a transcript line is emitted into scrollback, it is immutable: no retroactive restyling, no in-place rewrites, no clear-and-replay, and no full-buffer re-emission to paper over same-session divergence.**
- Compaction is same-session committed transcript progression, not a same-session transcript rewrite.
- User-visible transcript history is never truncated by compaction or handoff. It is durable on disk and served by streaming the persisted event log through a windowed projector (page or recent-tail), never by holding the full transcript in memory; the live conversation storage keeps only the bounded model working set.
- Latest-compaction boundary/floor is tail/model metadata only; detail paging and rendering ignore it.
- Rollback/fork is navigation or attachment to a different session target, not same-session transcript mutation.
- **Assistant streaming in ongoing mode uses source-backed Markdown promotion. Stable rendered assistant lines append during streaming after being styled as markdown; volatile tail that cannot be styled deterministically stays in live viewport and will be rewritten with styled content on permanent scrollback commit. This is necessary to prevent overflow of live area beyond screen height (which makes it forcibly immutable per TUI limitations and breaks scrollback).**
- Runtime-control feedback rows that appear in the transcript are committed by the runtime. Runtime-backed clients must not emit optimistic or transient transcript echoes for those rows; local transcript fallback is limited to sessions without a runtime client or committed-append failures.
- Connectivity/subscription continuity loss discards transient live viewport immediately and recovers by hydrating authoritative committed transcript state.
- Transcript-affecting transport failures must not be swallowed or converted to fake empty/idle state.
- External continuity-loss recovery may re-issue the ongoing buffer from authoritative committed state, only allowed on connection loss.
- Client-side transcript divergence from deduplication, ordering, overlap, or pagination bugs is not an acceptable redraw case.
- **Pending tool-call activity lives only in the volatile live region. When tool execution is in progress and no tool completion event has been issued, the tool calls render a loading spinner in a separate terminal area that is refreshed every frame to enable animations, but when tools are completed, they use regular `steer` path to get committed to permanent native terminal scrollback.**
- Messages in TUI use icon-like, single-symbol glyphs: `@` for web search, `§` for reviewer status/suggestion entries, `⇄` for file edits (edit/patch tools), `$` for shell tool calls, `⚠` for all warnings, `!` for all errors, `ℹ` for all ongoing-visible neutral notices (such as goal, worktree messages), and `?` for questions.
- **Pending tool-call previews in live region use the same rendering/layout as committed tool-call previews, with no pending-only labels.**
- **Tool completion appends exactly one final committed line in transcript order. Ongoing never recolors/mutates an earlier emitted tool line.**
- **Parallel tool calls commit through a stable frontier: later completed calls remain live until all earlier pending calls are ready.**
- **In main-input mode, `Up`/`Down` are reserved for prompt-history recall at whole-buffer boundaries or multiline cursor movement. They do not scroll ongoing transcript.**
- `PgUp`/`PgDn` also do not scroll ongoing transcript state.
- **Ongoing mouse capture is disabled to preserve native text selection.**
- **Ongoing mode never enables terminal alternate-scroll `?1007`.**

## Detail Mode

- Detail mode is an expandable transcript inspector.
- Collapsed detail is default presentation mode.
- User and assistant messages show at most the first 3 rendered lines when collapsed.
- Tool calls show the same first input line used by ongoing previews.
- Ask-question entries show only the question when collapsed.
- Known developer/context reminders use typed compact labels.
- Expanding reveals full entry content verbatim.
- Detail compact labels are metadata-first. Runtime/client projection preserves source message type, source path, compact content/label, and tool presentation metadata.
- Unknown roles, unknown message types, and invalid/missing metadata remain visible and expandable when recoverable text exists.
- Detail tool calls with error results stay collapsed by default but may show compact input plus structured error summary.
- Detail scrolling is line-oriented.
- `Up`/`Down` move by one rendered line when viewport can scroll.
- After line, page, wheel, or alternate-scroll movement, compact detail selects the visible selectable item nearest viewport center.
- Tall expanded entries remain selected while their body crosses the center anchor.
- Detail rows do not use dedicated collapsed/expanded glyphs. First rendered line keeps normal role/tool symbol; continuations use faint tree guides.
- Compact detail replaces selected expandable item's role symbol with `▶` or `▼`. The affordance is selected-only.
- Detail status line mirrors selected action as `Enter to expand` or `Enter to collapse`.
- Detail items use blank-line role-group separators. Consecutive tool rows form dense chunks.
- Detail selection uses full-width selected background/fill only and does not change foreground colors.
- Detail is a live transcript view with UI-local expansion and selection. Transcript changes update content while scroll/anchor stays stable unless the user navigates.
- Mid-step entries show latest completed snapshot only.
- Snapshot scope is the full session transcript up to latest completed step.
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
- `environment`: `D`
- `compaction_summary`: `O`, using compact label in ongoing/collapsed detail and full summary on expansion.
- `interruption`: `O`
- `error_feedback`: `O`
- `compaction_soon_reminder`: `D`
- `reviewer_feedback`: represented by reviewer transcript roles, effective `OC` or `O` depending on reviewer verbosity.
- `background_notice`: `OC`
- `handoff_future_message`: `D`
- `manual_compaction_carryover`: `D`
- `headless_mode`: `D`
- `headless_mode_exit`: `D`
- Locked non-message roles:
- user turns: `O`
- assistant turns: `O`
- tool calls: `OC`
- reviewer suggestions/status: `OC` or `O`
- Runtime projection decides whether persisted/runtime messages become transcript entries and which role they use.
- TUI rendering decides how transcript roles behave in ongoing and detail.
- When a concept already has a dedicated transcript role, do not also render its raw developer/request artifact.

## Rendering Pipeline

- Transcript rendering stages are ordered: content render, low-level semantic transform, wrap, line layout, final decoration.
- Formatter config owns syntax backgrounds and formatter base foreground.
- Transcript rendering owns role styling, subdued shell preview styling, and diff semantics.
- Layout owns prefixes, indentation, and wrapping only.
- Semantic color tokens are centralized in `shared/theme`.
- Syntax-highlighted output must not emit backgrounds unless explicitly intended, such as diff add/remove decoration.
- Formatted text uses app foreground as base text color.
- Patch/edit tools use `⇄` in ongoing, detail, and native replay, inheriting result-state color.
- Detail shell commands use full syntax color. Ongoing shell commands are subdued.
- No timestamps are shown in UI.
- Streaming paint cadence is 16ms with token coalescing per flush tick.
- Main status line is compact and fixed: activity indicator, optional git branch, model label, process/server metadata, transient warning, and right-aligned context meter.
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
- Clipboard image paste hotkeys are `Ctrl+V` and `Ctrl+D`; they save clipboard images to temp PNG files and insert the path.
- Mid-run steering is soft-insert only at safe boundaries after current tool completion.
- Steering submissions never lock the input box; each `Enter` while busy queues another steering message.
- Pending steering and pending user messages are strict FIFO.
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
- Optional post-create setup script is `worktrees.setup_script`, runs async only after new worktree creation, receives args/stdin JSON/env, and failures do not undo worktree/session switch.

## Slash Commands

- Leading slash enters command mode when first non-space char is `/`.
- Picker matches only first token and updates continuously.
- After whitespace, command enters argument mode and picker hides.
- `Enter` runs the selected slash command, including default first match for partial input.
- `Tab` on partial selected command autocompletes it and inserts trailing space.
- Unknown slash commands are sent to the model as normal user prompts.
- Built-ins: `/logout`, `/exit`, `/new`, `/resume`, `/compact`, `/name`, `/thinking`, `/fast`, `/review`, `/init`, `/supervisor`, `/autocompaction`, `/status`, `/ps`, `/copy`, `/back`.
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
