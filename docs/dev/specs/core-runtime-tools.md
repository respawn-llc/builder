# Core Runtime And Tools Spec

## Product Scope

- Kent is a professional coding agent focused on output quality, speed, long-running work, and transparent activity.
- Architecture stays composable and pluggable with low-friction extension points.
- Full-access execution is the v1 default; there is no default sandbox.
- The working CLI name is `kent` and should remain easy to rename.
- Public docs use Astro + Starlight from `docs/`, deploy as static Cloudflare Pages.

## Client/Server Boundary

- Server owns durable lifecycle state, lifecycle mutations, and canonical lifecycle event streams.
- CLI/TUI consumes client-facing DTOs and shared service clients. It does not own alternate durable/runtime lifecycle flows for embedded-local mode.
- Embedded-local mode adapts through the same loopback service/client boundary as remote/shared-server mode. Direct in-process engine, broker, process, auth, or project objects may exist only behind server-owned adapters.
- `shared/clientui` is a DTO/read-model boundary only. Runtime-event state transitions, pending-input policy, reasoning-stream presentation, activity transitions, transcript-sync commands, background notices, and prompt-history commands are owned by CLI packages.
- `shared/serverapi` is a wire-contract package only: serializable request/response DTOs, validation helpers, typed wire errors, stream/progress DTOs, and route-facing value contracts.
- Server-owned service interfaces, concrete service implementations, runtime handles, headless launchers, logging/timeout policy, lifecycle orchestration, and close/drop semantics must not live in `shared/serverapi`.
- In-process route service interfaces live in `shared/apicontract`. That package is the narrow loopback boundary for shared clients/server adapters and contains route-shaped interfaces with no execution policy.
- CLI production packages must not import `server/*` directly.
- User-visible lifecycle side effects trigger at one client-facing accepted-event boundary, not inside only one transport/runtime path.
- Any migration paths should be removed instead of preserved as compatibility shims. Breaking API/protocol changes are acceptable when documented and surfaced clearly.

## Skills And Generated Assets

- Skills are discovered from Kent-owned roots: `<persistence-root>/skills` (default `~/.kent/skills`), workspace `.kent/skills`, and generated embedded skills under `<persistence-root>/.generated/skills`. The global and generated roots follow the selected persistence root; only the workspace root stays workspace-relative. Global `AGENTS.md` and the global system-prompt file resolve under the same `<persistence-root>` (an empty value falls back to `~/.kent`).
- First-run onboarding may optionally symlink skills and slash-command roots from supported source tools into Kent's layout; runtime discovery still reads only Kent-owned directories.
- `config.toml` supports file-only `[skills]` boolean toggles for per-skill new-session enable/disable. Disabled skills remain visible in `/status` and only affect future skills-message injection.
- Preinstalled skills are seeded from binary-embedded deterministic assets under `prompts/skills/**` into `<persistence-root>/.generated/skills`.
- `<persistence-root>/.generated` is deterministic, destructible, overwritten on server startup, and not user-owned.
- Generated sync runs on server startup (`kent serve` or embedded server), not in clients.
- Edited/add/delete/rename/symlink/invalid-marker generated trees move to `<persistence-root>/recovered/<UTC timestamp>/.generated`, then regenerate.
- If `<persistence-root>/recovered` is non-empty, every new session gets a user-facing, non-model-visible warning asking the user to clean recovered files and not edit `.generated`.
- Generated skills are always seeded. Existing `[skills]` toggles only disable injection by normalized skill name.
- User skills with the same normalized name shadow generated skills.
- Generated skill validation rejects empty files, invalid frontmatter, duplicate generated names, and symlinks/non-regular entries.

## Core Tools

- Core tools are `shell`, `write_stdin`, `view_image`, `patch`, `ask_question`, and `trigger_handoff`.
- Goal management is CLI/runtime-owned. Kent must not add model-callable goal tools.

## Runtime Output Boundary

