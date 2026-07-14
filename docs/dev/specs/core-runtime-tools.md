# Core Runtime And Tools Spec

## Product Scope

- There is and will not be virtualization / sandboxing in Kent. Kent ships Client-server architecture that enables true sandboxing (remote machine / Docker).
- The product name is `Kent` and should remain easy to rename.
- Public docs use Astro + Starlight from `docs/`, deploy as static Cloudflare Pages.

## Client/Server Boundary

- Server owns all tool calling, data persistence, agentic loop, network (API/provider) communication logic. 
- Server-owned service interfaces, concrete service implementations, runtime handles, headless launchers, logging/timeout policy, lifecycle orchestration, and close/drop semantics must not live outside server-scoped code. CLI/GUI packages must not import `server/*` directly.
- User-visible lifecycle side effects trigger at one client-facing accepted-event boundary, not inside only one transport/runtime path.
- Transcript delivery to clients is exactly-once and ordered per subscription: every transcript-affecting event carries a per-subscription monotonic sequence number; opening a subscription delivers hydration state as the first ordered message(s) on the same channel; every event is content-complete (no change notifications requiring a follow-up read); tool completions are emitted in committed order; committed assistant entries carry the stream/step identity of their deltas. Clients do not receive absolute transcript counts, offsets, or revisions.
- Any migration paths should be removed instead of preserved as compatibility shims. Breaking API/protocol changes are acceptable after confirming with the human.

## Skills And Generated Assets

- Skills are discovered from Kent-owned roots: `<persistence-root>/skills` (default `~/.kent/skills`), workspace `.kent/skills`, and generated embedded skills under `<persistence-root>/.generated/skills`. The global and generated roots follow the selected persistence root; only the workspace root stays workspace-relative. Global `AGENTS.md` and the global system-prompt file resolve under the same `<persistence-root>` (an empty value falls back to `~/.kent`).
- Symlinks must be followed through when discovering, reading, loading skills and AGENTS.md.
- `config.toml` supports file-only `[skills]` boolean toggles for per-skill new-session enable/disable. Disabled skills remain visible in GUI/TUI and only affect model visibility.
- Preinstalled skills are seeded from binary-embedded deterministic assets under `prompts/skills/**` into `<persistence-root>/.generated/skills`.
- `<persistence-root>/.generated` is deterministic, destructible, overwritten on server startup, and not user-owned. Generated sync runs on server startup (`kent serve` or embedded server), not in clients.
- Structured model file-edit tools deny writes to `<persistence-root>/.generated` and its descendants without asking for approval. The generated-files guidance is the rule-owned denial reason: it tells the model that generated files are overwritten every session, generated skills cannot be edited, and generated skills should be shadowed from the active user skills path with the same name/id/directory structure. Normal tool error formatting may add surrounding context. This policy does not change command execution or file reads. Generated-asset deny rules may use literal paths, glob patterns, or regex patterns; regex use in this deny-policy matcher is a product feature and is not covered by the repository ban on regex parsing and replacement hacks.
- Edited/add/delete/rename/symlink/invalid-marker generated trees move to `<persistence-root>/recovered/<UTC timestamp>/.generated`, then regenerate.
- If `<persistence-root>/recovered` is non-empty, every new session gets a user-facing, non-model-visible warning asking the user to clean recovered files and not edit `.generated`.
- Generated skills are always seeded. Existing `[skills]` toggles only disable injection by normalized skill name.
- User skills with the same normalized name shadow generated skills.
- Generated skill validation rejects empty files, invalid frontmatter, duplicate generated names, and symlinks/non-regular entries.

## Core Tools

- Core Model tools are `shell`, `write_stdin`, `view_image`, `patch`, `ask_question`, and `trigger_handoff`.
- Goal management is CLI/runtime-owned. Kent must not add model-callable goal tools, worktree tools, task management tools, workflow tools (outside workflow runs). New tool addition happens strictly after human approval and spec edits.

## Runtime Output Boundary

