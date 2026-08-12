---
title: Kent Server
description: Kent's local client-server architecture and background service management.
---

Kent runs all its work through a local server process. Frontends are clients: TUI, desktop app, headless runs, and other local integrations all need the server to be running. To start the server, run `kent serve`.

The server owns all long-running work: sessions, projects, runtime orchestration, background shells, tool execution, tasks, workflows, and storage.

While annoying at times, this:
- Gives ability to fully isolate work on another machine, VM, or container. See [Sandboxing](../sandboxing/) for remote/container setup.
- Drastically reduces resource consumption
- Allows agents to work asynchronously during workflows.
- Allows spawning agents on schedule and periodically.
- Uses only about 25 MB of RAM while idle.

## Background Service

To use Kent on your local machine more easily, consider installing a system service that will run `kent serve` for you at login:

```bash
kent service status
kent service install
kent service restart
kent service stop
kent service start
kent service uninstall
```

All service commands accept `--persistence-root` and honor `KENT_PERSISTENCE_ROOT`. The root you install with is remembered, so pass the same root on `status`/`start`/`stop`/`restart`/`uninstall` to target that instance.

### Service recovery
On Linux, status `2` suppresses automatic crash recovery for the active service-manager activation, while other exits continue restoration; macOS retains restoration after every server exit.
On Windows, an observed numeric status `2` stops the service cleanly without recovery, and every other observed numeric status continues restoration; an unexpected service-host failure retains recovery.
If Windows cannot confirm termination, the service retains ownership and neither reports `Stopped` nor launches a replacement.
If Windows confirms termination without a numeric status, the service releases the server, launches no replacement, and stops cleanly.
A human start or restart, or `kent service install` without `--no-start`, begins a new activation; `--no-start` installs without starting. A later independent operating-system, login, or service-manager activation may start the installed service again.

## Backends

| OS | Service |
| --- | --- |
| macOS | LaunchAgent |
| Linux / WSL2 | `systemd --user` |
| Windows | Windows Service |


- On windows, `uninstall --keep-running` is not supported; the server is bound to the service and stops with it.
- Linux headless machines may need lingering enabled so the server survives logout `loginctl enable-linger "$USER"`.

## Port Conflicts

Service install/start commands refuse to change the service when Kent's configured server endpoint is already owned by a manual `kent serve` process or by a non-Kent listener.
If you started `kent serve` manually, stop that process before installing or starting the background service.

Running another server on a different configured port is fine. Kent only checks the endpoint resolved from `server_host` and `server_port`.