- Runtime owns one active conversation list/stateflow per session runtime.
- Runtime producers materialize conversation-facing output through `steer`.
- Runtime producers store delayed conversation-facing output through `queue`; queues store typed steering intents and flush through `steer`.
- A steering intent may contain one item or an ordered pack of items. Item order inside the pack is preserved.
- Steering items cover all transcript-visible, non-queued messages with exactly 1 exception: markdown line-by-line streaming of agent final_answer responses.
- Ordering, transcript visibility, ongoing/detail presentation, model visibility, dedupe, derived events, and post-persist state updates are steering policy, not separate append paths.
- pending user text that they steered coalesces at flush into one user message separated by blank lines; the coalesced message is a normal steering intent.
- Runtime events that do not create model-history items still route through the output boundary.
- History replacement is an output mutation owned by the same boundary. Normal additions after replacement use `steer`. Messages about handoffs emitted to TUI scrollback buffer still use steer due to the rule above. Only logical/algorithmic history replacement procedure does not use steering, all user-facing history replacement effects are still regular `steer` api usages.

## Command Execution

- `shell` is the only shell-command execution surface.
- Commands run in the user login shell, non-TTY mode, with direct shell invocation.
- Execution inherits parent environment and adds non-interactive hints and other technical environment variables.
- stdout/stderr merge into one stream without origin tags.
- Command lifetime is unlimited. `yield_time_ms` controls when Kent returns control and backgrounds the process.
- Non-zero exit is recoverable and does not auto-abort the turn.
- Shell process-launch failures are not automatically retried.
- Interrupt escalation is `SIGINT` then `SIGKILL` after 10 seconds.
- Command post-processing is Kent-owned, applied after execution, configured under `[shell]`, and bypassed by per-call `raw=true` parameter on the `shell` tool.
- `[shell].postprocessing_mode` uses `none | builtin | user | all`.
- The generic sanitizer runs before built-ins and hooks for every non-raw mode except `none`.
- Built-ins run before the optional user hook. A built-in halt stops later built-ins only.
- User hooks receive JSON stdin and return JSON stdout, receiving both original sanitized output and Kent's current processed output.
- Hook failures do not change the provider-facing command-output envelope.
- Background shell processes are server-global. Process IDs are server-global within one server instance; owner session metadata is advisory for routing notices (to both humans and models) and history, NOT access control.
- TUI `/ps` may surface and operate on background processes from other sessions in the same app instance.

## Patch And Image Tools

- `patch` apply is atomic: malformed/conflicting patches make no file changes.
- `patch` supports add, update, move, and delete.
- Patch targets are validated with real-path resolution.
- `patch` has no timeout and no automatic retries.
- Patch success persistence includes patch input plus apply-result metadata.
- Outside-workspace edits are approval-gated unless explicitly enabled. `allow_non_cwd_edits=false` by default.
- If outside-workspace approval is denied, Kent returns to the model an explicit non-circumvention tool error instructing manual user edits when essential.
- `view_image` path resolution uses absolute and canonical real paths before access checks.
- Workspace boundary checks apply after symlink resolution; symlink escapes are blocked.
- Outside-workspace file reads are approval-gated through the same approver contract as `patch`.
- Approved outside-workspace reads are written to run logs with requested/resolved path metadata.
- Default `view_image` raster attachment materialization optimizes performance and minimizes provider-bound data transfer by validating then attempting to re-encode every supported non-raw raster image with source bytes `>= 100 KiB` into JPEG. If JPEG optimization fails or does not reduce payload size, Kent preserves the original validated image bytes and still enforces the attachment size cap.

## Tool Output And Failures

- Large tool output is truncated for model consumption using standardized head/tail payloads with truncation metadata.
- Model-step transient failures use exponential backoff retries with 5 attempts: `1s`, `2s`, `4s`, `8s`, `16s`.
- Model/API errors in ongoing mode are shown as concise single-line errors; full details remain in detail/logs.
- After a provider HTTP 400, Kent may repair tool calls that lack outputs (typically left dangling by an interruption) by appending a synthetic completion to each, then rebuilding the request and retrying. The repair is append-only: it never rewrites or removes persisted history, so the prompt-cache prefix through each repaired call stays intact, and the materialized output matches the original call kind. The synthetic result is an error stating the call was interrupted with no output, never a fabricated success. The repair defers to the resume path while interrupted calls still have pending re-execution starts, and no-ops when a 400 has no missing outputs (the original error then surfaces). Each repair appends one operator-only `developer_error_feedback` warning noting how many calls were closed.
- Persisted operator-facing turn-start failures that prevent the agent loop from starting use `developer_error_feedback` so they appear in ongoing scrollback.
- Local command/validation failures that do not block a model turn remain plain `error` diagnostics.

