---
title: Configuration
description: Settings locations, precedence, CLI and environment overrides, and the full Kent config reference.
---

## Precedence

Kent resolves settings in this order (ascending priority):

1. Built-in defaults
2. `~/.kent/config.toml`
3. `<workspace-root>/.kent/config.toml`
4. Environment variables
5. `kent run` CLI flags

Interactive session flows resolve workspace-local config from the session workspace root. 

:::tip
`kent serve` starts without a workspace root and applies workspace config only when a project/session selects a workspace, so it doesn't matter where you run `kent serve`
:::

## Locations

### Persistence root

- Workspace settings live at: `<workspace-root>/.kent/config.toml`. Note that workspace root is not necessarily the same as where you might have started the TUI - it's where the agent will actually do the work.

- Global settings live at: `~/.kent/config.toml`, and this location (along with all other data storage) is overridable via `--persistence-root`. The flag also relocates the root's model-visible global context — global `AGENTS.md`, the global system-prompt file, global skills, and generated assets.
When you select a non-default root via the flag, Kent normalizes it to an absolute path and publishes it as the `KENT_PERSISTENCE_ROOT` environment variable, so subagents launched via `kent run` and shell tools inherit the same isolated root.
`kent service` is also root-aware. Each `--persistence-root` install bakes the root into the registration. The OS still holds a single service, so install with the root you want managed.

Selecting a non-default root also makes client attach root-aware: a client only attaches to a server that actually serves that root, and the check is re-applied if the connection drops and reconnects, so a run can never silently switch to a different instance that later takes over the same host and port. This prevents an isolated-root invocation (for example `kent run --persistence-root /tmp/root`) from attaching to your default `~/.kent` server when both resolve to the same host and port. If no server is serving the selected root, start one with `kent serve --persistence-root <root>` (or install the service with that root).

## Example

```toml
model = "gpt-5.5"
thinking_level = "medium" # low, medium, high, xhigh
model_verbosity = "medium" # or "low"
# system_prompt_file = "SYSTEM.md" # relative to this config.toml directory
theme = "auto" # or light / dark
web_search = "native"
compaction_mode = "local" # or "native" (if supported)
cache_warning_mode = "default" # cache invalidation warning frequency; or "verbose" / "off"
server_host = "127.0.0.1"
server_port = 53082

[timeouts]
model_request_seconds = 400

[tools]
shell = true
# Leave both patch/edit commented to use Kent's model-based default.
# patch = true
# edit = false
view_image = true
web_search = true
trigger_handoff = true # proactive compaction by the model

[shell]
postprocessing_mode = "all" # shell output token optimizations by Kent, or "all" | "none" | "user"
# postprocess_hook = "~/.kent/shell_postprocess_hook" # custom processor, see docs

[workflow]
completion_mode = "auto"
concurrency = 5
max_invalid_completion_attempts = 5
subagents = false # TOML-only; workflow agents cannot launch custom roles unless enabled

[skills]
"skill name" = true

[reviewer] # aka supervisor
frequency = "edits"
# model = "gpt-5.4-mini"
# model_verbosity = "low"
# provider_override = "openai"
# openai_base_url = "http://127.0.0.1:11434/v1"
# auth = "none" # or "inherit"
# model_context_window = 64000
timeout_seconds = 120
verbose_output = false # show supervisor suggestions in ongoing transcript
# system_prompt_file = "~/.kent/reviewer_system_prompt.md"

# custom subagent roles config, fast is the default one, always provided
[subagents.fast]
# agent_callable = true
# description = ""
# model = "gpt-5.4-mini"
# priority_request_mode = true
```

### Workflow subagent delegation

`[workflow] subagents` defaults to `false` and has no environment override. Set it to `true` to let workflow agents launch eligible custom roles.

`workflow_subagent` is optional role metadata and defaults to `true`. A custom role is workflow-callable only when `agent_callable`, `[workflow] subagents`, and its effective `workflow_subagent` value all permit it. The global workflow setting remains authoritative.

