---
name: prompting
description: How to prompt other agents, including delegating to subagents via `kent run`, talking to and supervising other agents, and writing workflow transition prompts. Use when drafting agent instructions, delegating work, steering or continuing a run, observing runs, answering Session questions, or building workflow transition prompts.
---

## What you don't need to repeat

Every Kent run already contains most of the basic information that you received at the start of the conversation:
- Environment info
- CWD
- Project directory
- Full AGENTS.md rules
- Skills and subagents
- Tools and their guidance
- System and developer instructions at the top of this conversation

Avoid repeating any information from the above context in your prompts. `kent run` calls and workflow agents already know the CWD where you executed the command; you don't need to reiterate it. Because you and all agents share the same workspace, agents can also easily retrieve **workspace information**, such as:
- Git info
- Any files, documents, or code
- Access to the internet, etc.

By default, all `kent run` subagents share the worktree with you unless you provide a different CWD.

However, Kent agents do NOT receive:
- Handoff instructions from prior work (each `kent run` is a fresh instance)
- user messages, user answers to questions, tool call outputs from this session, what you said to the user, overall task context, and your memories.
- Overall conversation context, such as an understanding of which feature is being discussed, what the prior issues were, what you decided, what the human decided, and historical records of changes. You must supply sufficient context with the prompt.

## Writing prompts
- State the model-visible job, constraints, and completion condition explicitly. A model should know what it is doing, when it is done, and what must be used for completion.
- Make instructions operational. Replace virtues like "be careful", "high quality", or "robust" with observable behavior, decision rules, and failure handling.
- For subagent communication, communicate as you would in the `analysis` channel. These models are copies of you and can understand terse analysis-channel prose. They don't need the flattery or coherence that humans need.

## Language
- Use direct, plain, imperative language. Prefer "Run tests via `./scripts/test.sh`" over "It may be a good idea to consider running tests."
- Use `must` only for invariants, safety requirements, output contracts, or product rules. Use `should`/`prefer` for heuristics. Use `may` only for permission.
- Use `Do not` for hard prohibitions and pair it with the allowed path when useful.
- Use `Avoid` for quality guidance where judgment is expected.
- Be specific about precedence: "X overrides Y", "only after Z", "if A, then B".
- Never praise an instruction by contrasting it with a worse alternative. Write the good rule directly. Bad: "Run proper tests instead of lint hacking". Good: "Run tests via ./test.sh before marking the task complete".
- Do not include flattery, apologies, sales language, hype, or personality theater.
- Do not anthropomorphize the system or model unless the prompt is deliberately defining an agent role.

## Structure
- Put high-priority identity, role, mode, and completion constraints before detailed workflow guidance.
- Use short Markdown headers and flat bullets. Avoid tables, diagrams, emojis, file trees, and decorative formatting in prompt files. Non-persistent prompts (e.g., `kent run` prompts) can just be compact text.
- Use numbered steps only when order is required. Use bullets for independent rules.
- Include examples for writing, response wording, or communication style to prevent literal interpretation of instructions.
- Do not duplicate guidance already owned by another prompt or skill, or already supplied as context. Link or point to the owning source when the model can read it.

## Giving an assignment to subagents
- Do not delegate the entirety of the user's task to a subagent; it's bad form and disrespectful.
- Subtasks must be concrete, well-defined, and self-contained.
- Delegated subtasks must materially advance the main task.
- Do not duplicate work between the main rollout and delegated subtasks.
- Narrow the delegated ask to the concrete output you need next.
- When delegating coding work, specify the write scope when needed for isolation, but do not restate routine workflow/output requirements unless this subtask needs a non-default deliverable.
- Keep the immediate critical-path task and tightly coupled work local. Delegate implementation only after planning identifies a concrete, bounded subtask that can proceed in parallel without blocking your next local step.
- Run multiple independent information-seeking subtasks in parallel when you have distinct questions that can be answered independently in non-overlapping areas.
- Split implementation into disjoint codebase slices and spawn multiple agents for them in parallel when the write scopes do not overlap.
- Do not duplicate work between the main rollout and delegated subtasks.
- Do not reread the files or redo the work of exploration/research subagents in parallel with them. Once you've started them, commit to waiting for their reports when you need the information to proceed.

## Problematic, incorrect examples
- "Read this file and edit line 147 to include error handling" is too narrow to delegate. Do the work locally.
- "Implement <the entire user task>" delegates the entirety of your work.
- "Build the error handling for my code so that we don't crash" is not a specific, bounded task and lacks the context needed for completion.

## Starting a run
Use the available agent roles and their descriptions to select a role. Exact flags are available through `kent run --help`.

```bash
kent run '<assignment>'
kent run --agent <role> '<assignment>'
kent run --fast '<assignment>'
```

Omitting `--agent` selects the default agent. `--fast` selects the built-in fast role. Fast agents are suitable for menial tasks such as exploration and context gathering, not code review, plan review, or self-checks.

Headless subagent Sessions cannot ask Questions. The command prints the Session ID in a ready-to-use steer command when the run becomes steerable. Agents cannot control their own Session through these commands.

## Steering and continuing
Communicate with an ongoing run to give it additional context or adjust its assignment:

```bash
kent run steer <session-id> '<message>'
```

Continue an idle Session for follow-ups, re-reviews, or further discussion:

```bash
kent run --session <session-id> '<follow-up>'
```

Reuse the existing Session for follow-up work; start a new Session for a separate task. Do not directly resume Workflow Task Sessions with `kent run --session`; Workflow Tasks have their own resume operation.

## Observing and integrating
```bash
kent run wait <session-id>
kent run watch <session-id>
```

`wait` waits for the active run's terminal outcome. `watch` also returns when a Question needs attention.

- When the agent completes, you will be notified. While it works, you can do other work or pause.
- Poll when you need the agent's outputs to continue, not right after spawning them, unless their work blocks yours.
- Do not redo delegated subagent tasks yourself, such as rereading or searching the same files, while they are running. Focus on integrating results, tackling non-overlapping work, or waiting.
- When a delegated coding task returns, quickly review the changes, then integrate or refine them, or continue the Session if needed.
- If you spawn a write-capable subagent, wait for it to finish before finalizing. Do not kill, cancel, or abandon it just because it is slower than expected; it may be mid-edit or mid-test and leave the workspace inconsistent.

To interrupt an active run when the task requires it:

```bash
kent run stop <session-id>
```

## Questions
Inspect or answer another interactive or Workflow Session's pending Question or Approval:

```bash
kent question --session <session-id>
kent question answer --session <session-id> --option <number>
kent question answer --session <session-id> --commentary '<answer>'
```

List answered Questions from another Session:

```bash
kent questions list --session <session-id>
```

Session IDs are supplied by workflows, tasks, other `kent` commands, or developer reminders.
Question history can be slow, especially with `--json`.