## Ask Question

- `ask_question` is shared by model and runtime with unified UI.
- Runtime `ask_question` pauses the active pipeline until answered.
- Questions wait indefinitely; there is no timeout/default cancel.
- Model-callable `ask_question` is limited to ordinary question/suggestion/freeform asks. Approval prompts are internal automated workflows and are not exposed to the model tool schema.
- Suggestions support a freeform override branch. `Tab` toggles between picker and freeform commentary editing.
- Suggestions use schema-level 1-based `recommended_option_index`.
- Recommended suggestion UI shows a `success`-colored marker and faint recommended note; selected recommended row uses selected-row styling.
- Selecting freeform with empty input opens freeform editing; submitting from that path still requires non-empty commentary.
- Returning to picker preserves a pending freeform draft as muted text.
- Internal approval asks show only `Allow once`, `Allow for this session`, and `Deny`.
- Internal approval commentary is injected through regular queued user-message steering; denial fails the guarded tool call authoritatively.
- Freeform ask input uses the same editor/cursor behavior as main input.
- Source origin is not labeled in UI.
- Answers are persisted as explicit summary text including selected option number and commentary.
- Ask queue semantics are strict FIFO, in-memory only, and submitted answers are not editable.
- Optional post-answer action binding uses typed registry with stable ID, payload schema, and handler.

## Sessions And Persistence

- **Full transcript history is expected to weigh dozens of gigabytes. Production code must never load full `events.jsonl` into memory or walk the entire file. Session forking or cloning is the only accepted production full-walk operation because it explicitly copies history to the fork point.**
- Sessions support stop/resume.
- Persistence root is configurable; default is `~/.kent`.
- Durable domain model is `project > workspace > worktree`.
- SQLite is authoritative for structured metadata and server-owned resources.
- Large append-only session artifacts remain file-backed under `projects/<project-id>/sessions/<session-id>`.
- App-global daemon listen config is explicit through `server_host` and `server_port`. Kent binds exactly the configured address and fails startup if occupied.
- Same-machine Unix-socket optimization is local-first and additive. Explicit `server_host` or `server_port` overrides stay authoritative.
- JSON-RPC custom error codes in `shared/protocol` are wire contracts.
- Interactive startup is workspace-first. Unregistered cwd enters an explicit post-auth binding flow with create-new-project first and existing-project picker below.
- Server-browsing mode can open existing server projects/workspaces only; it must not offer binding or project creation for the client path.
- Headless startup in an unregistered workspace fails fast; it must not auto-create hidden project/workspace state.
- To recover from headless fail-fast workspace binding, `kent project [path]` inspects the project bound to a path, `kent attach [path]` binds a workspace to the project already bound to cwd, and `kent attach --project <project-id> [path]` binds with an explicit project override. All forms default `path` to cwd.
- Minimum server-admin setup commands are `kent project list`, `kent project create --path <server-path> --name <project-name>`, and `kent attach --project <project-id> <server-path>`.
- Server-admin project/binding commands prefer RPC to the configured running daemon when available; they must not require shutting down the server or taking local ownership of the persistence root.
- Explicit relocation recovery is `kent rebind <session-id> <new-path>`, which retargets one session to a different workspace root.
- When a session selected from the interactive picker has a stored workspace root different from Kent's current workspace root, startup shows `Workspace changed`. `Yes` retargets that session before opening; `No` returns to the session picker.
- Workspace relocation/rebinding is explicit user action; Kent does not infer auto-rebinds.
- Session metadata authority lives in SQLite.
- Interactive session creation is lazily durable.
- Session start/setup becomes immutable at first model request dispatch, except thinking level can change on resume and system/reviewer prompt snapshots can refresh after successful compaction.
- Lock covers model, core generation params, enabled tool IDs, native web-search mode, and system/reviewer prompt snapshots. Model/core generation fields, active enabled tool IDs, and native web-search mode remain locked for the session lifetime; system/reviewer prompt snapshot fields are locked per compaction generation. Developer meta context messages are transcript entries, not lazy-refreshed lock snapshots. Tool declarations for locked tool IDs are runtime-defined and are not persisted as session snapshots.
- Locked enabled tool IDs distinguish explicit empty tool sets from legacy missing metadata with a presence field; explicit empty sets are authoritative.
- Legacy request-shape locks that lack enabled-tool presence or native web-search mode are backfilled before use; failed backfill blocks request/launch planning instead of using mutable config fallback.
- Transcript message order is immutable for cache stability.
- Canonical model context/history is stored as Responses API input items; message-only chat is UI projection.
- `events.jsonl` is append-only on normal writes; periodic compaction rewrites canonical JSONL to control growth.
- Committed transcript is durable on disk (synchronous persistence on commit). Both active and dormant sessions project user-visible transcript by streaming the persisted event log through a windowed projector that retains only the requested page/recent-tail window; live reads overlay only the in-flight streaming delta. The in-memory transcript storage retains the bounded model working set (compaction checkpoint plus post-cutoff tail), not the full transcript: compaction trims pre-cutoff provider items, local entries, and tool completions; only an `O(1)` committed-entry counter survives for hot-path delta detection.
- Crash-loss tolerance allows losing up to one in-flight tool call. No session event compression.