## CLI Overrides

| Flag | Overrides | Notes |
| --- | --- | --- |
| `kent run --model` | `model` | |
| `kent run --provider-override` | `provider_override` | |
| `kent run --thinking-level` | `thinking_level` | |
| `kent run --theme` | `theme` | |
| `kent run --model-timeout-seconds` | `timeouts.model_request_seconds` | |
| `kent run --tools` | entire tool set | CSV replacement, not a merge |
| `kent run --openai-base-url` | `openai_base_url` | Also affects continuation behavior |


`kent run` also accepts the headless-only selectors `--agent <role>` and `--fast`, which choose a subagent role rather than directly overriding one config key.

## Reference

### Core Settings

| Key | Type | Default | Env | CLI | Description |
| --- | --- | --- | --- | --- | --- |
| `model` | string | `gpt-5.5` | `KENT_MODEL` | `kent run --model` | Model name. If provider inference from the model name is not enough, set `provider_override` too. |
| `thinking_level` | string | `medium` | `KENT_THINKING_LEVEL` | `kent run --thinking-level` | Provider-specific reasoning effort string. |
| `model_verbosity` | string | `medium` |  |  | Text verbosity hint for supported models. Allowed: `""`, `low`, `medium`, `high`. Unsupported models ignore it. |
| `system_prompt_file` | string | `""` |  |  | Main system prompt file. Relative paths resolve from the containing `config.toml` directory. Empty files are skipped. |
| `theme` | string | `auto` | `KENT_THEME` | `kent run --theme` | TUI theme. Allowed: `auto`, `light`, `dark`. `light` and `dark` force Kent's fixed palettes. `auto` or an omitted value falls back to terminal background detection. |
| `notification_method` | string | `auto` | `KENT_NOTIFICATION_METHOD` |  | Terminal notification backend. Allowed: `auto`, `osc9`, `bel`. `auto` chooses `osc9` on supported terminals and falls back to `bel`. |
| `tool_preambles` | bool | `true` | `KENT_TOOL_PREAMBLES` |  | Includes tool-usage preambles in the main system prompt for interactive runs. Headless `kent run` still suppresses them. |
| `priority_request_mode` | bool | `false` |  |  | Enables fast-mode requests where the provider supports them. |
| `debug` | bool | `false` | `KENT_DEBUG` |  | Enables global developer-oriented strictness and logging. Only use for development/debugging |
| `server_host` | string | `127.0.0.1` | `KENT_SERVER_HOST` |  | Exact TCP app-server host Kent will dial or listen on. Kent does not use discovery files or silent port rebinding. Same-machine Unix socket optimization, when supported, is derived automatically and does not override an explicit TCP target. |
| `server_port` | int | `53082` | `KENT_SERVER_PORT` |  | Exact TCP app-server port Kent will dial or listen on. Must match across clients attached to the same persistence root. Same-machine Unix socket optimization, when supported, is additive only and does not override an explicit TCP target. |
| `web_search` | string | `native` | `KENT_WEB_SEARCH` |  | Web search backend. Allowed: `off`, `native`. `custom` (e.g. Brave Search) is not implemented yet, on the roadmap. |
| `provider_override` | string | `""` | `KENT_PROVIDER_OVERRIDE` | `kent run --provider-override` | Forces provider family for custom or alias model names. Allowed: `openai`, `anthropic`. Requires an explicit `model` override. |
| `openai_base_url` | string | `""` | `KENT_OPENAI_BASE_URL` | `kent run --openai-base-url` | OpenAI-compatible base URL. Must be used with `provider_override=openai` or with no explicit provider override. Cannot be changed mid-session. |
| `store` | bool | `false` | `KENT_STORE` |  | Sets OpenAI Responses `store=true` for main model requests. |
| `allow_non_cwd_edits` | bool | `false` | `KENT_ALLOW_NON_CWD_EDITS` |  | Lets first-class file edit tools edit files outside the workspace root. This is not sandboxing - model can still bypass this easily. |
| `model_context_window` | int | `272000` | `KENT_MODEL_CONTEXT_WINDOW` |  | Explicit context-window size used for compaction and token accounting. Must be at least `40000`. |
| `context_compaction_threshold_tokens` | int | `258400` | `KENT_CONTEXT_COMPACTION_THRESHOLD_TOKENS` |  | Auto-compaction threshold. Must be `> 0`, `< model_context_window`, and at least `50%` of `model_context_window`. The default is derived from the default context window. |
| `pre_submit_compaction_lead_tokens` | int | `35000` | `KENT_PRE_SUBMIT_COMPACTION_LEAD_TOKENS` |  | Fixed pre-submit runway reserve before auto-compaction. Kent compacts before sending the next user prompt once (`context_compaction_threshold_tokens` - this threshold) is reached. |
| `minimum_exec_to_bg_seconds` | int | `15` | `KENT_MINIMUM_EXEC_TO_BG_SECONDS` |  | Default floor for `exec_command` yield time before it moves to background and lets Kent manage it asynchronously. Must be `> 0`. Use if model frequently expects your commands to complete fast, they background, and force model to poll for them. |
| `compaction_mode` | string | `local` | `KENT_COMPACTION_MODE` |  | Allowed: `native`, `local`, `none`. `native` prefers provider-native compaction and falls back to local compaction. `local` always uses local summary compaction. `none` disables auto-compaction and makes manual compaction fail. |
| `cache_warning_mode` | string | `default` | `KENT_CACHE_WARNING_MODE` |  | Prompt-cache warning policy. Allowed: `off`, `default`, `verbose`. `default` catches unwanted invalidations and keeps them in detail mode. `verbose` includes everything from `default`, surfaces cache warnings in ongoing mode too, and a broader range of warnings. |
| `shell_output_max_chars` | int | `16000` | `KENT_SHELL_OUTPUT_MAX_CHARS` |  | Output budget for shell tools and background-shell notices before they are truncated. |
| `bg_shells_output` | string | `default` | `KENT_BG_SHELLS_OUTPUT` |  | Background-shell output mode (injection of shell outputs into model context). Allowed: `default`, `verbose`, `concise`. Verbose dumps all output into the main agent's model. Concise forces it to read output files. Default outputs truncated previews + gives a file path. |
| `shell.postprocessing_mode` | string | `builtin` | `KENT_SHELL_POSTPROCESSING_MODE` |  | Semantic post-processing mode for `exec_command`. Allowed: `none`, `builtin`, `user`, `all`. `builtin` enables Kent processors only. `all` runs Kent processors first, then your hook. |
| `shell.postprocess_hook` | string | `""` | `KENT_SHELL_POSTPROCESS_HOOK` |  | Optional executable/script path for a single local command post-processing hook. Kent sends JSON on stdin and expects JSON on stdout. |
| `prevent_sleep` | string | `active` | `KENT_PREVENT_SLEEP` |  | Prevent system sleep while Kent is running. Allowed: `always` (while the server process is live), `active` (while any agent is working, plus up to one minute of idle-confirmation grace), `never` (disabled). Only system sleep is inhibited; screensaver and display sleep are unaffected. |
| `timeouts.model_request_seconds` | int | `400` | `KENT_TIMEOUTS_MODEL_REQUEST_SECONDS` | `kent run --model-timeout-seconds` | Model request timeout. Must be `> 0`. For non-streaming requests it bounds the whole request. For streaming responses it is a per-event idle window: the request is only aborted when no streaming activity arrives within this duration (measured from dispatch, so it also bounds time-to-first-event), letting a healthy long generation stream past it while a dead stream fails fast. |