- Runtime owns one active conversation list/stateflow per session runtime.
- Runtime producers materialize conversation-facing output through `steer`. Steering is sumbitting events (messages, notices, user inputs) into the model's context/conversation via a FIFO queue that waits until the next available **step** boundary and drains the entire queue full at the first step boundary. User message submission, developer notices, mode switch notices, error notices, warning messages.  Steering of multiple items at once must be supported.
- Tool calls are allowed to maintain special machinery due to tool start/end events discontinuity in addition to steering but must reuse the same code paths as much as possible, for example for emission of RPC events and TUI transcript.
- Runtime producers store delayed conversation-facing output through `queue`; Queueing is submission of events (same as steering), but after the **model turn** boundary. See terminology.md for differences between turns and steps.
- Steering items cover all transcript-visible, non-queued messages with exactly 1 exception: markdown line-by-line streaming of agent commentary and final_answer responses. Any other exception must be user-approved and locked in this spec.
- Ordering, transcript visibility, ongoing/detail presentation, model visibility, dedupe, derived events, and post-persist state updates are steering policy, not separate append paths.
- pending user text that they steered coalesces at flush into one user message separated by blank lines; the coalesced message is a normal steering intent.
- Runtime events that do not create model-visible items still route through the output boundary and are stored in the same transcript.
- History replacement is an output mutation owned by the same boundary. Normal additions after replacement use `steer`. Messages about handoffs emitted to TUI scrollback buffer still use steer due to the rule above. Only logical/algorithmic history replacement procedure does not use steering, all user-facing history replacement effects are still regular `steer` api usages.

## Command Execution

- `shell` is the only model-facing shell-command execution surface.
- Commands run in the user login shell, non-TTY mode, with direct shell invocation.
- Execution inherits parent environment and adds non-interactive hints and other technical environment variables.
- stdout/stderr merge into one stream without origin tags.
- Command lifetime is unlimited. `yield_time_ms` controls when Kent returns control and backgrounds the process.
- Non-zero exit is recoverable and does not auto-abort the turn.
- Shell process-launch failures are not automatically retried.
- Interrupt escalation is `SIGINT` then `SIGKILL` after 10 seconds.
- Command post-processing is Kent-owned, applied after execution, configured under `[shell]`, and bypassed by the per-process `raw=true` parameter on the `shell` tool.
- `[shell].postprocessing_mode` accepts exactly `none | builtin | user | all`; omitted configuration resolves to the built-in default before policy compilation, while empty or unknown configured values are errors.
- `[shell].postprocess_hook` is optional. Absence is represented as `null` in API contracts and by typed optionals in clients and server code; a present empty or whitespace-only value is invalid and must be removed to express absence.
- Runtime-effective post-processing mode and hook settings compile into one immutable policy when a shell process starts. That captured policy remains authoritative for foreground completion, transition-to-background output, later `write_stdin` polling, automatic completion notices, and terminal processing during process exit or server shutdown, regardless of later runtime wiring or role/workspace changes.
- `raw=true` bypasses the process's captured post-processing policy in every foreground, background, polling, completion, and shutdown output path.
- The generic command output sanitizer runs before built-ins and hooks for every non-raw mode except `none`. Generic sanitizer is just another command post-processor, not special infrastructure.
- Built-ins run before the optional user hook. A built-in halt stops later built-ins only.
- User hooks receive JSON stdin and return JSON stdout, receiving both original sanitized output and Kent's current processed output.
- Hook failures do not change the provider-facing command-output envelope.
- Background shell processes share one server-global manager, process registry, process-ID sequence, event router, and shutdown lifecycle even when their captured post-processing policies differ. Process IDs are server-global within one server instance; owner session metadata is advisory for routing notices (to both humans and models) and history, NOT access control.
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
- Default `view_image` raster attachment materialization optimizes performance and minimizes provider-bound data transfer by validating then attempting to re-encode every supported non-raw raster image with source bytes `>= 100 KiB` into JPEG or WEBP. If JPEG/WEBP optimization fails or does not reduce payload size, Kent preserves the original validated image bytes and still enforces the attachment size cap.

## Tool Output And Failures

