---
title: Lifecycle Hooks
description: Send terminal-client lifecycle events to a local command.
---

Lifecycle hooks invoke a local command when an interactive Kent terminal session starts, finishes work, fails, waits for input, or starts compaction. The hook receives one JSON event on stdin.

## Configuration

Set the command and any fixed arguments in the global `config.toml`:

```toml
[hooks.client]
lifecycle = ["python3", "/absolute/path/lifecycle_hook.py", "/absolute/path/lifecycle-events.jsonl"]
```

The global file is `~/.kent/config.toml` unless Kent uses another persistence root. `hooks.client.lifecycle` cannot be set in workspace config, through an environment variable or CLI flag, in a subagent role, or on the server. The array must contain a non-blank executable and non-blank arguments.

Restart the terminal client after changing the command.

The terminal client runs the hook in its own environment and current directory. For remote attachments, the command runs on the client machine rather than the server. Desktop clients, unattended `kent run`, subagents, and server-only processes do not run lifecycle hooks.

Lifecycle hooks do not transform shell output. Use [command post-processing](../command-postprocessing/) for synchronous `exec_command` output processing.

## Events

The configured command receives every lifecycle category:

| Category | `hook_event_name` | Details |
| --- | --- | --- |
| `session.start` | `SessionStart` | `kind` is `new` or `resumed`. |
| `task.complete` | `Stop` | `final_answer` contains the final response; `work_performed` reports whether the run performed tool work. |
| `task.error` | `PostToolUseFailure` | `diagnostic` describes the runtime failure. |
| `input.required` | `PermissionRequest` | `kind` is `question` or `approval`; `summary` contains the prompt. |
| `resource.limit` | `PreCompact` | `compaction_mode` identifies the compaction mode. |

`category` is authoritative. `hook_event_name` is an OpenPeon-compatible alias.

`task.complete` requires an assistant final answer, and `task.error` requires a failed runtime result. Interruptions, shell activity, and successful runs without an assistant final answer emit neither category.

`resource.limit` emits for every compaction start, including manual compaction.

## JSON contract

Each invocation receives one JSON object with `schema_version` set to `1`:

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
    "final_answer": "The requested changes are complete.",
    "work_performed": true
  }
}
```

`context` contains the session ID, session title, and workflow task ID when available. Absent values are omitted. `focused` reports whether the terminal client was focused when it observed the event, and `occurred_at` is a UTC timestamp.

Payloads exclude filesystem paths, transcript history, tool input, command output, hidden reasoning, credentials, and internal runtime identifiers.

## Delivery and failures

Hook delivery is asynchronous and best-effort. Events are not persisted or retried, saturated clients may drop events, and invocations may overlap or complete out of order.

Each invocation has a 30-second timeout. Kent ignores stdout and retains up to 4 KiB of stderr for diagnostics. Launch failures, non-zero exits, and timeouts produce a transient terminal error; repeated failures may be coalesced into one notice containing the total count and latest diagnostic. A failed invocation does not disable subsequent invocations.

Closing the session cancels running hook commands without waiting for them. Descendant processes may continue. Hook output and failures do not change agent or server behavior.

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

Use fixed command arguments for receiver configuration; event data arrives through stdin.
