---
title: Bash Hooks
description: Configure Kent's shell command post-processing and ship your own hook.
---

Kent post-processes shell command output before it is shown to the model to normalize output, reduce command noise, and add useful execution context.

Successful commands with visible output return that output. A completed command without visible output reports its exit code and explicit no-output completion.

## Config

Configure command post-processing under `[shell]` in `~/.kent/config.toml`:

```toml
[shell]
postprocessing_mode = "all" # none | builtin | user | all
postprocess_hook = "~/.kent/shell_postprocess_hook"
```

### `postprocessing_mode`

Allowed values:

- `none`: disable command post-processing. Not recommended.
- `builtin`: run Kent's sanitizer and built-in processors. Kent has multiple built-in processors that are generally applicable to all projects and improve model performance.
- `user`: run Kent's sanitizer, then your configured hook. Only recommended if your post-processors fully replace what is done in Kent.
- `all`: run Kent's sanitizer, built-ins, then your configured hook

:::info[Not a hooks replacement]
The model can always control whether a command is post-processed. If the output has issues, the agent will bypass command post-processing, so do not treat this as hooks replacement - hooks are not optional.
:::

## Protocol

### Input

Kent sends JSON like:

```json
{
  "tool_name": "exec_command",
  "command": "go test ./...",
  "parsed_args": ["go", "test", "./..."],
  "command_name": "go",
  "workdir": "/abs/workdir",
  "original_output": "...sanitized command output...",
  "current_output": "...built-in processed output or original output...",
  "exit_code": 0,
  "backgrounded": false,
  "max_display_chars": 16000
}
```

Your hook receives both:

- `original_output`: sanitized command output before Kent semantic shaping
- `current_output`: current Kent output after built-ins, or the same as `original_output` if no built-in handled it

This lets your hook either add on top of Kent defaults or replace them completely.
In `all` mode, the hook runs after built-ins even when a built-in processor stops the built-in chain.

Hook **must** return JSON like:

```json
{
  "processed": true,
  "replaced_output": "...new output..."
}
```

Return `{"processed": false}` for no-op passthrough. If the hook is missing, times out, exits nonzero, or returns invalid JSON, Kent falls back to the current output and reports a warning.
