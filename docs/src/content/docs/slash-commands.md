---
title: Slash Commands
description: Available slash commands, how their input is parsed, and how file-backed custom commands are discovered.
---

Press Tab to autocomplete a command, and Enter to autocomplete and send. Press Tab again when command matches fully to **queue** the command. This allows chains like `"commit" -> [Tab] -> "/compact" -> [Tab] -> "/prompts:open_pr" -> [Tab]`.

| Command                                                                                 | Input                        | What it does                                                                                                                                                       |
| --------------------------------------------------------------------------------------- | ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `/exit`                                                                                 | none                         | Exit Kent. During active work, Kent detaches while the server continues the run.                                                                                   |
| `/new`                                                                                  | none                         | Start a new session without stopping active work in the current session.                                                                                           |
| `/resume`                                                                               | none                         | Open the session picker without stopping active work in the current session.                                                                                       |
| `/login`                                                                                | none                         | Open auth options.                                                                                                                                                 |
| `/compact <instructions>`                                                               | optional free-form text      | Compact the current context. Trailing text is passed through as **additional** compaction instructions. Active non-workflow goals continue in the resumed context. |
| `/name <title>`                                                                         | optional free-form text      | Set the session title. Empty input resets.                                                                                                                         |
| <code>/thinking &lt;low&#124;medium&#124;high&#124;xhigh&#124;max&#124;ultra&gt;</code> | optional single value        | Set the thinking level. Empty input shows the current level.                                                                                                       |
| <code>/fast [on&#124;off&#124;status]</code>                                            | optional single value        | Toggle or inspect Fast mode;                                                                                                                                       |
| <code>/supervisor [on&#124;off]</code>                                                  | optional single value        | Toggle supervisor invocation.                                                                                                                                      |
| <code>/autocompaction [on&#124;off]</code>                                              | optional single value        | Toggle auto-compaction.                                                                                                                                            |
| `/status`                                                                               | none                         | Open a page with detailed information about the config, git, runtime, and model.                                                                                   |
| <code>/goal [pause&#124;resume&#124;clear&#124;&lt;objective&gt;]</code>                | optional action or objective | Set or manage the current session goal (ralph-loop). Empty input opens the goal page.                                                                              |
| <code>/ps [kill&#124;inline&#124;logs] &lt;id&gt;</code>                                | optional action + id         | Open the background-process picker, or manage a specific background shell.                                                                                         |
| <code>/wt</code>                                                                        | none                         | Open the Worktrees page.                                                                                                                                           |
| <code>/wt create</code>                                                                 | none                         | Open the create-worktree dialog; new branches require a non-empty base ref.                                                                                        |
| <code>/wt switch &lt;target&gt;</code>                                                  | required selector            | Schedule entry into a worktree by id, branch, display name, or path.                                                                                               |
| <code>/wt leave</code>                                                                  | none                         | Schedule a return to the main workspace.                                                                                                                           |
| <code>/wt delete [&lt;target&gt;]</code>                                                | optional selector            | Delete a worktree.                                                                                                                                                 |
| `/copy`                                                                                 | none                         | Copy the latest durable model final answer to the system clipboard.                                                                                                |
| `/back`                                                                                 | none                         | Return to the parent session, if present, with the child’s latest durable final answer prefilled.                                                                  |
| `/review <what to review>`                                                              | optional free-form text      | Trigger Kent's native code review. It reuses an empty session; otherwise it starts a fresh child session.                                                          |
| `/init <instructions>`                                                                  | optional free-form text      | Run repository initialization. It reuses an empty session; otherwise it starts a fresh child session.                                                              |
| `/prompt:<name>`                                                                        | optional trailing arguments  | Run a server-owned custom prompt command.                                                                                                                          |

Goal changes are saved immediately, including during model work. Confirmation means the goal is saved; the model receives its reminder at the next step boundary.

Kent discovers Markdown prompt commands on the server that owns the attached Project Workspace. Remote clients do not read server paths or receive prompt bodies in the command catalog.

The effective roots, in descending precedence, are:

- `<workspace>/.kent/prompts`
- `<workspace>/.kent/commands`
- `<persistence-root>/prompts`
- `<persistence-root>/commands`
- `<persistence-root>/.generated/prompts`
- `<persistence-root>/.generated/commands`

Discovery is non-recursive and includes non-blank `.md` files. The first valid file for each normalized basename wins. IDs lowercase letters, preserve digits, convert whitespace and underscores to one underscore, and discard other characters.

The picker shows a one-line preview made from the first 256 Unicode characters after collapsing whitespace. Markdown punctuation remains unchanged. Prompt bodies are resolved by the server when the command is invoked.

If the exact `$ARGUMENTS` token appears in the body, Kent replaces every occurrence with trimmed trailing arguments. Otherwise, Kent appends non-empty trailing arguments after one blank line.

First-time setup can import slash-command directories from supported providers. An unavailable or unknown `/prompt:` command reports an error and is never sent to the model as plain text. Other unknown slash commands retain their normal behavior.

During an active turn, an available `/prompt:<name>` command steers its server-expanded prompt into the current session. `/review` and `/init` retain their fresh-session behavior.
