---
title: Lifecycle Hooks
description: Run a client-local command for interactive TUI lifecycle events.
---

Lifecycle hooks publish read-only interactive TUI events to one client-local command. They are separate from the synchronous [shell command post-processing hook](../command-postprocessing/), which transforms `exec_command` output and returns replacement JSON.

## Configuration

Set one executable and its fixed arguments in the controlling TUI client's global `config.toml`:

```toml
[hooks.client]
lifecycle = ["python3", "/absolute/path/lifecycle_hook.py", "/absolute/path/lifecycle-events.jsonl"]
```

The global file is `~/.kent/config.toml` by default and follows the selected persistence root. `hooks.client.lifecycle` is TOML-only: workspace config, environment variables, CLI flags, and servers cannot set or override it. An empty array or blank argument is invalid.

The TUI copies the command when it opens a session. Edits apply after reopening the session or restarting the controlling TUI.

Every interactive controlling TUI executes its own command, including a TUI attached to a Kent server on another machine. The command runs on the TUI machine. Desktop clients, unattended `kent run` and subagent runs, and server-only processes do not execute lifecycle hooks.

## Events

The command receives every supported category and filters the events it needs.

| Category | `hook_event_name` | Details |
| --- | --- | --- |
| `session.start` | `SessionStart` | `kind` is `new` or `resumed`. Both include the materialized session ID. |
| `task.complete` | `Stop` | `final_answer` is the last Markdown preview; `work_performed` reports whether any turn in the drained batch used at least two tool calls. |
| `task.error` | `PostToolUseFailure` | `diagnostic` describes an eligible stop without a final answer. |
| `input.required` | `PermissionRequest` | `kind` is `question` or `approval`; `summary` contains the user-visible Markdown. |
| `resource.limit` | `PreCompact` | `compaction_mode` identifies automatic compaction. User-requested compaction is excluded. |

`category` is authoritative. `hook_event_name` is a lean OpenPeon-compatible alias and does not change Kent semantics or fabricate tool details.

`session.start` emits once after successful planning and TUI attachment preparation. `task.complete` emits after queued work drains and the supervisor finishes or is disabled. A drained batch emits exactly one terminal category: `task.complete` or `task.error`.

Interruptions, successful workflow completion without an ordinary final answer, and client/server connection loss do not emit `task.error`. `task.complete`, `task.error`, and `resource.limit` are live-only and are not reconstructed from transcript hydration.

When a TUI opens, its pending snapshot emits `input.required` for each unresolved question or approval. A stream gap and reopen may repeat unresolved items in the same attachment. Lifecycle hooks add no pending-item cap.

## JSON contract

Each invocation receives exactly one schema-v1 JSON object on stdin:

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

`context` includes `session_id`, `session_title`, and `workflow_task_id` only when available. Optional values are omitted rather than encoded as empty strings. The payload contains no server execution root, workspace path, runtime ID, step ID, prompt ID, approval ID, transcript history, tool input, hidden reasoning, credentials, or full command output.

`focused` is present on every event and is `false` when focus is unknown. Event timestamps use UTC RFC 3339.

Each variable user-visible summary is capped at 4 KiB. The complete JSON object is capped at 32 KiB. When content is shortened, the object adds:

```json
{
  "truncation": {
    "fields": ["details.final_answer"]
  }
}
```

Possible field names are `context.session_title`, `details.final_answer`, `details.diagnostic`, and `details.summary`.

## Process behavior

Kent starts one hook process at a time per TUI attachment and preserves accepted event order. Delivery is best-effort and at most once:

- enqueueing never waits for the hook;
- events are not retried;
- each invocation has a five-second deadline;
- stdout is ignored;
- stderr is bounded and used for failure diagnostics;
- the command inherits the controlling TUI's environment and current directory;
- Kent does not set a subprocess working directory.

A missing or non-executable program reports one non-blocking TUI error and disables hooks for that attachment. A process that starts and then exits nonzero or times out reports an error but remains eligible for later events. Queue overload drops the new event and reports an error. Hook failures never emit `task.error` and never alter agent behavior.

TUI shutdown cancels and reaps the active process tree, drops queued events, and does not wait for the full invocation deadline.

## Minimal receiver

This receiver appends each JSON object to the fixed path passed in the command argv:

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

Use the fixed argv for receiver configuration. Event data is available only through JSON stdin.