## Auth

- OpenAI auth supports API key and subscription OAuth.
- Auth is global server-level, not per-session.
- Startup blocks on auth only when the resolved provider path requires Kent-managed OpenAI auth.
- Explicit OpenAI-compatible base URLs and other non-OpenAI provider paths may continue without Kent-managed auth.
- Startup auth failures and 401s surface as normal actionable UX.
- Startup auth picker uses themed startup picker style and friendly titles with one-line explanations.
- Picker exposes browser OAuth, device-code OAuth, `No auth`, and env-key adoption when available.
- Browser OAuth uses a hybrid callback flow accepting local callback or pasted callback URL/code.
- OAuth issuer routing is not configurable in production. `KENT_OAUTH_ISSUER` is intentionally unsupported.
- Interactive startup treats `OPENAI_API_KEY` as chooser-backed auth source, not unconditional override.
- Saved subscription auth plus env key with no preference asks the user which source should win.
- `/login` and `/logout` reopen auth selection without clearing credentials first. Only choosing `No auth` option explicitly clears active auth method and env-vs-saved preference.
- OAuth failure does not auto-fallback to API key.
- OAuth refresh is silent except refresh failures are surfaced.
- Global auth method can be switched only while idle.

## Configuration

- User settings load from `~/.kent/config.toml` by default unless persistence root is overridden.
- Unknown config keys are errors.
- Precedence is CLI overrides > environment > settings file > built-in defaults.
- After first successful auth, missing `config.toml` triggers first-time setup before session selection.
- Headless runs refuse to start if onboarding has not been completed (config.toml does not exist and no interactive GUI startup was ever performed)
- `theme=light` and `theme=dark` select fixed Kent palettes. `theme=auto` or omitted theme uses terminal background detection.
- Global debug mode is configured by `debug = true` or `KENT_DEBUG=1` and enables developer-oriented strictness.
- Thinking level passes configured values to the API unchanged.
- Context window setting is `model_context_window` and varies per model and can be overridden by user.
- Effective reviewer and subagent context windows must be at least `40000` or the server crashes with config validation failure.
- `context_compaction_threshold_tokens < model_context_window` is required or the server crashes with config validation failure.
- OpenAI Responses API `store` is configurable with default `false`.
- `tools.web_search` is enabled by default; `web_search` selects provider-native search (`native`) or disabled (`off`).
- `tools.view_image` is enabled by default and advertised only to multimodal-capable models.

## Compaction

