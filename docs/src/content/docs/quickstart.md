---
title: Quickstart
description: Install Kent, authenticate on first launch, tune the most useful settings, and learn the main session workflows.
---

## 1. Install Kent Server and CLI

#### Homebrew (macOS/Linux)

```bash
brew tap respawn-llc/tap
brew install respawn-llc/tap/kent
```

#### Arch Linux (AUR)

The [`kent-bin`](https://aur.archlinux.org/packages/kent-bin) AUR package is community-maintained.

```bash
yay -S kent-bin
```

#### Standalone binaries via GitHub Releases

These versions are **not auto-updated**. Please keep them updated manually by re-running install scripts.

Linux:

```bash
curl -fsSL https://kent.sh/install.sh | sh
```

Windows:

```powershell
irm https://kent.sh/install.ps1 | iex
```

Check the installed version with: `kent --version`

## 2. Optional: Install the Background Service

Run this if you want one shared Kent server to start at login:

```bash
kent service install
```

It uses 20 MB of RAM when idle, lets unlimited frontends stay lightweight by connecting to **one** orchestrator, makes spawning and controlling subagents and background shells reliable, and enables the use of the desktop app. See [Kent Server](../server/) for details and service management commands.

## 3. Install Kent Desktop

The desktop app lets you use Kent's [Workflows and Tasks](../workflows/) feature to build agentic loops and deterministic pipelines to **fully automate** processes and scale to 10s or 100s of agents.

![Kent Desktop showing a project kanban board with tasks grouped by workflow stage](/desktop/desktop-kanban.webp)

### Manual Install

Download the installer for your platform at [kent.sh/desktop](https://kent.sh/desktop) (currently MacOS and Linux), or install via Homebrew:

```bash
brew install --cask respawn-llc/tap/kent-desktop
```

Homebrew installs update through `brew upgrade`, standalone installs self-update.

:::note
The desktop app, due to the asynchronous nature of workflows, needs a [server](../server/) to connect to.
:::

# First Use

:::danger[Security Warning]
Out of the box, Kent does not ship a sandbox, and does not enforce tool calling permissions. **Using Kent is equivalent to running `claude --dangerously-skip-permissions` or `codex --yolo`.** The model will have **full access** to your entire computer. By using Kent, you accept full responsibility for what the model does on your computer. If you want to safely run Kent in a real sandbox, see [Sandboxing](../sandboxing/).
:::

Start Kent CLI with: `kent`. The first run will ask you to pick auth option and walk you through onboarding.

Supported auth options:

- OpenAI/Codex subscription OAuth via the startup sign-in picker.
- OpenAI API-key auth via `OPENAI_API_KEY`. If you prefer API-key auth, export `OPENAI_API_KEY` before launch and kent will ask to use it.

:::note
Anthropic or Gemini subscriptions/models will not be supported until these companies allow third-party harnesses in their ToS.
:::

## Main Workflows

- Press `F1` to invoke the help menu.
- Use `Enter` to steer the model, `Tab` to queue messages. Slash commands also work!
- Use `Shift+Tab` to toggle between detailed transcript mode and lean ongoing mode.
- Type `$ <command>` to execute a shell command and show its output to the model.
- Press `Esc` twice to enter Edit mode, which lets you go back in time, edit a previous message, and fork the session starting with it. Use `Up`/`Down` to walk through user messages. File edits are **not** rolled back.
- Use the `Up`/`Down` arrow keys to select and resend previous prompts.
- Press `Ctrl+V`, `Ctrl+D`, `Alt+V`, or `Alt+D` to paste clipboard content: images become temporary file paths and text is inserted at the cursor. Terminal-native bracketed paste remains normal text input.
- Use `/review` to start a code review. In a non-empty session, Kent opens that review in a fresh child session. After the review finishes, you can use `/back` to teleport to the original session.
- `/name <new-name>` will set your session name in the picker and terminal title.
- `/autocompaction` will toggle compaction, and `/compact` will trigger one. If autocompact is off, you can go above 100% context usage if model allows it. **Going above 100% will cost more and degrade model performance**.
- Run `/status` to get detailed info about the session.

For the full command reference, see [Slash Commands](../slash-commands/).

## Configuration

Kent reads settings from `~/.kent/config.toml`. The full reference is on the [Configuration](../config/) page.

## Skills and Slash Commands

On first launch, the setup wizard can optionally symlink existing skills and slash-command directories from `~/.claude`, `~/.codex`, or `~/.agents` into Kent's `~/.kent` layout.

Kent discovers skills from:

- `<workspace>/.kent/skills`
- `~/.kent/skills`

Kent also seeds preinstalled skills into `~/.kent/.generated/skills`. Do not edit `~/.kent/.generated`; copy a generated skill into a workspace or global skill root to customize it.

You can disable skills for new sessions in `config.toml`:

```toml
[skills]
creating-skills = false
```
Changes take effect when you start a new session.

Kent discovers custom slash commands from Markdown files in:

- `<workspace>/.kent/prompts`
- `<workspace>/.kent/commands`
- `~/.kent/prompts`
- `~/.kent/commands`

More info on the [Slash commands](../slash-commands/) page.

## Supervisor

- Use `/supervisor` to toggle its invocation for the current session. Supervisor is a feature that will automatically review the edits made by the model. It increases costs by ~15% (if using the main model) but improves results. By default supervisor uses the same model as the main one. That may be too costly / too slow for you. [Configuration](../config/) page contains instructions on how to change supervisor model.

## Advanced

Once you're comfortable driving the traditional agentic CLI, consider upgrading to [workflows](../workflows/).
