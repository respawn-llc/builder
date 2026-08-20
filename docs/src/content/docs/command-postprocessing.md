---
title: Bash Hooks
description: Configure Kent's shell command post-processing and ship your own hook.
---

Kent post-processes shell command output before it is shown to the model to normalize output, reduce command noise, and add useful execution context.

## Config

Configure command post-processing under `[shell]` in `~/.kent/config.toml`:

```toml
[shell]
postprocessing_mode = "all" # none | builtin | user | all
postprocess_hook = "~/.kent/shell_postprocess_hook"
```

Omit `postprocess_hook` when no hook is configured; Kent silently skips the hook stage.

### `postprocessing_mode`

Allowed values:

- `none`: disable command post-processing.
- `builtin`: run Kent's output cleanup and built-in processing.
- `user`: run Kent's output cleanup, then run the configured hook when present; an omitted hook is skipped.
- `all`: run Kent's output cleanup and built-in processing, then run the configured hook when present; an omitted hook is skipped.

In `builtin`, `user`, and `all`, Kent's final model-visible command-output pass limits each line to 1,000 Unicode code points; oversized lines keep only their prefix and end with `… [N characters omitted]`, where `N` is exact. This runs after user-hook replacement; `none` bypasses the limit, and Kent operational warnings are not command-output lines.

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

- `original_output`: sanitized command output before built-in processing
- `current_output`: command output after built-in processing, or `original_output` when unchanged

Hook **must** return JSON like:

```json
{
  "processed": true,
  "replaced_output": "...new output..."
}
```

Return `{"processed": false}` for no-op passthrough. An omitted `postprocess_hook` is skipped without a warning. If a configured hook executable is missing, times out, exits nonzero, or returns invalid JSON, Kent falls back to the current output and reports a warning.
