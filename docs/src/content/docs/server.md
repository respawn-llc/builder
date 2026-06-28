---
title: Kent Server
description: Kent's local client-server architecture and background service management.
---

Kent runs all its work through a local server process. Frontends are clients: TUI, desktop app, headless runs, and other local integrations all need the server to be running. The background server uses about 25 MB of RAM while idle.

The server owns long-running work: sessions, projects, runtime orchestration, background shells, tool execution, tasks, workflows, and storage.

While annoying at times, this:

- Gives ability to fully isolate work on another machine, VM, or container. See [Sandboxing](../sandboxing/) for remote/container setup.
- Drastically reduces resource consumption
- Allows agents to work asynchronously during workflows.
- Allows spawning agents on schedule and periodically.

## Background Service

`kent service` runs a local `kent serve` as a background service that starts at login.

## Commands

```bash
kent service status
kent service install
kent service restart
kent service stop
kent service start
kent service uninstall
```

All service commands accept `--persistence-root` and honor `KENT_PERSISTENCE_ROOT`. The root you install with is remembered, so pass the same root on `status`/`start`/`stop`/`restart`/`uninstall` to target that instance.

## Backends

| OS | Service |
| --- | --- |
| macOS | LaunchAgent |
| Linux / WSL2 | `systemd --user` |
| Windows | Windows Service |

### Windows

The background server runs as you, with your user environment, and starts when you log in.

- `install` and `uninstall` prompt for Administrator elevation (UAC). Other commands run without elevation.
- `stop`, a service restart, and system shutdown shut the server down gracefully.
- `uninstall --keep-running` is not supported; the server is bound to the service and stops with it.

Linux headless machines may need lingering enabled so the server survives logout:

```bash
loginctl enable-linger "$USER"
```

## Port Conflicts

Service install/start commands refuse to change the service when Kent's configured server endpoint is already owned by a manual `kent serve` process or by a non-Kent listener.
When the service is installed, `restart` reclaims the configured endpoint from a healthy Kent listener and attaches it back to the service.
If you started `kent serve` manually, stop that process before installing or starting the background service.

Running another server on a different configured port is fine. Kent only checks the endpoint resolved from `server_host` and `server_port`.
