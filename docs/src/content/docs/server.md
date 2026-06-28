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

`kent service` installs and manages a supervised background `kent serve` process.
The service starts at login and keeps the local server always available.

```bash
kent service install
```

## Commands

```bash
kent service status
kent service install
kent service restart
kent service stop
kent service start
kent service uninstall
```

All service commands accept `--persistence-root` (and honor `KENT_PERSISTENCE_ROOT`). The root you install with is baked into the generated registration (bound to `--persistence-root <root>`), so the supervised service uses the same config+data root rather than re-resolving `~/.kent` under whatever user the supervisor runs as. Use the same root on `status`/`start`/`stop`/`restart`/`uninstall` to target that instance.

## Backends

| OS | Supervisor |
| --- | --- |
| macOS | LaunchAgent |
| Linux / WSL2 | `systemd --user` |
| Windows | Windows Service (SCM), runs as the logged-in user |

### Windows

On Windows the background service is a real Service Control Manager service that starts at boot and restarts on failure. The service itself runs as `LocalSystem`, but it is only a supervisor: it launches `kent serve` **in your interactive session, as you**, using your logon token — so the server keeps your full identity (`%USERPROFILE%`, git config, SSH keys, Windows Credential Manager) with **no password stored anywhere**. The server runs with no console window, and starts as soon as you log in.

- `install` / `uninstall` need Administrator and prompt for elevation (UAC) automatically.
- `status` / `start` / `stop` / `restart` run without elevation — install grants your account start/stop rights on the service.
- `stop` (and a service restart or system shutdown) shuts the server down gracefully — the supervisor signals it to exit cleanly, exactly as `Ctrl+C` would, and only force-kills it if it does not exit within the stop window.
- Installing or uninstalling also removes any leftover registration from older Kent versions (the previous logon scheduled task and Startup-folder launcher), which is what caused stray console windows.


Linux headless machines may need lingering enabled so the server survives logout:

```bash
loginctl enable-linger "$USER"
```

## Port Conflicts

Service install/start commands refuse to change the service when Kent's configured server endpoint is already owned by a manual `kent serve` process or by a non-Kent listener.
If the service is installed but unloaded, `restart` can stop a healthy Kent listener on the configured endpoint and attach that endpoint back to the background service.
If you started `kent serve` manually, stop that process before installing or starting the background service.

Running another server on a different configured port is fine. Kent only checks the endpoint resolved from `server_host` and `server_port`.