- Large tool output is truncated for model consumption using standardized head/tail payloads with truncation metadata. Tool truncation threshold is not a tool post-processor, but a separate path that runs after the post-processor pipeline. 
- Large output truncation threshold is configurable via a config file.
- Foreground shell completion uses the final visible result after sanitization, postprocessing, warnings, truncation, and presentation trimming to determine whether output exists. Whitespace-only final content counts as no output.
- A foreground shell command that exits successfully with output returns that output as plaintext without an exit-code header.
- A foreground shell command that exits with no output returns exactly `Exit code N, no output.`, where `N` is its exit code.
- A foreground shell command that exits unsuccessfully with output returns `Exit code N, output:` followed by the output.
- A completed background shell always exposes its exit code in both polling results and automatic completion notices. When it has no output, its completion text is `Exit code N, no output.`
- Background completion keeps shell identity and lifecycle state separate from its output summary. Polling retains its structured lifecycle fields.
- Configured background-output verbosity continues to control inline previews. If concise presentation hides a preview for a command that produced output, completion still exposes the exit code and output-file location and does not claim that there was no output.
- Recoverable shell-output warnings remain visible and count as output for completion wording. Background output-file metadata describes retained command output only; a warning does not add lines to or imply content in the command's log file.
- An invalid internal background-completion event panics with diagnostic context in debug invariant mode. Production keeps the background-notice envelope and terminal process facts, records the invariant diagnostic, and presents an explicit internal error instead of fabricated successful or no-output content.
- Model-step transient failures use exponential backoff retries with 5 attempts: `1s`, `2s`, `4s`, `8s`, `16s`.
- Model/API errors in ongoing mode are shown as concise single-line errors; full details remain in detail/logs.
- Kent implements tool repair infrastructure after a provider HTTP 400: Kent may repair tool calls that lack outputs (typically left dangling by an interruption) by appending a synthetic completion to each, then rebuilding the request and retrying. The repair is append-only: it never rewrites or removes persisted history, so the prompt-cache prefix through each repaired call stays intact, and the materialized output matches the original call kind. The synthetic result is an error stating the call was interrupted with no output, never a fabricated success. The repair defers to the resume path while interrupted calls still have pending re-execution starts, and no-ops when a 400 has no missing outputs (the original error then surfaces). Each repair appends one operator-only `developer_error_feedback` warning noting how many calls were closed.
- Persisted operator-facing turn-start failures that prevent the agent loop from starting use `developer_error_feedback` so they appear in ongoing scrollback.
- Local command/validation failures that do not block a model turn remain plain `error` diagnostics.

## Ask Question

- `ask_question` is shared by model and runtime with unified UI.
- Runtime `ask_question` pauses the active pipeline until answered.
- Questions wait indefinitely; there is no timeout/default cancel.
- Model-callable `ask_question` is limited to ordinary question/suggestion/freeform asks. Approval prompts are internal automated workflows and are not exposed to the model tool schema.
- Suggestions support a freeform override branch. In the TUI, `Tab` toggles between picker and freeform commentary editing.
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