- Compaction starts a new active conversation list from compacting output seed items. Full persisted session events remain in the durable session log.
- Runtime context needed after compaction, including workflow prompts and reminders, is steered into the new active list after replacement.
- Kent may compact before submitting a queued user prompt when current context usage is within the runway reserve.
- Pre-submit compaction uses `context_compaction_threshold_tokens - pre_submit_compaction_lead_tokens`, with default lead `35000`.
- Startup rejects compaction settings that begin normal or pre-submit compaction below 50% of `model_context_window`; this is separate from the `40000` minimum context window.
- `compaction_mode=none` disables manual and automatic compaction and accepts provider API errors on context overflow.
- Manual `/compact` is available while the agent is idle.
- Human-facing UX says `compact`; agent-facing prompt/tool language says `handoff`.
- Successful manual `/compact` steers a hidden developer carryover message containing the last visible user prompt.
- Agent-triggered handoff uses its own internal compaction mode and may steer a detail-only future-agent developer message; it does not reuse manual carryover semantics.
- Main-agent OpenAI `session_id` is the persisted Kent session ID for the conversation lifetime.
- Prompt-cache lineage rotates by compaction generation: base `<session_id>`, then `<session_id>/compact-N`.
- Supervisor/reviewer cache keys use `<session_id>/supervisor` with the same compaction generation counter.
- After successful history replacement, Kent clears stale system/reviewer prompt snapshots from the locked contract. The next model request lazily reloads effective config, preserves locked model/provider/generation fields and active enabled tool IDs, refreshes system/reviewer prompt snapshots, then persists the refreshed lock for the new generation. The no-marker design accepts the existing file-store crash window where `history_replaced` can be durable before prompt snapshot clearing is durable.
- The compaction request itself uses the stored pre-compaction contract when one exists. Refreshed system/reviewer prompt snapshots apply only to ordinary requests sent after successful history replacement. A repeat compaction before lazy refresh starts from the cleared prompt snapshot state and uses a non-mutating prompt resolver rather than preserving or persisting the previous prompt.
- Local compaction instructions are final `developer` messages. Runtime rejects any tool calls returned by the agent during local compaction.
- Local compaction summary generation reuses the normal main-agent request envelope and changes only request items by appending compaction instructions.
- If native or local compaction exceeds provider context length, Kent retries by collapsing supported historical tool payloads in the compaction request only. The four total attempts are the original request, then cumulative collapse targets of 10%, 20%, and 40% of the model context window. Shell outputs, including `exec_command` and `write_stdin` outputs, and patch inputs collapse to exact text `<collapsed>`; tool calls and call/output relationships remain present. Reasoning items and unsupported tool payloads are not removed or collapsed. Successful repaired compaction persists an operator-visible diagnostic with collapse counts and estimated omitted tokens.
- Compaction lifecycle status is emitted through runtime output mutation.
- Completed compaction creates no UI-only transcript row. Transcript-visible compaction summaries come from server-owned transcript items.

## Model Defaults

- Model seed is unset by default.
- Temperature is fixed to `1`.
- Max output tokens are unlimited by default.

## Goals

- Models may use normal shell commands `kent goal show`, `kent goal complete`, and first-time `kent goal set <objective>` for the current session, but other goal commands detect invocation by the agent and refuse it.
-  `goal set` by agents is allowed only when no active or paused goal exists. Completed goals do not block the next agent-set goal.
- Successful goal mutations emit typed runtime status updates carrying the projected goal status state so frontends can update from goal SSOT instead of inferring status from transcript feedback or run lifecycle. Set, pause, resume, complete, and clear emit updates; show/read-only operations do not.
- `/goal <objective>` immediately sets/replaces the session goal and starts a model turn. It must be allowed even while the model turn is running, and the new notice is steered as usual.
- `/goal resume` on a completed goal reopens it as active.
- Goal completion is explicit CLI state mutation, not natural-language inference.
- Goal mode requires `ask_question` in the locked tool surface for active model loops. Validate parity at model-work startup and surface a normal runtime error if violated. This parity is enforced inside workflow runs too, so a goal set inside a workflow requires `ask_question` visibility as well.
- Lock: `ask_question` visibility and `/questions` state are separate contracts. Missing `ask_question` from the locked tool surface blocks goal model loops; `/questions off` only makes `ask_question` calls return the questions-disabled tool result and must not stop, suspend, or block active goal execution.
- Goal control is available inside a workflow run (set/show/pause/resume/complete/clear). While an active workflow run drives the session, the goal is a passive objective: no separate goal continuation loop runs (the workflow turn loop is the single driver), and the active goal's reminder is folded into the workflow's invalid-completion nudge.
- A valid terminal workflow completion soft-cascades an active goal to complete (actor=system) in the same step, across every completion source (structured, unstructured, tool, observed-durable). A valid completion is never blocked by a still-active goal; paused goals are left intact. The cascade is conditional on the same goal still being active when it commits, and for tool-mode completion it is emitted after the terminal tool result is persisted so it never interleaves a non-tool item between a tool call and its result.
- Goal mutations apply directly through the shared engine, including agent-origin set/complete from the model's shell tool (no queueing, no shell token, no lease). The deterministic agent-overwrite denial (an agent may not overwrite an active or paused goal) still applies.
- Goal CLI never mutates session DB directly. It crosses live server/runtime RPC.
- Any `kent service` commands that affect the server state (restart, stop, start it) detect invocation by kent itself and refuse to run, remaining human-only.
- Ctrl+C during active goal work keeps persisted status `active` and creates runtime-local suspension only. The next user message auto-resumes the suspended goal loop after its turn completes (no `/goal resume` needed); an explicit `/goal pause` is still the hard pause. A user turn that is itself interrupted leaves the loop suspended.
- The goal status-line indicator in TUI shows the animated spinner only while a goal run is executing; when the goal is `active` but idle (e.g. after Ctrl+C), it shows the idle status dot.

