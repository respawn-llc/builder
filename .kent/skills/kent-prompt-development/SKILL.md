---
name: kent-prompt-development
description: Develop Kent’s model-facing instructions. Use when editing Kent system or developer prompts, tool definitions and outputs, compaction or workflow reminders, or prompt assembly.
---

Common prompt-writing guidance lives in the repository source [prompting](../../../prompts/skills/prompting/SKILL.md). Read its Writing prompts, Language, and Structure sections when authoring prompt text.

## Goal
Prompt text is a runtime interface for future agents. Write it like product code: precise contracts, low ambiguity, minimal token cost, and behavior that survives weak context, long sessions, handoffs, and different model families.

## Push vs Pull
- **Push** is the information the agent **needs** to know beforehand to make use of. You **push** into context minimal contextual info to enable discovery. You push the information to avoid "I don't know what I don't know" issues. Your memory is constrained, so you must only push the minimum info needed, and if the agent needs more, it **pulls**. Upside: already in context and likely to be taken advantage of by the agent. Downside: pollutes memory and increases costs. Prefer pushing information as contextually as possible.
- **Pull**: You provide this info for the agent to discover only when it decides it needs more. For example, in this skill the `description` frontmatter was **pushed** to you so you learned about it, but you just **pulled** the SKILL.md file because you thought you needed more. Upside: 0 impact on context unless directly relevant to the agent. Downside: places thinking and decisionmaking burden on the agent. Too many decisions to make confuse and distract LLMs from their task. Only rely on pulling for things that you can't push contextually.

Neither push nor pull are ideal. The ideal version is **contextual push** - provide information right before you know that it will be directly needed by the agent. In practice this requires code or system chanegs to achieve. For every piece of information you provide to the agent, attempt to contextually push it first, then try to make it pullable w/ a pushed breadcrumb, and only resort to pushing unconditionally when you know it's needed literally always (like role or communication style) or required to get best results (like global rules).

## Core Principles
- Design prompts as control surfaces, not essays. Every sentence should change model behavior, preserve a constraint, or remove ambiguity.
- Prefer deterministic runtime contracts and typed data over prompt complexity. If the product can enforce something in code or schema, do that and keep the prompt short.
- Keep the model unburdened. Remove repeated guidance, generic engineering advice, motivational framing, and explanations the model does not need to act.

## Structure
- Keep mode-specific rules isolated. A workflow completion prompt, headless-mode prompt, compaction prompt, and general system prompt should not carry each other's unrelated constraints.
- For long prompt surfaces, separate stable global behavior from injected situational reminders. Reminders should describe only the changed state and the resulting action constraints.

## Tool Descriptions And Schemas
- Start a tool description with what the tool does in one sentence.
- Add when/how to use it only when the model must choose between tools or avoid misuse.
- Include restrictions in the description when they affect whether the model may call the tool: "only in workflow tool-completion mode", "allowed only after a specific developer message", "private to you".
- Keep parameter sets minimal. Prefer fewer optional parameters and even fewer required parameters.
- Each parameter description should answer the practical questions that affect correct calls: required/optional status, default behavior, accepted format, unit, path resolution, and side effects.
- Name formats explicitly: ISO date, milliseconds, 1-based index, relative-to-workspace path, allowlist domain, etc.
- Explain omission semantics for optional fields when omission differs from zero/empty values.
- Keep schema descriptions model-facing. Do not document UI copy, implementation details, internal behavior, validation semantics (unless non-obvious).
- Avoid schema text that repeats the field name without adding behavior.
- Use tool result and transcript presentation text to reduce cognitive load: compact summaries for common success, detailed output only when needed, and stable labels for pending/background work.
- Keep tool output minimal to save costs and memory space. For create/update actions, only output confirmation like "ok" or exit code 0. For read actions, include in plaintext the minimal sufficient amount of information. Avoid formatting-heavy formats like JSON as direct model-consumed outputs where automation is not expected.
- Treat tool outputs as real propmts that can trigger behavior. Any outputs like "Warn: LSP reported 7 lint issues" may trigger the model to take action on them.