- **Full transcript history is expected to weigh dozens of gigabytes. Production code must never load full `events.jsonl` into memory or walk the entire file under any circumstance. Session forking or cloning is the only accepted production full-walk operation because it explicitly copies history to the fork point.**
- Sessions support stop/resume.
- Persistence root is configurable; default is `~/.kent`.
- Durable domain model is `project > workspace > worktree`.
- SQLite is authoritative for structured metadata and server-owned resources.
- Large append-only session artifacts are file-backed under `projects/<project-id>/sessions/<session-id>`.
- App-global daemon listen config is explicit through `server_host` and `server_port`. Kent binds exactly the configured address and fails startup if occupied.
- Same-machine Unix-socket optimization is local-first and additive. Explicit `server_host` or `server_port` overrides stay authoritative.
- JSON-RPC custom error codes in `shared/protocol` are wire contracts.
- Interactive startup is workspace-first. Unregistered cwd enters an explicit post-auth binding flow with create-new-project first and existing-project picker below.
- Server-browsing mode can open existing server projects/workspaces only; it must not offer binding or project creation for the client path.
- Headless startup in an unregistered workspace fails fast; it must not auto-create hidden project/workspace state.
- To recover from headless fail-fast workspace binding, `kent project [path]` inspects the project bound to a path, `kent attach [path]` binds a workspace to the project already bound to cwd, and `kent attach --project <project-id> [path]` binds with an explicit project override. All forms default `path` to cwd.
- Server-admin project/binding commands use RPC to the configured running daemon. They must not require shutting down the server or taking local ownership of the persistence root.
- Explicit relocation recovery is possible via CLI, which retargets one session to a different workspace root.
- When a session selected from the interactive picker has a stored workspace root different from Kent's current workspace root, startup UI presents a prompt to rebind the workspace to the currently open destination.
- Workspace relocation/rebinding is explicit user action; Kent does not infer auto-rebinds.
- Interactive session creation **is lazy**. Sessions are created, persisted, initialized, started, and their data is loaded at the first usage point - user message or other trigger that leads to the agentic loop or model requests.
- Just before the first model request at session start OR after each compaction (exactly matching cache key rotation/lifecycle), model tool definitions, tool list, system prompt, developer context entries, skills list, workspace info, current date/time sent to the model, and conversation context are all snapshotted and become immutable to prevent cache key rotation.
- Developer-role meta context messages are transcript entries, not lazy-refreshed lock snapshots. Tool declarations for locked tool IDs, default (embedded) system prompt are code-defined (hardcoded in the binary) and are not persisted as session snapshots.
- Transcript message order is immutable for cache stability. Under no circumstance without exclusion may the order of items in the persisted transcript change.
- Committed transcript is durable on disk (asynchronous persistence on commit). Both active and dormant sessions project user-visible transcript by streaming the persisted event log through a windowed projector that retains only the requested page/recent-tail window; live reads overlay only the in-flight streaming delta. The in-memory transcript storage retains the bounded model working set (compaction checkpoint plus post-cutoff tail), not the full transcript: compaction trims pre-cutoff provider items, local entries, and tool completions; only an `O(1)` committed-entry counter survives for hot-path delta detection.
- Crash-loss tolerance allows losing up to one model step.

## Auth

- The infrastructure must support using and storing multiple auth providers/auth credentials at the same time.
- Auth is global server-level, not per-session.
- Startup blocks on auth only when the resolved provider path requires Kent-managed provider auth, such as a combination of `OpenAI subscription` provider and a missing OAuth token. Custom providers do not validate auth in Kent, only parse 401 auth errors and advertise them.
- Startup auth failures and 401s surface as normal actionable UX.
- Picker exposes browser OAuth, device-code OAuth, `No auth`, and env-key adoption when available.
- Choosing `No auth` persists the marker that the user chose no auth option, clears active server auth, and disables env-key fallback. Kent then does not send any auth credentials until the user re-executes the auth path and picks another option explicitly.
- Remote client rebinding preserves and re-acknowledges the no-auth connection policy before the rebound client uses startup-owned server routes.
- Headless clients, fresh external clients, and remote connections that have not explicitly acknowledged no-auth remain blocked when Kent-managed OpenAI auth is required and no real auth is configured.
- Browser OAuth uses a hybrid callback flow accepting local callback or pasted callback URL/code.
- Interactive startup treats `OPENAI_API_KEY` as chooser-backed auth source when user has confirmed it, not unconditional override.
- `/login` and `/logout` reopen auth selection without clearing credentials first. Only choosing `No auth` option explicitly clears active auth method and env-vs-saved preference.
- OAuth failure does not auto-fallback to API key.
- OAuth refresh is silent except refresh failures are surfaced.
- Global auth method can be switched only when no active agent runs are present.

## Configuration