### Workflow

| Key | Type | Default | Env | Description |
| --- | --- | --- | --- | --- |
| `workflow.completion_mode` | string | `auto` | `KENT_WORKFLOW_COMPLETION_MODE` | Default completion mode for workflow agent nodes that inherit the global default. Allowed: `auto`, `structured_output`, `tool`, `shell_command`, `unstructured_output`. |
| `workflow.concurrency` | int | `5` | `KENT_WORKFLOW_CONCURRENCY` | Maximum number of workflow agent runs scheduled concurrently. Must be `> 0`. |
| `workflow.max_invalid_completion_attempts` | int | `5` | `KENT_WORKFLOW_MAX_INVALID_COMPLETION_ATTEMPTS` | Number of invalid workflow completion attempts allowed before Kent interrupts the run. Must be `> 0`. |
| `workflow.subagents` | bool | `false` |  | Allows workflow agents to launch eligible custom roles. |


### Supervisor
Configure the supervisor agent that oversees model changes ("reviewer" is the legacy name of the feature).


| Key | Type | Default | Env | Description |
| --- | --- | --- | --- | --- |
| `reviewer.frequency` | string | `edits` | `KENT_REVIEWER_FREQUENCY` | Allowed: `off`, `all`, `edits`. `all` runs the reviewer after every completed assistant turn. `edits` runs it only after successful first-class file edits. |
| `reviewer.model` | string | inherits `model` | `KENT_REVIEWER_MODEL` | Separate model for the reviewer pass. If unset, Kent uses main `model`. |
| `reviewer.thinking_level` | string | inherits `thinking_level` | `KENT_REVIEWER_THINKING_LEVEL` | Allowed: `low`, `medium`, `high`, `xhigh`. |
| `reviewer.model_verbosity` | string | inherits `model_verbosity` | `KENT_REVIEWER_MODEL_VERBOSITY` | Text verbosity hint for supported reviewer models. Allowed: `""`, `low`, `medium`, `high`. |
| `reviewer.provider_override` | string | inherits `provider_override` | `KENT_REVIEWER_PROVIDER_OVERRIDE` | Forces provider family for the reviewer model. Allowed: `openai`, `anthropic`. |
| `reviewer.openai_base_url` | string | inherits `openai_base_url` for OpenAI-family reviewer providers | `KENT_REVIEWER_OPENAI_BASE_URL` | OpenAI-compatible base URL for the reviewer model. Non-OpenAI endpoints can run without Kent auth when the server accepts anonymous requests. |
| `reviewer.auth` | string | `inherit` | `KENT_REVIEWER_AUTH` | Reviewer auth policy. `inherit` uses Kent's configured auth. `none` sends no `Authorization` header; providers that require auth return their normal runtime error. |
| `reviewer.model_context_window` | int | inherits `model_context_window` | `KENT_REVIEWER_MODEL_CONTEXT_WINDOW` | Explicit reviewer context-window size sent to the reviewer provider. The effective value must be at least `40000`. |
| `reviewer.system_prompt_file` | string | `""` |  | Path to a custom supervisor system prompt file. Relative paths resolve from the config file directory. Workspace config overrides global config; |
| `reviewer.timeout_seconds` | int | `120` | `KENT_REVIEWER_TIMEOUT_SECONDS` | Reviewer HTTP timeout. Must be `> 0`. |
| `reviewer.verbose_output` | bool | `false` | `KENT_REVIEWER_VERBOSE_OUTPUT` | Controls whether reviewer suggestion text is shown at all. When `false`, Kent only shows the concise reviewer result/status line. When `true`, Kent shows the full suggestion list at the moment the reviewer issues it, and the later reviewer status stays concise after the follow-up is applied or ignored. |



