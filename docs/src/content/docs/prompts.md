---
title: Prompts
description: Prompt customization files, precedence, placeholders, and session snapshot behavior.
---

Learn how to customize system prompts, supervisor instructions, subagent system prompts, workflow prompts, and repo guidance.

Models will follow the instructions by role: `system -> developer -> user`, from most authoritative to least authoritative. With kent, you can customize all three levels:

## Instruction Files

- `~/.kent/AGENTS.md` is a global instructions file injected into every session automatically.
- `<workspace>/AGENTS.md` adds developer instructions that are specific to the current project.

These files are `developer`-level instructions.

## System Prompt

System prompt files replace Kent's built-in default "product engineer" / SWE-focused system prompt. Priority, lowest to highest:

- Built-in system prompt
- `~/.kent/SYSTEM.md`
- `~/.kent/config.toml` `system_prompt_file`
- `<workspace-root>/.kent/SYSTEM.md`
- `<workspace-root>/.kent/config.toml` `system_prompt_file`
- Selected `[subagents.<role>]` `system_prompt_file`

`system_prompt_file` paths are resolved relative to the containing `config.toml` directory unless absolute.

Kent snapshots the rendered system prompt on each compaction to prevent cache misses. Edits to system prompt files take effect after a successful compaction and the next model request.

## Goal Continuation

After successful automatic, manual, or handoff compaction, a non-workflow session with an active goal resumes with the exact goal text and Kent's goal work and completion guidance. Paused, completed, cleared, and absent goals add no continuation guidance, and reopening a session without compaction does not add it.

## Placeholders

You can assemble your own system prompt from building blocks provided by Kent. It's highly recommended to leave the instructions about the harness (`HarnessWorkflowAutonomy`) intact.

System prompt files use Go template syntax with these fields:

- `{{.DefaultSystemPromptHarnessWorkflowAutonomy}}` - important guidelines on harness behavior, environment constraints, available tools.
- `{{.DefaultSystemPromptPersonality}}` - Kent agent identity, communication style, and engineering posture.
- `{{.DefaultSystemPromptAmbiguityAndOutputQuality}}` - opinionated product ambiguity handling and implementation quality rules.
- `{{.DefaultSystemPromptFinalAnswerAndFormatting}}` - final response, Markdown, and formatting rules suitable for TUI.
- `{{.DefaultSystemPrompt}}` - full text of the built-in Kent system prompt.
- `{{.LaunchCommand}}` - Kent executable command, e.g. `path/to/kent.exe`.
- `{{.EstimatedToolCallsForContext}}` - estimated function/tool-call budget before compaction/handoff, exact number that varies with model context window, like `185`.
- `{{.EditingToolName}}` - name of the tool the agent uses to modify files, like `edit` or `patch`. Varies per model.

Example:

```md
{{.DefaultSystemPromptPersonality}}

{{.DefaultSystemPromptHarnessWorkflowAutonomy}}

# Team Rules

Prefer small, reviewable commits.
```

Additionally, if `tool_preambles = true` in the [config](../config/), another block of text is appended instructing the model to talk to you while working.

## Supervisor System Prompt

`reviewer.system_prompt_file` replaces Kent's built-in supervisor system prompt:

- `~/.kent/config.toml`
- `<workspace-root>/.kent/config.toml`

The workspace config value takes priority. Kent snapshots the rendered supervisor prompt independently when a supervisor request is built; edits take effect for supervisor requests after successful compaction.