- User settings load from `~/.kent/config.toml` by default unless persistence root is overridden.
- Unknown config keys are errors.
- Precedence is CLI overrides > environment > settings file > built-in defaults.
- After first successful auth, missing `config.toml` triggers first-time setup before session selection.
- Headless runs refuse to start if onboarding has not been completed (config.toml does not exist and no interactive GUI startup was ever performed)
- `theme=light` and `theme=dark` select fixed Kent palettes. `theme=auto` or omitted theme uses terminal background detection or system theme detection.
- Global debug mode is configured by config file's `debug = true` or `KENT_DEBUG=1` and enables developer-oriented strictness and fail-fast checks. when debug is false, the runtime never crashes due to developer errors and instead executes best-effort recovery path where possible or exits with clear message. Debug builds fail-fast with a panic and collect+print diagnostic info needed for manual QA and fast developer iteration. Exact cases and recovery mechanisms for this dichotomy must be confirmed on a case-by-case basis with a human.
- Thinking level passes configured values to the API unchanged. Predefined thinking levels exist, inventoried statically per known model, for example `low`, `medium`, `high`, `xhigh`, `max`. Thinking level defaults only are used in GUI when model or provider is recognized as supporting them, and fall back to user-provided value otherwise.
- Context window setting is `model_context_window` and varies per model and can be overridden by user.
- Effective reviewer and subagent context windows must be at least `40000` or the server crashes with config validation failure.
- `context_compaction_threshold_tokens < model_context_window` is required or the server crashes with config validation failure.
- OpenAI Responses API `store` is configurable with default `false`.
- Root-level `provider_identifier` defaults to `kent` and must be a non-empty HTTP product token. OpenAI-family model-provider requests use it for the `originator` header and for the `<provider_identifier>/<Kent version>` `User-Agent`; the same resolved value applies to main, reviewer, workflow, and subagent clients. It is process configuration rather than persisted session-contract data, so resumed sessions adopt the active value after restart. OAuth bootstrap, subscription-status, and update-check traffic are excluded.
- `tools.web_search` is enabled by default; `web_search` selects provider-native search (`native`) or disabled (`off`).
- `tools.view_image` is enabled by default and advertised only to multimodal-capable models.

## Model Requests And Cache Identity

- Every provider-neutral generation request carries one required typed tool-choice mode: automatic or required. Missing and unknown values are invalid.
- Required tool choice validates against the complete effective advertised-tool set, including local/custom declarations and enabled provider-hosted tools. A truly empty set is an invalid request; a selected adapter that cannot represent required choice returns a typed provider-policy error before dispatch.
- Provider and transport failures for a valid required request surface without retrying or falling back to automatic choice.
- Tool-choice mode changes only selection policy. It does not remove, add, or reorder effective tool declarations or change parallel-tool behavior.
- Tool-choice mode is excluded from cached-prefix chunks and digest identity and does not rotate the prompt cache key.
- Exact input-token counting of an already-built generation request preserves its tool-choice mode and complete effective advertised-tool set. Standalone item estimation uses automatic choice.

## Compaction

- Compaction starts a new active conversation list from compacting output seed items. Full persisted session events remain in the durable session log.
- One unified meta-context builder composes generation-scoped environment, AGENTS.md, skills, subagents, active headless/workflow modes, and worktree context. The typed history-replacement steer commits compacting output followed by that canonical context in one durable `history_replaced` event, so the new active generation is born atomically and runtime resume cannot re-arm its context.
- Kent may compact before submitting a queued user prompt when current context usage is high enough that the next user task likely causes compaction. this threshold uses defaults and is configurable. When pre-submit compaction happens, the runtime detects it, session compaction begins first, and then user message is steered using regular mechanisms to arrive as compaction finishes.
- Pre-submit compaction uses `context_compaction_threshold_tokens - pre_submit_compaction_lead_tokens`, with default lead value.
- Startup rejects compaction settings that begin normal or pre-submit compaction below 50% of `model_context_window`; this is separate from the `40000` minimum context window.
- `compaction_mode=none` disables manual and automatic compaction and accepts provider API errors on context overflow.
- Manual `/compact` is available while the agent is both idle and running, submitting /compact during an agent run steers it to occur before the next model step.
- Human-facing UX always says `compact`; agent-facing prompt/tool language says `handoff`.
- Successful manual `/compact` steers a hidden developer carryover message containing the last visible user prompt.
- Agent-triggered handoff uses its own internal compaction mode and may steer a detail-only future-agent developer message.
- Main-agent OpenAI `session_id` is the persisted Kent session ID for the conversation lifetime, including all compactions.
- Prompt-cache lineage rotates by compaction generation: base `<session_id>`, then `<session_id>/compact-N`.
- Supervisor/reviewer cache keys use `<session_id>/supervisor` with the same compaction generation counter.
- After successful history replacement, Kent clears stale system/reviewer prompt snapshots from the locked contract. The next model request lazily reloads effective config, reloads and snapshots model/provider/generation fields and active enabled tool IDs, refreshes system/reviewer prompt snapshots, then persists the refreshed lock for the new generation. The no-marker design accepts the existing file-store crash window where `history_replaced` can be durable before prompt snapshot clearing is durable.
- The compaction request itself always uses the stored pre-compaction contract when one exists. Refreshed system/reviewer prompt snapshots apply only to requests sent **after** successful history replacement to prevent cache invalidation before cache key is rotated due to compaction. A repeat compaction before lazy refresh starts from the cleared prompt snapshot state and uses a non-mutating prompt resolver rather than preserving or persisting the previous prompt.
- Local compaction instructions are final `developer` messages. Runtime rejects any tool calls returned by the agent during local compaction, submits a developer-only error message instructing the model not to call tools, and retries the compaction requests up to 3 times before failing the compaction attempt and stopping the model loop. Each failed attempt emits a user-visible, model-visible, persisted developer error transcript message stating that the model attempted tool calls during compaction. Model-visible text is an instruction not to call tools and retry; user-visible message is a regular notice wording.
- Local compaction summary generation reuses the normal main-agent request envelope and logic and only appends the developer message, then capturing model output, to reuse existing model turn paths and cache continuity.
- Local compaction-summary generation uses automatic tool choice. Post-compaction workflow generation derives the active workflow run's normal generation policy.
- If native or local compaction exceeds provider context length and triggers an API failure (both must be true), Kent retries by collapsing supported historical tool payloads in the compaction request only. The four total attempts are the original request, then cumulative collapse targets of 10%, 20%, and 40% of the model context window. Shell outputs, including `exec_command` and `write_stdin` outputs, and patch inputs collapse to exact text `<collapsed>`; tool calls and call/output relationships remain present. Reasoning items and unsupported tool payloads are not removed or collapsed. Successful repaired compaction persists an operator-visible diagnostic with collapse counts and estimated omitted tokens.
- Compaction lifecycle status is emitted through runtime output mutation.
- Completed compaction creates no UI-only transcript row. Transcript-visible compaction summaries come from server-owned transcript items.