### Supervisor Capability Overrides
Use these for custom supervisor models or supervisor providers when the built-in registry is not enough.


| Key (inside `reviewer.provider_capabilities.*`)| Type | Default | Env | Description |
| --- | --- | --- | --- | --- |
| `supports_reasoning_effort` | bool | inherits `model_capabilities.supports_reasoning_effort` | `KENT_REVIEWER_MODEL_CAPABILITIES_SUPPORTS_REASONING_EFFORT` | Override-marks the reviewer model as supporting reasoning effort / thinking levels. |
| `supports_vision_inputs` | bool | inherits `model_capabilities.supports_vision_inputs` | `KENT_REVIEWER_MODEL_CAPABILITIES_SUPPORTS_VISION_INPUTS` | Marks the reviewer model as supporting multimodal image and PDF inputs. |
| `provider_id` | string | inherits `provider_capabilities.provider_id` | `KENT_REVIEWER_PROVIDER_CAPABILITIES_PROVIDER_ID` | Required whenever you set reviewer provider capability overrides. |
| `supports_responses_api` | bool | inherits `provider_capabilities.supports_responses_api` | `KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_RESPONSES_API` | Marks the reviewer provider as supporting the Responses API. |
| `supports_responses_compact` | bool | inherits `provider_capabilities.supports_responses_compact` | `KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_RESPONSES_COMPACT` | Marks the reviewer provider as supporting server-side compaction. |
| `supports_request_input_token_count` | bool | inherits `provider_capabilities.supports_request_input_token_count` | `KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_REQUEST_INPUT_TOKEN_COUNT` | Marks the reviewer provider as supporting exact request input-token counting. |
| `supports_prompt_cache_key` | bool | inherits `provider_capabilities.supports_prompt_cache_key` | `KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_PROMPT_CACHE_KEY` | Marks the reviewer provider as accepting prompt cache keys. |
| `supports_native_web_search` | bool | inherits `provider_capabilities.supports_native_web_search` | `KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_NATIVE_WEB_SEARCH` | Marks the reviewer provider as supporting native web search. |
| `supports_reasoning_encrypted` | bool | inherits `provider_capabilities.supports_reasoning_encrypted` | `KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_REASONING_ENCRYPTED` | Marks the reviewer provider as supporting encrypted reasoning items. |
| `supports_server_side_context_edit` | bool | inherits `provider_capabilities.supports_server_side_context_edit` | `KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_SERVER_SIDE_CONTEXT_EDIT` | Marks the reviewer provider as supporting server-side context editing. |
| `supports_provider_verbosity` | bool | inherits `provider_capabilities.supports_provider_verbosity` | `KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_PROVIDER_VERBOSITY` | Controls Responses `text.verbosity` for unknown reviewer models; known models use catalog facts. |
| `is_openai_first_party` | bool | inherits `provider_capabilities.is_openai_first_party` | `KENT_REVIEWER_PROVIDER_CAPABILITIES_IS_OPENAI_FIRST_PARTY` | Marks the reviewer provider as first-party OpenAI semantics. |

