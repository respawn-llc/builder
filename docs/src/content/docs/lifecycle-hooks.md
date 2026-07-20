---
title: Lifecycle Hooks
description: Run a client-local command for interactive TUI lifecycle events.
---

Lifecycle hooks send interactive TUI events to a local command. They are separate from the synchronous [shell command post-processing hook](../command-postprocessing/), which transforms `exec_command` output.

## Configuration

Set the executable and fixed arguments in the controlling TUI client's global `config.toml`:

```toml
[hooks.client]
lifecycle = ["python3", "/absolute/path/lifecycle_hook.py", "/absolute/path/lifecycle-events.jsonl"]
```

The global file is `~/.kent/config.toml` by default and follows the selected persistence root. `hooks.client.lifecycle` has no workspace, environment-variable, CLI, subagent-role, or server override. An empty array or blank argument is invalid.

The TUI loads the command at startup and captures it for each opened session. Restart the controlling TUI to apply a changed command.

Every controlling Go TUI executes its own command, including a TUI attached to a server on another machine. The command runs on the TUI machine. Desktop clients, unattended `kent run`, subagents, and server-only processes do not execute lifecycle hooks.

## Events

The command receives every supported category and filters the events it needs.

| Category | `hook_event_name` | Details |
| --- | --- | --- |
| `session.start` | `SessionStart` | `kind` is `new` or `resumed`. |
| `task.complete` | `Stop` | `final_answer` contains the final response; `work_performed` is true when a completed step recorded at least two tool starts. |
| `task.error` | `PostToolUseFailure` | `diagnostic` contains an actual runtime failure. |
| `input.required` | `PermissionRequest` | `kind` is `question` or `approval`; `summary` contains the prompt. |
| `resource.limit` | `PreCompact` | `compaction_mode` identifies the compaction that started. |

`category` is authoritative. `hook_event_name` is an OpenPeon-compatible alias.

`task.complete` emits only for an assistant final-answer result. `task.error` emits only for a failed runtime result. Interruptions, successful workflow completion without an ordinary final answer, shell/background activity, and other completed-without-final-answer outcomes emit neither terminal category.

`resource.limit` emits for every compaction start, including manual compaction.

Delivery follows the live event stream. Connection or subscription gaps may miss or repeat hooks; Kent does not reconstruct, acknowledge, retry, persist, or deduplicate them.

The controlling Go TUI advertises terminal-fact support during its protocol handshake. Protocol-compatible clients that do not advertise support receive the legacy transcript stream without `live_run_finished`.

## JSON contract

Each invocation receives one schema-v1 JSON object on stdin:

```json
{
  "schema_version": 1,
  "cesp_version": "1.0",
  "scope": "client",
  "category": "task.complete",
  "hook_event_name": "Stop",
  "occurred_at": "2026-07-20T10:15:30Z",
  "focused": false,
  "context": {
    "session_id": "4f44b818-e9d5-4ff4-a4ab-b9bc03bb776f",
    "session_title": "Lifecycle receiver",
    "workflow_task_id": "BUI-51"
  },
  "details": {
    "final_answer": "Implemented and verified the lifecycle receiver.",
    "work_performed": true
  }
}
```

`context` includes the session ID, session title, and workflow Task ID when available. Optional values are omitted rather than encoded as empty strings. Every event includes the TUI's observed focus state and a UTC timestamp.

The payload excludes server paths, workspace paths, transcript history, tool input, command output, hidden reasoning, credentials, and internal runtime, step, prompt, approval, or subscription identifiers.

Kent uses ordinary JSON encoding and preserves source text without a lifecycle-specific text cap, whole-object budget, or truncation metadata.

## Process behavior

Hook execution is asynchronous and best-effort:

- the event channel holds up to 64 pending events;
- up to 64 hook processes may run concurrently per TUI session;
- queue or process-slot saturation silently drops the new event;
- invocations have no launch, execution, or completion ordering guarantee;
- each invocation has a 30-second timeout;
- stdout is ignored;
- stderr is retained up to 4 KiB for diagnostics;
- the command inherits the TUI's environment and current directory.

Launch failures, non-zero exits, and timeouts produce a transient TUI error and diagnostic log. Each event attempts the command; a missing or non-executable command does not disable the hook.

Closing the session stops intake, requests cancellation of active direct hook processes, and returns without waiting for them. Background waiters reap the processes asynchronously. Kent does not guarantee termination of hook descendants.

Hook output and failures never change agent or server behavior.

## Minimal receiver

This receiver appends each JSON object to the fixed path passed in the command arguments:

```python
#!/usr/bin/env python3
import json
import os
import sys

event = json.load(sys.stdin)
line = json.dumps(event, separators=(",", ":")).encode() + b"\n"
fd = os.open(sys.argv[1], os.O_APPEND | os.O_CREAT | os.O_WRONLY, 0o600)
with os.fdopen(fd, "ab") as output:
    output.write(line)
```

Use fixed arguments for receiver configuration. Event data is available through JSON stdin.