## Goals

- Models may use normal shell commands `kent goal show`, `kent goal complete`, and first-time `kent goal set <objective>` for the current session, but other goal commands detect invocation by the agent and refuse it.
-  `goal set` by agents is allowed only when no active or paused goal exists. Completed goals do not block the next agent-set goal.
- Successful goal mutations emit typed runtime status updates carrying the projected goal status state so frontends can update from goal SSOT instead of inferring status from transcript feedback or run lifecycle. Set, pause, resume, complete, and clear emit updates; show/read-only operations do not.
- `/goal <objective>` (TUI slash command or GUI path) immediately sets/replaces the session goal and starts a model turn. It must be allowed even while the model turn is running, and the new notice is steered as usual.
- `/goal resume` on a completed goal reopens it as active.
- `/goal resume` on an already-active goal is no-op and does not emit any model-facing messages.
- Goal completion is explicit CLI state mutation, not natural-language inference.
- Goal mode requires `ask_question` in the locked tool surface for active model loops. Validate parity at model-work startup and surface a normal runtime error if violated. This parity is enforced inside workflow runs too, so a goal set inside a workflow requires `ask_question` visibility as well.
- Lock: `ask_question` visibility and `/questions` state are separate contracts. Missing `ask_question` from the locked tool surface blocks goal model loops; `/questions off` only makes `ask_question` calls return the questions-disabled tool result and must not stop, suspend, or block active goal execution.
- Inside a workflow run, user `/goal` control is rejected; agent self-goals (set and complete, per the agent goal rules above) remain available. While an active workflow run drives the session, the goal is a passive objective: no separate goal continuation loop runs (the workflow turn loop is the single driver), and the active goal's reminder is folded into the workflow's invalid-completion nudge.
- A valid terminal workflow completion soft-cascades an active goal to complete (actor=system) in the same step, across every completion source (structured, unstructured, tool, observed-durable). A valid completion is never blocked by a still-active goal; paused goals are left intact. The cascade is conditional on the same goal still being active when it commits, and for tool-mode completion it is emitted after the terminal tool result is persisted so it never interleaves a non-tool item between a tool call and its result.
- Goal mutations apply directly using the regular steering path. Goal in-memory state applies immediately and atomically, UI updates deterministically, while persistence, model-awareness (developer message injection), and other ops that require idle runtime apply via steering at the next model step. The deterministic agent-overwrite denial (an agent may not overwrite an active or paused goal) still applies immediately.
- Goal CLI never mutates session DB directly. It crosses live server/runtime RPC.
- Any `kent service` commands that affect the server state (restart, stop, start it) detect invocation by kent itself and refuse to run, being human-only.
- Ctrl+C during active goal work keeps persisted status `active` and creates runtime-local suspension only. The next user message auto-resumes the suspended goal loop after its turn completes (no `/goal resume` needed); an explicit `/goal pause` is still the hard pause. A user turn that is itself interrupted leaves the loop suspended.
- The goal status-line indicator in TUI shows the animated spinner only while a goal run is executing; when the goal is `active` but idle (e.g. after Ctrl+C), it shows the idle status dot.