## Errors And Recovery Text
- Model-facing errors should say what failed and what action can fix it.
- Preserve precise identifiers the model needs: path, session ID, field name, limit, allowed mode, transition ID, or rejected parameter.
- Translate raw machine failures at model boundaries. Prefer "tool timed out; retry with a larger timeout or a smaller command" over raw status codes when the action is clear.
- Do not hide uncertainty. If the system cannot know the fix, state the boundary and the safe next step.
- Use retry instructions only when retry is likely useful. Otherwise tell the model to change input, ask the user, or stop using the disallowed action.
- Keep user-denial and permission errors explicit.
- Avoid blame. The error is a contract response, not a scolding.

## Reminders And Meta-Commentary
- Inject reminders only for state the model cannot infer reliably from its local context.
- State concrete facts with exact values when environment changed: cwd, workspace root, worktree path, branch, mode, or tool availability.
- Say which prior assumption no longer applies when switching modes or workspaces.
- A reminder must not reduce task scope or quality. If it changes behavior, state the new constraint and the allowed continuation path.
- Background/headless prompts should explicitly define interaction expectations, allowed final response, and what to do with blockers.

## When to choose System Prompt, User Prompt, Tool Description, or Developer Reminder
- System prompt is snapshotted at session start and stays constant. It must not include any information that can become stale or change mid-session, like dates, git status, contextual information, environment information, etc. System prompt should ideally only contain information that **all** agents will **always** need. System prompt has the highest authority over every other prompt or instruction. Include in system prompt only 100% authoritative global instructions and guidance that should always be pushed, or things that can't be contextual but unavoidably need pushing. Confirm with the user ANY system prompt edits because it immensely affects agent behavior. 9/10 authority of instructions in system prompt.
- Developer messages are designed specifically for contextual pushing. The instructions in developer messages are 7/10 authoritative. Developer reminders work for both orders and info but should not give direct overrides of immediate tasks, as that will confuse the agent as to whose task to implement - user's or developer's.
- User messages are one-shot directions about their desires, not usually strict instructions. 3/10 authority, especially in cases of conflict. But with no other conflicts, they determine what the agent ultimately does. It's very rare that user messages are not given by the actual user, so only use them to carry over / move / preserve the user prompt, not to impersonate the user, unless that's the goal. In automated workflows, it makes sense to provide user messages only if you want the agent to genuinely think that a human said something, such as giving it a task in workflow mode, or in automation (subagents).
- Tool descriptions. These are unconditionally pushed into context. They hold ~5/10 authority and usually should only contain guidance or teach something, not strictly dictate rules. Tools can be disabled and enabled, in which case they are hidden from the agent, and any mention of a tool that is disabled will **greatly confuse** the agent, because a tool is referenced that it doesn't see. Because of that, as much tool-related prompting as possible should be contained in tool metadata (schema, etc.) and other prompts should try to avoid referencing tools directly unless unavoidable. Yes, regarding tools, sometimes direct orders need to be given, e.g. in system prompt to maintain contextuality (example: the `trigger_handoff`) or gain authority (example: edit/patch tools), you can conditionally give them with "If <tool> is enabled,", or "## <tool> guidance", elsewhere, but avoid wording that will be confusing if tool is hidden/not present. But tools are snapshotted at start of the session, so any info that goes stale goes stale forever. Because of these severe limitations tools must be minimized in count + in orders inside, and handled carefully. Treat prompt schemas as guidance on how to use something always needed or always available.

## Editing Checklist
- Does each sentence change behavior or clarify a contract?
- Is the exact role, mode, tool, output shape, or completion condition clear?
- Are hard rules separated from heuristics?
- Did you remove generic quality adjectives that are not operationalized?
- Did you remove duplicated, temporal, motivational, or explanatory text?
- Are schemas minimal and explicit about defaults, formats, and omission behavior?
- Are errors actionable without masking important raw detail?
- Can the prompt be read months later without knowing the conversation that produced it?
