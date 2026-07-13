---
title: Bash Hooks
description: Configure Kent's shell command post-processing and ship your own hook.
---

Kent post-processes shell command output before it is shown to the model to normalize output, reduce command noise, and add useful execution context.

Successful commands with visible output return that output. A completed command without visible output reports its exit code and explicit no-output completion.

Use `kent worktree` for worktree operations. Kent warns about direct `git worktree` commands.

Raw shell output skips post-processing.

## Config

Configure command post-processing under `[shell]` in `~/.kent/config.toml`:

```toml
[shell]
postprocessing_mode = "all" # none | builtin | user | all
postprocess_hook = "~/.kent/shell_postprocess_hook"
```

### `postprocessing_mode`

Allowed values:

- `none`: disable command post-processing.
- `builtin`: run Kent's output cleanup and built-in processing.
- `user`: run Kent's output cleanup, then your configured hook.
- `all`: run Kent's output cleanup, built-in processing, then your configured hook.

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

Return `{"processed": false}` for no-op passthrough. If the hook is missing, times out, exits nonzero, or returns invalid JSON, Kent falls back to the current output and reports a warning.
