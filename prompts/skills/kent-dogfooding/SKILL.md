---
name: kent-dogfooding
description: How to use `kent` cli to change your behavior, environment, config, or debug issues. Read when the user asks to manage worktrees, change Kent config.toml/settings/behavior/hooks/subagents, or debug project/workspace/worktree/workflow errors.
---

Kent is the harness you are running inside, but it's also a server that runs agentic loops, a TUI, and a CLI.

Source-of-truth for commands and public docs:

- Run `kent --help` and `kent <command> --help` for exact current CLI flags.
- Full docs index: `https://kent.sh/llms.txt`.

You can directly `curl -S` each of the docs pages with an `.md` postfix to get its content. Avoid using web fetch tools on those.

## Projects And Workspace Bindings
Kent tracks projects and workspace roots so sessions can move across checkouts and remote/local server boundaries. If your subagent commands fail with errors about workspace binding or projects, simply attach a workspace folder where you want to run the subagent to the project where you are running:

```bash
$ kent attach <path/to/subagent/workspace>
```

More info in `--help`.

## Config Locations
Global config (applies to all projects) `~/.kent/config.toml` (`%USERPROFILE%\.kent\` on Windows), local config is at `<workspace-root>/.kent/config.toml`. Workspace root is usually your cwd, or your worktree's main workspace cwd. Config schema and full notes at `https://kent.sh/config.md`. The database and session logs that kent uses are colocated with the config file. Session logs are `.json` files with a full history of events, split per-project. Careful: session logs are very long and can weigh gigabytes.

Most behavior changes you make affect only **new sessions** and only **after server restart**. Existing sessions will keep captured conversation logs and settings. After changing config, ask the user to restart the service with `kent service restart`, restart the Kent GUI, and then start a new session, for changes to apply.

Important: do not make changes to your configuration that were not authorized or directly asked for by the user. If your environment is buggy/broken, ask the user for help instead of messing with your internals.

## Change Agent Behavior
Use prompt files for broad behavior changes, skills for reusable on-demand workflows, and subagent roles for specialized headless agents. Start by reading docs at `https://kent.sh/prompts.md`

Note that you shouldn't be rewriting main agent's system prompt: the output can be biased and low-quality. System prompts need to be crafted carefully and vary strongly per LLM model family and use-case. Either the user should supply an existing prompt they want to use, or use `{{.DefaultSystemPrompt}}` for sane defaults, and add additional instructions to it.

## Subagent roles
User may ask you to define new "subagents" or "agent roles". Subagents are `kent run` commands you call. You can also use them for scripting of user's kent-based workflows. More info at `kent run --help` and `https://kent.sh/headless.md`.

## Worktrees
Use Kent's server-backed commands instead of mutating worktrees with `git worktree`:

```bash
kent worktree status
kent worktree list
kent worktree create <branch-or-ref> [path]
kent worktree enter <selector>
kent worktree leave
kent worktree delete <selector>
```

Inside an agent shell, these commands target `KENT_SESSION_ID`. `create` prepares and records the checkout but does not enter it; use the printed `kent worktree enter ...` command separately. Agent-shell deletion retains branches and rejects `--delete-branch`. Use `--force` only when the user explicitly authorizes removing a dirty or indeterminate worktree folder.

Worktree setup scripts prepare new checkouts with local files, credentials, symlinks, or dependencies. Read the setup contract at `https://kent.sh/worktrees.md`.

## Shell Postprocess Hooks
Kent can post-process shell command output before you see it.
Hook shape, output, and config are at `https://kent.sh/command-postprocessing.md`

You can disable this feature with `raw=true` in your `exec_command` tool. This hook is intended to optimize, shrink, or log the commands that you run. For example, a user may want you to use a tool that makes outputs smaller. Kent also ships embedded optimizers (`builtin` mode toggle) out of the box.

## Goals 

The user can set you a goal, or you may set a goal for yourself at will by running `kent goal set "<objective>"`. This goal will nudge you and all future agents across handoffs to work on a shared objective until completion. You should proactively set goals for yourself for larger tasks (this is encouraged) that might take multiple handoffs to complete. Goal text is a .md-formatted clear and exhaustive description of what needs to be done. Provide paths to relevant context: plan/doc files,  checklists, etc., clear Definition of Done, and measurable explicit completion criteria, in the goal text. Assume the agents that will read your goal text will know nothing about this conversation or session. Avoid assigning subtasks, phases of a larger plan, or implementation slices, as goals - instead, assign the overall task as a goal and keep a file-based worklog or checklist. If you are blocked and unable to complete your goal, ask the User a question to summon them to help you.
