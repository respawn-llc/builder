# Release And Distribution Spec

## Release Identity

- Every artifact in one release uses the same Kent version.
- Release tags use the normalized form `v<version>`.

## Metadata Upgrades

- Kent directly upgrades an existing Metadata database created by Kent v2.0.0 or newer.
- Kent rejects an older existing Metadata database before changing its schema or data.
- A missing Metadata database remains a supported fresh installation.
- Kent provides no fallback migration path or downgrade path.
- A supported Metadata upgrade atomically normalizes an empty or whitespace-only Session parent-provenance value to absence.
- A supported Metadata upgrade converts a canceled Workflow Task to its Workflow's `done` terminal Node when present. Without `done`, Kent preserves the Task's unique valid active terminal Node or otherwise chooses one terminal Node deterministically.
- When a canceled Task's Workflow has no terminal Node, a supported Metadata upgrade removes the Task, makes its Sessions workflow-neutral, and preserves its worktrees and other external artifacts.
- When serial Workflow state contains a pending Approval and a conflicting current position, a supported Metadata upgrade keeps the Approval source as the Task's sole Current Node.
- When serial Workflow state has no pending Approval and has several active Start or Terminal Nodes, a supported Metadata upgrade keeps the position with the latest update time, then latest creation time, then greatest identifier.

## Targets

- Supported release targets are `darwin/arm64`, `linux/amd64`, `linux/arm64`, `windows/amd64`, and `windows/arm64`.
- Kent does not publish a macOS Intel release.
- Linux CLI binaries are statically linked.

## GitHub Releases

- GitHub releases publish `checksums.txt`.
- The installer verifies an archive against `checksums.txt` when the file is available.
- Kent does not publish GitHub artifact attestations.

## TUI Update Discovery

- The Kent server determines TUI update status. Update status is independent of Session activity and transcript ordering.
- Kent checks GitHub Releases when a client requests update status.
- Kent refreshes a completed update result at most once per hour. Concurrent requests share one bounded check, and Kent caches every completed outcome for the one-hour freshness period.
- A newer valid release produces structured update information for the client to present.
- HTTP errors, invalid release information, malformed release versions, and unexpected update state appear as update-check failures with their cause.
- Network failures and timeouts report no available update and show no user-facing error.
- Kent releases advance the client/server protocol. The TUI does not reconcile application-version skew within one protocol version: its picker title remains the local client version while update discovery evaluates the attached server version.

## Installers

- Windows one-command installs are served by `scripts/install.ps1`.
- The default Windows user install path is `~/.kent/bin/kent.exe`, matching the user-scoped Kent persistence root.
- Windows installer uninstalls remove only installer-owned binary, PATH, registry, and marker files.
- Windows uninstalls never remove Kent config, sessions, auth, worktrees, skills, or winget-installed dependencies.

## Homebrew

- A Kent release includes the Homebrew `kent` formula.
- A macOS desktop release also includes the `kent-desktop` cask.
- Homebrew upgrades the server and desktop together for Homebrew installations.
- A delayed Homebrew publication uses the same Kent version. It does not require a second application release.

## Desktop Bundle Artifacts

- The desktop app ships arm64 macOS, x86_64 Linux, and x86_64 Windows bundles:
  - `Kent_<ver>_aarch64.dmg` (macOS installer),
  - `Kent_<ver>_aarch64.app.tar.gz` and `.sig` — macOS updater,
  - `Kent_<ver>_amd64.AppImage` (+`.sig`) — Linux updater artifact,
  - `Kent_<ver>_amd64.deb` (Linux, apt/manual updates),
  - `Kent_<ver>_x64-setup.exe` and `.sig` — Windows installer and updater.
- `latest.json` lists the updater artifacts for `darwin-aarch64`, `linux-x86_64`, and `windows-x86_64`.
- `desktop-checksums.txt` contains SHA-256 checksums for distributable desktop bundles.
- macOS bundles are Developer ID signed and are not notarized.
- The minimum macOS version is macOS 15 Sequoia.
- Liquid Glass falls back to the standard translucent material on macOS versions before 26.

## Desktop App Updates

The desktop app controls a separately installed Kent server. The server is not bundled with the app and does not update itself. The client and server must remain version-compatible.

### Install-source-aware update channel

The update channel is a property of how the app was installed, not a user setting.
Each install has exactly **one** update channel; the channels are mutually
exclusive so brew and the in-app updater never fight over the same bundle.

- **Direct download** (`.dmg`, AppImage, or Windows installer): the in-app updater is the channel. The app checks on startup and shows an update indicator. Windows desktop is available only through this channel.
- **Linux `.deb` or plain binary**: the system package manager or manual installation is the channel. The in-app updater is available only for AppImage installations.
- **Homebrew on macOS**: Homebrew is the channel. `brew upgrade` updates the `kent` formula and `kent-desktop` cask together. The in-app updater is disabled.

### Update-channel lock

- Each desktop installation has one device-local `selfUpdate` setting whose value is `enabled` or `disabled`.
- This setting controls only client update behavior. It does not contain server state and does not sync.
- Homebrew installation writes `selfUpdate: "disabled"`, and the desktop reads that value at startup to disable in-app updates.
- Direct-download installation enables in-app updates when the package format supports them.
- The same macOS application bundle can serve direct-download and Homebrew channels.

### Homebrew authority

- The `kent-desktop` cask must not tell Homebrew to skip desktop upgrades in favor of app-managed updates.
- The cask requires the `kent` formula, Apple silicon, and macOS 15 or later.
- The cask installs `Kent.app` and disables the in-app updater.

### Server-version handshake

The desktop verifies client/server compatibility when it connects. A mismatch shows a clear `update your Kent server` state. It never hangs or shows a cryptic error page.
