# Release And Distribution Spec

## Release Source Of Truth

- Root `VERSION` is the source of truth for release version and tag normalization.
- Release/build scripts sync Tauri and package metadata from `VERSION`.
- Official release binaries are built through `scripts/build.sh`.
- The release profile is `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, and `-ldflags "-s -w -X core/shared/config.Version=..."`.
- Release archive packaging and verification live in `scripts/release-artifacts.sh`; workflow YAML should stay orchestration-focused.

## Targets

- Supported release targets are `darwin/arm64`, `linux/amd64`, `linux/arm64`, `windows/amd64`, and `windows/arm64`.
- macOS Intel is unsupported and must not be reintroduced.
- Linux release binaries stay statically linked; do not enable PIE or other dynamic-linking release modes.
- Workflow runner labels use `*-latest` aliases where GitHub provides them. ARM smoke-test jobs stay on `ubuntu-24.04-arm` and `windows-11-arm` while GitHub does not publish `-latest` aliases for those hosted runners.

## GitHub Releases

- GitHub releases publish `checksums.txt`.
- `scripts/install.sh` verifies archive checksums when the manifest is present.
- The release workflow verifies the checksum manifest and smoke-tests packaged binaries on Linux, macOS, and Windows before publishing.
- The release workflow smoke-tests `scripts/install.ps1` against staged Windows release assets before publishing.
- GitHub artifact attestations are intentionally not part of the release pipeline.

## Installers

- Windows one-command installs are served by `scripts/install.ps1`.
- The default Windows user install path is `~/.kent/bin/kent.exe`, matching the user-scoped Kent persistence root.
- Windows installer uninstalls remove only installer-owned binary, PATH, registry, and marker files.
- Windows uninstalls never remove Kent config, sessions, auth, worktrees, skills, or winget-installed dependencies.

## Homebrew

- Homebrew tap automation is part of a release, not an optional follow-up.
- The release workflow updates `respawn-llc/homebrew-tap` through `scripts/update-brew-tap.sh` for formula `kent`.
- The tap PR must run `brew test-bot`; on success, `brew pr-pull` publishes bottle metadata to tap `master`.
- If app release publication succeeds but tap update fails, fix release plumbing first if needed, then create a tap-only change for the same published version. Do not cut a second app release.

## Desktop Bundle Artifacts

- The desktop app ships arm64 macOS + x86_64 Linux only. Per-release assets, built
  by `scripts/desktop-release.sh build` and published by the `release.yml`
  `build_desktop` → `publish_desktop` jobs:
  - `Kent_<ver>_aarch64.dmg` (macOS installer),
  - `Kent_<ver>_aarch64.app.tar.gz` (+`.sig`) — macOS updater artifact (Tauri emits
    it as `Kent.app.tar.gz`; the build step renames it to this versioned asset),
  - `Kent_<ver>_amd64.AppImage` (+`.sig`) — Linux updater artifact,
  - `Kent_<ver>_amd64.deb` (Linux, apt/manual updates).
- `latest.json` is the Tauri updater manifest (`scripts/desktop-release.sh assemble`
  builds it from the `.sig` files), with `darwin-aarch64` and `linux-x86_64` entries
  pointing at the `.app.tar.gz` / `.AppImage` updater artifacts. It is the
  `plugins.updater` endpoint target.
- `desktop-checksums.txt` carries sha256s for the distributable bundles.
- macOS bundles are Developer ID signed in CI (`APPLE_CERTIFICATE`); notarization is
  off for v1 (Apple-side blocked), so v1 ships signed + un-notarized. The macOS
  build runner is pinned `macos-26` for the liquid-glass icon toolchain. Minimum
  deployment target is macOS 15 (Sequoia); Liquid Glass UI falls back to
  `NSVisualEffectView` on pre-26 macOS.

## Desktop App Updates

The desktop GUI is a thin remote-control client over a separately-installed Kent
server (loopback RPC); the server stays authoritative and is **not** bundled into
the app. This shapes how the desktop updates, because the server cannot
self-update and must never drift out of version-lockstep with the client.

### Install-source-aware update channel

The update channel is a property of how the app was installed, not a user setting.
Each install has exactly **one** update channel; the channels are mutually
exclusive so brew and the in-app updater never fight over the same bundle.

- **Direct download** (`.dmg` / AppImage from the GH release): the Tauri
  self-updater is the channel. The app checks on startup and surfaces the update
  chip in the chrome.
- **Linux `.deb` / plain binary**: the system package manager (apt/manual) is the
  channel. The Tauri Linux updater only services AppImage bundles, so the in-app
  self-updater is gated **off** for these installs. The desktop detects this at
  runtime via the `APPIMAGE` env var (`self_update_supported` Tauri command): on
  Linux, self-update is enabled only when running from an AppImage.
- **Homebrew** (`kent-desktop` cask alongside the `kent` formula): **brew** is the
  channel. `brew upgrade` moves the `kent` server formula and the `kent-desktop`
  cask together, keeping client and server in lockstep. The in-app self-updater is
  **disabled** on brew installs.

### Gate mechanism

- A desktop-local settings store (`settings.json`, plugin-store-backed, in the
  Tauri app data dir — macOS `~/Library/Application Support/sh.kent/`, Linux
  `~/.local/share/sh.kent/`) holds a typed `selfUpdate: "enabled" | "disabled"`.
- Scope is device/install-local client preferences only (update behavior, future
  native-UI prefs). It never holds server-authoritative state and never syncs.
- The `kent-desktop` cask `postflight` writes `selfUpdate: "disabled"` into that
  file (outside the `.app` bundle, so no code-signature break). The desktop reads
  it at startup; `disabled` drives the existing `capabilities.updater` gate false,
  which stands the self-update check and chip down. One `.dmg` artifact serves both
  channels — no second build, no compiled-in flag.

### Cask must NOT set `auto_updates true`

The `kent-desktop` cask must **not** declare `auto_updates true`. That stanza tells
`brew upgrade` to skip the cask and let the app self-update — which would desync the
desktop from the brew-managed `kent` server and reintroduce client/server skew. brew
is the authoritative channel for brew installs and must upgrade desktop + server
together. (This reverses an earlier draft that called for `auto_updates true`.)

`scripts/update-brew-tap.sh` generates the cask when passed `--desktop-url <dmg>`
(it downloads the published `.dmg` to compute the `sha256`); without that flag it
updates only the `kent` formula. The generated cask declares
`depends_on formula: "kent"` (server present + lockstepped), `depends_on arch: :arm64`,
`depends_on macos: :sequoia` (macOS 15 minimum — the liquid-glass UI degrades to
`NSVisualEffectView` on pre-26 macOS; mirrored by `minimumSystemVersion` in
`tauri.conf.json`), `app "Kent.app"`, and the `postflight` gate. It carries **no**
`auto_updates`, and is validated by `brew style`.

### Server-version handshake

The desktop verifies server/client version compatibility on the loopback
connection and degrades gracefully on mismatch (a clear "update your Kent server"
state), never a hang or cryptic error page. This is a no-regrets boundary check for
a remote-control client regardless of update strategy.
