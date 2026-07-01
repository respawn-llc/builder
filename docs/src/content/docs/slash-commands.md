---
title: Slash Commands
description: Available slash commands, how their input is parsed, and how file-backed custom commands are discovered.
---

Press Tab to autocomplete a command, and Enter to autocomplete and send. Press Tab again when command matches clearly to **queue** the command. This allows chains like `"commit" -> [Tab] -> "/compact" -> [Tab] -> "/prompts:open_pr"`.


| Command | Input | What it does |
| --- | --- | --- |
| `/exit` | none | Exit Kent, same as Ctrl/Cmd+C. |
| `/new` | none | Start a new session. |
| `/resume` | none | Return to the startup session picker. |
| `/login` | none | Open auth options. |
| `/compact <instructions>` | optional free-form text | Compact the current context. Trailing text is passed through as **additional** compaction instructions. |
| `/name <title>` | optional free-form text | Set the session title. Empty input resets. |
| <code>/thinking &lt;low&#124;medium&#124;high&#124;xhigh&gt;</code> | optional single value | Set the thinking level. Empty input shows the current level. |
| <code>/fast [on&#124;off&#124;status]</code> | optional single value | Toggle or inspect Fast mode; |
| <code>/supervisor [on&#124;off]</code> | optional single value | Toggle supervisor invocation. |
| <code>/autocompaction [on&#124;off]</code> | optional single value | Toggle auto-compaction. |
| `/status` | none | Open a page with detailed information about the config, git, runtime, and model. |
| <code>/goal [pause&#124;resume&#124;clear&#124;&lt;objective&gt;]</code> | optional action or objective | Set or manage the current session goal (ralph-loop). Empty input opens the goal page. |
| <code>/ps [kill&#124;inline&#124;logs] &lt;id&gt;</code> | optional action + id | Open the background-process picker, or manage a specific background shell. |
| <code>/wt</code> | none | Open the Worktrees page. |
| <code>/wt create</code> | none | Open the create-worktree dialog; new branches require a non-empty base ref. |
| <code>/wt switch &lt;target&gt;</code> | required selector | Switch directly to a worktree by id/branch/path. |
| <code>/wt delete [&lt;target&gt;]</code> | optional selector | Delete a worktree. |
| `/copy` | none | Copy the latest model final answer to the system clipboard. |
| `/back` | none | Teleport back to the parent session, if present. |
| `/review <what to review>` | optional free-form text | Trigger Kent's native code review. Trailing text is appended to the prompt body. |
| `/init <instructions>` | optional free-form text | Start a new session that sets up the workspace on first-use. Trailing text is appended to the prompt body. |
| `/prompt:<name>` | optional free-form text | Run a custom Markdown prompt (see [prompts](../prompts/)). |


## Input Behavior

- `Enter` runs the selected command immediately, even when the name is only partially typed.
- `Tab` on a partial command autocompletes the selected command and inserts a trailing space so you can continue with arguments.
- `Tab` on an exact known command adds it into the queue. Use this to make chains of prompts and slash commands like /compact -> /review -> /prompts:commit.
- While the model is working on an active goal, `/goal` still opens the read-only goal page. `/goal pause` and `/goal clear` run immediately and append one persistent goal info line; setting or resuming a goal is rejected until the runtime is idle.
- If `ask_question` is disabled, Kent opens sessions with active goals for management, but goal set/resume fails until `ask_question` is enabled; pause and clear remain available.

### 2. Built-In and Custom Prompts

Kent supports markdown file-backed custom prompt commands.

- If the prompt body contains the exact token `$ARGUMENTS`, Kent replaces every occurrence with the trailing input.
- Otherwise, if trailing input was provided, Kent appends it to the end of the prompt body.

To add a custom prompt, create a Markdown file in one of these directories (descending priority):

- `<workspace>/.kent/prompts`
- `<workspace>/.kent/commands`
- `~/.kent/prompts`
- `~/.kent/commands`

The command id is derived from the filename as `prompt:<normalized_base_name>`. Duplicate command ids are deduplicated by first match, so repo-scoped commands override global commands.