### Model Capability Overrides

Use these only for custom or alias models when the built-in model registry is not enough.

| Key | Type | Default | Env | Description |
| --- | --- | --- | --- | --- |
| `model_capabilities.supports_reasoning_effort` | bool | `false` | `KENT_MODEL_CAPABILITIES_SUPPORTS_REASONING_EFFORT` | Override-marks the configured model as supporting reasoning effort / thinking levels. |
| `model_capabilities.supports_vision_inputs` | bool | `false` | `KENT_MODEL_CAPABILITIES_SUPPORTS_VISION_INPUTS` | Marks the configured model as supporting multimodal image and PDF inputs. |

If both values stay `false`, Kent falls back to the built-in model capability registry.

### Provider Capability Overrides

Use these only for custom providers or models (such as local models).


| Key | Type | Default | Env | Description |
| --- | --- | --- | --- | --- |
| `provider_capabilities.provider_id` | string | `""` | `KENT_PROVIDER_CAPABILITIES_PROVIDER_ID` | Required whenever you set provider capability overrides. |
| `provider_capabilities.supports_responses_api` | bool | `false` | `KENT_PROVIDER_CAPABILITIES_SUPPORTS_RESPONSES_API` | Marks the provider as supporting the Responses API. |
| `provider_capabilities.supports_responses_compact` | bool | `false` | `KENT_PROVIDER_CAPABILITIES_SUPPORTS_RESPONSES_COMPACT` | Marks the provider as supporting server-side compaction. |
| `provider_capabilities.supports_native_web_search` | bool | `false` | `KENT_PROVIDER_CAPABILITIES_SUPPORTS_NATIVE_WEB_SEARCH` | Marks the provider as supporting native web search. |
| `provider_capabilities.supports_reasoning_encrypted` | bool | `false` | `KENT_PROVIDER_CAPABILITIES_SUPPORTS_REASONING_ENCRYPTED` | Marks the provider as supporting encrypted reasoning items. |
| `provider_capabilities.supports_server_side_context_edit` | bool | `false` | `KENT_PROVIDER_CAPABILITIES_SUPPORTS_SERVER_SIDE_CONTEXT_EDIT` | Marks the provider as supporting server-side context editing. |
| `provider_capabilities.supports_provider_verbosity` | bool | `false` | `KENT_PROVIDER_CAPABILITIES_SUPPORTS_PROVIDER_VERBOSITY` | Controls Responses `text.verbosity` for unknown models; known models use catalog facts. |
| `provider_capabilities.is_openai_first_party` | bool | `false` | `KENT_PROVIDER_CAPABILITIES_IS_OPENAI_FIRST_PARTY` | Marks the provider as first-party OpenAI semantics, which gates some Responses-specific behavior such as fast mode and phase protocol features. |

