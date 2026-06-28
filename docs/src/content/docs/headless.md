---
title: Headless runs
description: Headless Kent runs, scriptable output modes, and how interactive Kent uses the same mechanism for subagents.
---

Kent supports a headless, non-interactive run mode via `kent run`.
When the interactive Kent session uses subagents, it does so by launching separate headless Kent runs.
This keeps the subagent path transparent and scriptable: the feature Kent uses internally is scriptable and contextual.

Run a single prompt:

```bash
kent run --agent fast "summarize the unstaged changes in this repo"
```

Continue an existing headless session:

```bash
kent run --continue <session-id> "<follow-up>"
```

`kent run` needs a server connection to keep long-running shells and agents properly orchestrated. If you want to script kent runs, make sure the [Server](../server/) is running.

## Subagent Roles

Roles are needed to create specialized subagent types for different tasks. Treat them like different employees or specialists.

`--agent <role>` selects a named subagent role from `[subagents.<role>]` in the local or global config file. `--agent default` clears a resumed role and uses the base settings; `none` and `self` are not run-agent selectors. To define a new role, edit the config:

```toml
[subagents.research]
model = "gpt-5.5"
thinking_level = "xhigh"
system_prompt_file = "research-agent.md"
description = "Use when you need fast, smart general-purpose researcher for deep thinking or complicated plans."
priority_request_mode = true
agent_callable = true

[subagents.research.tools]
patch = false

[subagents.research.skills]
"kent-dogfooding" = false
```

- Set `agent_callable = false` to disallow agents to call that subagent role on their own.
- The built-in `fast` role exists even without config.
- Subagent roles inherit the main config and then override only the keys you set in that role table.

Useful role-specific keys include:

- `model`, `provider_override`, `openai_base_url`, etc.
- `thinking_level`, `model_verbosity`, `priority_request_mode`
- `system_prompt_file`
- `description`, `agent_callable`
- `[subagents.<role>.tools]`
- `[subagents.<role>.skills]`

For the full list of shared overrides, see [Configuration](../config/).

## Session Behavior

Headless runs are non-interactive. They do not stop to ask the human operator questions mid-run, issue tool preambles, or support the Supervisor. That makes them more suitable for background execution, automation, and saves tokens.

You can talk to a headless agent if you select it in the `/resume` (session picker), but if expect the model to be less talkative overall due to how the run was started.

Sessions with a goal cannot be continued headlessly. Clear the goal from the interactive session before using `kent run --continue`.

## Workspace Binding

Headless runs fail if the selected workspace is not already attached to a Kent project.
This is needed to enable functionality related to project management and allows remote execution, but sometimes comes as a limitation where you want to run subagents in different repos. To fix the error, you simply need to approve workspace (git repo, folder etc.) binding:

- `kent project` prints the project id for the bound workspace at `path` or `cwd`. Use to learn project IDs.
- `kent attach <path>` attaches another workspace at [path] to the project already bound to `cwd`.
- `kent attach --project <project-id> [path]` attaches using the ID.
- `kent rebind <session-id> <new-path>` retargets one session to a different workspace root, for example when workspace has moved locally.

The main agent will fix these issues on their own.

## Output Modes

The default headless output mode is plain final text. For scripting, use JSON mode:

```bash
kent run --output-mode=json "summarize the repo" | jq
```

JSON mode emits exactly one final object on `stdout`.

```json
{
  "status": "ok",
  "result": "...",
  "session_id": "...",
  "session_name": "...",
  "continue_id": "...",
  "continue_command": "kent run --continue ... \"follow-up\"",
  "warnings": ["..."],
  "duration_ms": 1234
}
```

On failure, JSON mode emits `status: "error"` and an `error` object instead of `result`.
If a selected subagent role emits startup warnings, `final-text` prints them above the model response and JSON mode returns them in `warnings`.

---

Supported run-specific flags:

| Flag | Description |
| --- | --- |
| `--timeout` | Optional run timeout such as `30s`, `5m`, or `1h`. Default is no timeout. |
| `--output-mode` | `final-text` or `json`. Default is `final-text`. |
| `--progress-mode` | `quiet` or `stderr`. Default is `quiet`. |
| `--continue` | Continue a previous session by id. |
| `--agent` | Select a named subagent role from `config.toml`; use `default` for the base role. |
| `--fast` | Shortcut for the built-in `fast` subagent role. |