## Headless Mode

- `kent run "prompt"` is the supported headless/subagent interface.
- `kent run` is a pure client: it attaches to an already-running server (configured remote or discovered local daemon) and never starts a server of its own. When no server is reachable it fails fast with a typed error directing the operator to `kent serve` or `kent service install`.
- Headless roles use `kent run --agent <role> "prompt"`; `--fast` selects the built-in fast role. `--fast` and `--agent` cannot be combined.
- Subagent roles are file-only `[subagents.<role>]` config tables and inherit main config unless overridden.
- Subagent role `model_context_window` and `reviewer.model_context_window` overrides must resolve to at least `40000` or config validation crashes the server.
- Subagent roles may set `agent_callable=false`; such roles are hidden from agent-facing role context and rejected for Kent-session subagent calls, while humans may still run them from ordinary shells as headless, or use them in workflows.
- `[workflow] subagents = false` is a TOML-only, default-disabled control for custom-role delegation by workflow agents. When enabled, a custom role may additionally set `workflow_subagent=false` to reject workflow-agent launches and omit that role from workflow-agent role context; this does not affect ordinary human-shell launches or direct workflow-node assignment.
- Workflow-agent custom-role delegation requires `agent_callable=true`, `[workflow] subagents = true`, and an effective `workflow_subagent=true`; the global setting remains authoritative over per-role metadata.
- The built-in `fast` role remains absent from the custom-role catalog and bypasses the workflow-specific controls while continuing to obey `agent_callable`.
- The built-in `fast` role exists without config and may switch to a smaller/faster profile on exact provider first-party setups, or be configured by the user.
- Headless executes a single non-interactive prompt with normal runtime/session persistence.
- It creates/resumes normal sessions and auto-names unnamed sessions `<session-id> subagent`.
- Default timeout is infinite; `--timeout` can bind execution.
- Progress defaults to live mode (`--progress-mode=stderr`): every finalized user-visible assistant commentary or final message is emitted to stdout when committed, while lifecycle notices remain on stderr. New sessions announce the real `{{.LaunchCommand}} run steer <session-id> "prompt"` command only after the session run becomes actively steerable; resumed sessions do not announce it. Compaction-start and recoverable-failure notices remain visible, while routine tool/reviewer/completion status spam is omitted. `-q` and `--quiet` are aliases for `--progress-mode=quiet`, which preserves final-result-only output and suppresses lifecycle notices.
- `kent run steer <session-id> <message...>` submits steering into an active shared runtime for another session. It requires an explicit positional session id, rejects a target equal to the caller's `KENT_SESSION_ID`, records prompt history through the runtime-control queue path, and prints `ok` when accepted. The target headless run prints `Steered message: <full text>` from the same accepted/queued event and emits no later delivery notice. It fails when the target has no active run and includes the equivalent `kent run --continue <session-id> <message>` hint; it never starts or queues an idle follow-up turn.
- `kent run stop <session-id>` interrupts any active shared runtime by session id, regardless of which client started it. It requires an explicit positional session id, rejects a target equal to the caller's `KENT_SESSION_ID`, prints `Stopped` when an interrupt is accepted, and prints `No active run` as a successful no-op when the session is already idle or nonexistent. Pending steering tagged into the stopped live run is not executed; if the runtime closes before it can be resumed, Kent emits a visible stopped/failed queued-message status before dropping the in-memory queue item.
- `kent run wait <session-id>` waits for the active shared runtime's final result. It requires an explicit positional session id, rejects a target equal to the caller's `KENT_SESSION_ID`, fails fast without printing final-answer content when no active run is present, and mirrors ordinary `kent run` abnormal completion handling. Final-text output includes the ordinary continue hint; `--output-mode=json` uses the same result object shape as ordinary `kent run --output-mode=json`. Wait has no progress output.
- Live run-control commands are dedicated runtime-control operations, not `RunPrompt` continuations. Steering performs the active-run check and queue mutation in one server operation, and stop returns explicit status so clients distinguish accepted interruption from idle no-op.
- Live run-control commands accept only flags that are meaningful for the specific operation: `--persistence-root` for targeting the configured root, and `--output-mode=json` for `wait`. They do not accept workspace, model, provider, agent, timeout, tools, or progress flags.
- Headless stdin is not a steering channel. Parent agents steer live runs with explicit run-control commands instead of relying on stdin line or chunk boundaries.
- A headless or workflow run registers the session's single shared runtime and drives it. Interactive activation for the same active session resolves and attaches to that same shared engine as an equal full-control surface: live transcript/status, user steering and queued messages, prompt/approval answers, and every control operate against the shared runtime with no ownership, lease, or limited-control mode.
- A running workflow task is steerable from any attached client as usual (chat, queued steering, goal control, settings, compaction, worktree, process view). The only workflow-specific limit is that the model cannot submit a structured-output final answer that is invalid for the node; that is a completion constraint, not a client restriction. Failures to reach an active runtime surface as the typed runtime-unavailable error.
- A submission to a busy runtime is steered into the in-flight step via the shared engine's queue boundary and auto-drains when the step finishes, unless terminal workflow completion wins first; in that case the client receives a visible failure instead of a silent drop. Queue requests made while the runtime is closing are rejected so the client can restore the input.
- Clients report the active runtime phase from shared runtime state and use an active/busy fallback while a run is executing, draining, or closing, including periods with no active engine-step snapshot. Registered-idle runtimes accept idle operations.
- Prompt and approval resolution uses server-acknowledged shared prompt state. Clients do not locally finalize a pending prompt before the server accepts the answer and publishes/returns the resolved state.
- Worktree controls are available from any client, and transition scheduling does not vary by caller surface. List and status are reads. Creation, including blocking setup, executes in the requesting command and does not require a model-step boundary. Deleting a worktree that does not require switching the calling session also executes in the requesting command.
- CLI `kent worktree list` resolves the workspace bound to cwd without requiring a session. `KENT_SESSION_ID` or explicit `--session` adds that session's current-worktree projection; absent session context produces a markerless workspace list, and Kent never infers a session from workspace history.
- Enter, leave, and deletion that switches the calling session are process-local scheduled transition operations. The command returns an accepted/scheduled result without waiting for an active model step to finish; the transition executes in the next between-steps idle slot before queued user work and is then serialized by the workspace lock. When no model step is active, the same operation may execute immediately but retains scheduled/reminder semantics.
- Each session has at most one pending worktree transition. An identical in-process retry returns the original acknowledgement; a different request is rejected with typed pending-transition state.
- Scheduled worktree transition failures do not retroactively change the command exit status. Attached clients observe typed completion or failure through the existing per-session activity stream, and Kent steers a typed failure notice into the affected session when runtime steering is available. Successful target changes remain authoritative only when the ordinary worktree reminder arrives.
- Pending worktree transitions are not persisted or resumed. Server shutdown or restart cancels them; reconnecting clients refresh authoritative worktree status instead of hydrating a separate operation record or stream.
- Resuming a session with persisted subagent role metadata reapplies that role best-effort when it exists. Missing roles do not block explicit continuation.
- JSON command mode for all TUI commands emits exactly one final object on stdout and implicitly uses quiet progress behavior.

- LLM provider wiring uses a provider-factory seam so runtime/app constructs clients via provider selection.
- OpenAI-compatible Responses SSE streams accept either OpenAI's `data: [DONE]` sentinel or normal EOF after a valid `response.completed` event has been consumed. `response.failed`, `response.incomplete`, `error`, and caller cancellation are terminal failures. Streams that end before any terminal Responses event, including malformed SSE/JSON framing that prevents a terminal event from being consumed, surface provider-contract errors instead of partial model completion.