Known models always use the built-in catalog. For unknown non-empty models, `supports_provider_verbosity` controls whether Kent sends `text.verbosity`. Without a provider override, OpenAI and ChatGPT Codex built-ins enable it; OpenAI-compatible and Anthropic built-ins disable it.

### Tools

`[tools]` is a per-tool boolean table in `config.toml`.
File-based tool toggles merge with defaults. `KENT_TOOLS` and `kent run --tools` behave differently: they replace the entire tool set with the CSV you provide.


| Key | Default | What enabling it exposes |
| --- | --- | --- |
| `tools.ask_question` | task-dependent | Tool to ask interactive questions |
| `tools.shell` | `true` | The primary shell tool. Internally this maps to `exec_command`. |
| `tools.patch` | model-dependent | Freeform patch grammar edit tool |
| `tools.edit` | model-dependent | JSON text replacement/create/delete edit tool. Intended for models that are not trained to apply patches. |
| `tools.trigger_handoff` | `true` | Tool agents can use to proactively compact their own context. |
| `tools.view_image` | model-dependent | Ability to view PNG, JPEG, still GIF, and PDF files (if supported) |
| `tools.web_search` | provider-dependent | Tool to search the web |
| `tools.write_stdin` | `true` | Interaction with background shells. |

Notes:

- `tools.web_search = true` does not force web search on. Native search still depends on `web_search = "native"` and provider support.
- `tools.patch` and `tools.edit` are mutually exclusive. If both are left at their defaults, Kent chooses `patch` for first-party OpenAI providers or `gpt-*` model names, and `edit` otherwise. To force `edit`, set `edit = true` and `patch = false`.

## Ripgrep config

Kent also installs an optimized, editable ripgrep config at:

```text
~/.kent/rg.conf
```

Kent creates `rg.conf` in the config+data root when missing and exports it to shell tools via `RIPGREP_CONFIG_PATH` only when you have not already set `RIPGREP_CONFIG_PATH` yourself. In an isolated root this is the root's own `rg.conf`, not the default `~/.kent/rg.conf`.

### Subagents

`[subagents.<role>]` is a file-only table for named headless subagent roles. Fast is always-present, but you can add custom agents here.

More info on the [Subagents page](../headless/).

### Skills

`[skills]` is a file-only per-skill boolean table in `config.toml` to disable unneeded skills. Keys are matched case-insensitively.

```toml
[skills]
"<skill name>" = true # | false
```

Notes:

- Skill toggles are only applied when Kent creates a new conversation/session.
- Use `"quoted names"` to refer to skill keys containing spaces.