## Headless Mode

- `kent run "prompt"` is the supported headless/subagent interface.
- `kent run` is a pure client: it attaches to an already-running server (configured remote or discovered local daemon) and never starts a server of its own. When no server is reachable it fails fast with a typed error directing the operator to `kent serve` or `kent service install`.
- Headless roles use `kent run --agent <role> "prompt"`; `--fast` selects the built-in fast role.
- Subagent roles are file-only `[subagents.<role>]` config tables and inherit main config unless overridden.
- Subagent role `model_context_window` and `reviewer.model_context_window` overrides must resolve to at least `40000` or config validation crashes the server.
- Subagent roles may set `agent_callable=false`; such roles are hidden from agent-facing role context and rejected for Kent-session subagent calls, while humans may still run them from ordinary shells.
- The built-in `fast` role exists without config and may switch to a smaller/faster profile on exact provider first-party setups.
- Headless executes a single non-interactive prompt with normal runtime/session persistence.
- It creates/resumes normal sessions and auto-names unnamed sessions `<session-id> subagent`.
- Default timeout is infinite; `--timeout` can bound execution.
- Output modes are explicit: `final-text` default and optional `json`.
- JSON mode emits exactly one final object on stdout.
- Progress is quiet by default and emits to stderr only with `--progress-mode=stderr`.
- A headless or workflow run registers the session's single shared runtime and drives it. Interactive activation for the same active session resolves and attaches to that same shared engine as an equal full-control surface: live transcript/status, user steering and queued messages, prompt/approval answers, and every control operate against the shared runtime with no ownership, lease, or limited-control mode.
- A running workflow task is steerable from any attached client as usual (chat, queued steering, goal control, settings, compaction, worktree, process view). The only workflow-specific limit is that the model cannot submit a structured-output final answer that is invalid for the node; that is a completion constraint, not a client restriction. Failures to reach an active runtime surface as the typed runtime-unavailable error.
- A submission to a busy runtime is steered into the in-flight step via the shared engine's queue boundary and auto-drains when the step finishes, unless terminal workflow completion wins first; in that case the client receives a visible failure instead of a silent drop. Queue requests made while the runtime is closing are rejected so the client can restore the input.
- Clients report the active runtime phase from shared runtime state and use an active/busy fallback while a run is executing, draining, or closing, including periods with no active engine-step snapshot. Registered-idle runtimes accept idle operations.
- Prompt and approval resolution uses server-acknowledged shared prompt state. Clients do not locally finalize a pending prompt before the server accepts the answer and publishes/returns the resolved state.
- Worktree controls are available from any client. Worktree mutation runs in a between-steps idle slot of the shared engine (it waits for a gap rather than rejecting while a run is in progress) and remains serialized by the workspace lock.
- Resuming a session with persisted subagent role metadata reapplies that role best-effort when it exists. Missing roles do not block explicit continuation.

## Provider Wiring

- OpenAI requests always set `originator` and `User-Agent`.
- `session_id` header is sent whenever a session ID exists, for both OAuth and API-key auth.
- LLM provider wiring uses a provider-factory seam so runtime/app constructs clients via provider selection.
