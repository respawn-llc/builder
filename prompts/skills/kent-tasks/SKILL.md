---
name: kent-tasks
description: Use the Kent CLI to create, inspect, start, list, and comment on workflow tasks. Use when the user asks to manage Kent task records, execution targets, task state, or tickets.
---

## How tasks work
A task is a durable user-facing unit of work moving through one workflow.

- Tasks live under the intersection of a **Workflow**, which defines the process graph, and a **Project**, which defines the execution environment and source workspaces.
- Projects are sets of workspace directories, with one primary workspace (e.g. `~/Kent`) and secondary ones (e.g. `~/Kent-Marketing`).
- Tasks follow the project's default workflow unless another linked workflow is selected. Each task has a source workspace.
- The workflow's execution-target policy is evaluated when an unlocked task first reaches executable work. It may run directly in the source workspace or create a managed task worktree from source `HEAD`, the repository default branch, or a custom Git revision.
- Every workflow runs in **the same environment as you do** - it may be the user's local machine, or if you are running on a server, there. You can generally assume that what you have available here in this environment will also be available for agents that work on tasks (such as main-workspace docs, git repos, committed files, system utilities or tools), but you should avoid leaking PII, credentials, or references to outside-workspace paths (e.g. ~/Desktop, ~/Documents) that are unstable and may be moved or deleted by the user.
- Task body and comments are formatted as markdown.

## Principles of task management
- User will be asking you to create tasks, or you might decide to interact with tasks on your own. Both are fine, with the exception that you do **NOT** execute destructive (task delete, task edit with removal of information, task comment delete on others comments) and cost-incurring (task start, task move, task approve) ops without explicit user consent or request.
- Structure managed-worktree tasks so each branch is independently shippable. Tasks do not share unmerged worktree changes. Keep coupled work in one task, or split it into slices that can merge independently.
- A no-managed-worktree target runs in the source workspace and does not provide branch isolation. Inspect the workflow policy and task target before assuming concurrent tasks are isolated.
- You can inspect the workflow with `kent workflow list` and `kent workflow inspect` for context on what will actually be done for any given task. Adapt the level of detail and how you write requirements in tasks to the workflow you're working with. More info in the `kent-workflows` skill.
- Task titles should be under 40 characters of plain text.
- Task bodies should avoid excessive fancy formatting, tables, LaTeX, file trees, verbatim code blocks, and H1 task-level headers like "My task" that duplicate the task title field. Task bodies are for **agents** first, and humans second.
- Code samples are fine if they are small, but do not attempt to write the task's code in its body.
- You do not know **when** tasks will be done - it might be in 10 minutes, or 6 months from now. Avoid including references to any information that can be lost in tasks, such as paths to files on the local user machine outside the workspace (~/Desktop etc.), `/tmp/`-based paths, or ephemeral files. Comprehensively include the information that the agent who picks it up will need to successfully and fully complete it.
- Tasks are **public** to other users of the machine and **not encrypted**, so NEVER include sensitive info, PII, or credentials in tasks. Discuss with the user how the necessary credentials may be provided to agents that want them (`.env`? keychain? etc).
- Be an effective project manager for agents that will work on the task. The agents will begin working with **zero** extra knowledge or memory beyond **only the task body, workflow node instructions, repo guidance, and worktree repo state**, as their context. Give them context into **why** something is being done, what is the DoD, how to verify completion, what are project and product requirements for the task, what are the completion criteria, what are the caveats to watch out for, what are the decisions made and background for this task. Ask the user questions to clarify anything that might be confusing, up to interpretation, double-edged, ambiguous, uncertain, or imply tradeoffs. Treat the user as the CPO and yourself as the PM. Being effective also implies **avoiding micromanaging** the agents: by default, don't dictate approaches to work, tools to use, filenames, code shapes, skills to use, files to read in the task, unless the user clarifies that's needed per their workflow.

## How to control tasks
Authoritative command details are always the live CLI:

```bash
kent task --help
```

Args marked with `[]` are optional and will attempt to auto-resolve - current cwd's project, default workflow for that project, current session ID.

Create task records against a linked/default workflow and project, then inspect them:

```bash
kent task create [--project "path or id"] [--workflow "id"] --title "Fix flaky workflow tests" --body ".md content"
kent task list [--project]
kent task show <short-id-or-task-id>
```

## Execution targets
`kent task show` always reports the source workspace. After target lock it also reports the target mode and execution root; managed targets include requested revision, resolved commit, current named branch when available, and managed worktree.

`kent task start`, `kent task approve`, and `kent task move` never prompt. If an action reports that selection is required, rerun the same action with one concrete selector:

```bash
kent task start <task> --execution-target none
kent task start <task> --execution-target head
kent task start <task> --execution-target default-branch
kent task start <task> --execution-target ref:<revision>
```

The override applies only to an unlocked task and does not edit the workflow. Do not attempt to replace a locked target.

## Approvals And Manual Moves
Some workflow transitions wait for human approval before target runs start. Inspect the task before approving, rejecting, or moving it so you know which workflow transition is pending.

When approval or movement requires an execution target, rerun it with the same concrete selectors:

```bash
kent task approve <transition-id> --execution-target none|head|default-branch|ref:<revision>
kent task move <task> <target-node-id> --execution-target none|head|default-branch|ref:<revision>
```

## Comments
Task comments are task-local notes mostly for **agents**. They are useful for design discussion, decision logs, review notes, cross-agent comms, and work logs that should not be committed into a worktree but need to be preserved beyond your memory. Good candidates are notes about the approaches taken, discussion between agents, hacks, caveats, etc. that don't fit in the repository or project's existing documentation paradigms. Generally, feel free to comment under tasks as much as you want if there is no better explicit location specified for the info you're trying to save. Agents that work on the task may or may not read your comments.

```bash
kent task comment add [--project] <short-id-or-task-id> --body "Please prioritize the failing scheduler test."
kent task comment list [--project] <short-id-or-task-id>
kent task comment replace <comment-id> --body "Updated note."
```
