---
title: Lifecycle Hooks
description: Run a local command when an interactive terminal session changes state.
---

Lifecycle hooks run a local command for events observed by an interactive Kent terminal client. Each invocation receives one JSON event on stdin.

## Configuration

Add the command and any fixed arguments to the global `config.toml`:

```toml
[hooks.client]
lifecycle = ["python3", "/absolute/path/lifecycle_hook.py"]
```

The global config is `~/.kent/config.toml` unless Kent uses another persistence root. `hooks.client.lifecycle` has no workspace, environment-variable, CLI, or subagent-role override. The executable must be non-blank, as must any arguments.

The terminal client reads this setting at startup. The command inherits the client's environment and current directory.

For remote attachments, the command runs on the terminal client's machine, not the server. Desktop clients, headless runs, subagents, and server processes do not run lifecycle hooks.

Use [command post-processing](../command-postprocessing/) instead to transform `exec_command` output.

## Events

Kent sends these categories to the configured command:

| `category` | `hook_event_name` | `details` |
| --- | --- | --- |
| `session.start` | `SessionStart` | `kind` is `new` or `resumed`. |
| `task.complete` | `Stop` | `final_answer` and `work_performed` describe the completed run. |
| `task.error` | `PostToolUseFailure` | `diagnostic` describes the runtime failure. |
| `input.required` | `PermissionRequest` | `kind` is `question` or `approval`; `summary` contains the prompt. |
| `resource.limit` | `PreCompact` | `compaction_mode` identifies the compaction mode that started. |

`category` is the canonical event name. `hook_event_name` is an OpenPeon-compatible alias.

`task.complete` requires an assistant final answer. `task.error` requires a failed runtime result. Interruptions and successful runs without an assistant final answer emit neither event.

`resource.limit` emits for every compaction start, including manual compaction.

## Payload

This `task.complete` payload shows the schema:

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
    "session_title": "Review API changes",
    "workflow_task_id": "ENG-42"
  },
  "details": {
    "final_answer": "The requested changes are complete.",
    "work_performed": true
  }
}
```

`schema_version` identifies the Kent payload schema. `cesp_version` and `hook_event_name` provide OpenPeon compatibility.

`occurred_at` is a UTC timestamp. `focused` reports whether the terminal client had focus when it observed the event. `context` includes the session ID, session title, and workflow task ID when available; absent values are omitted.

Payloads do not include filesystem paths, transcript history, tool input, command output, hidden reasoning, credentials, or internal runtime identifiers.

## Delivery

Delivery is asynchronous and best-effort. Events are not persisted or retried, bursts may drop events, and invocations may overlap or complete out of order.

Each invocation has a 30-second timeout. Kent ignores stdout. Launch failures, non-zero exits, and timeouts produce a terminal notice using up to 4 KiB of stderr. Repeated failures may be combined into one notice with the total count and latest diagnostic. A failure does not disable the hook.

Closing the session cancels running hook commands without waiting for them; descendant processes may continue. Hook output and failures do not change agent or server behavior.
