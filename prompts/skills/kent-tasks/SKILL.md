---
name: kent-tasks
description: How to use the Kent CLI to manage tasks and task comments - create, inspect, and list tasks, and add/list/replace comments. Use when the user asks to create/edit Kent task records, inspect task states.
---

## How tasks work
A task is a durable user-facing unit of work moving through one workflow.

- Tasks live under the intersection of a **Workflow**, which defines the Kanban board shape the user sees, and a **Project**, which defines the execution environment, workspaces, worktrees, rules, etc.
- Projects are sets of workspace directories, with one primary workspace (e.g. `~/Kent`) and secondary ones (e.g. `~/Kent-Marketing`).
- Tasks default to a **new git worktree** created off of **primary workspace**'s **main branch** and follow the **default workflow** unless specified otherwise.
- Every workflow runs in **the same environment as you do** - it may be the user's local machine, or if you are running on a server, there. You can generally assume that what you have available here in this environment will also be available for agents that work on tasks (such as main-workspace docs, git repos, committed files, system utilities or tools), but you should avoid leaking PII, credentials, or references to outside-workspace paths (e.g. ~/Desktop, ~/Documents) that are unstable and may be moved or deleted by the user.
- Task body and comments are formatted as markdown.

## Principles of task management
- User will be asking you to create tasks, or you might decide to interact with tasks on your own. Both are fine, with the exception that you do **NOT** execute destructive (task delete, task edit with removal of information, task comment delete on others comments) and cost-incurring (task start, task move, task approve) ops without explicit user consent or request.
- Because tasks are created off of main branch and **each task** has its own worktree, it is **required** to structure tasks such that each one is **shippable from a separate branch**. Tasks do NOT share code between them unless the worktree is explicitly merged before the next task starts, and there is no way to enforce sharing by the agent. For example, don't break big features into tasks that require all of the coding to be done on a single worktree; that should be ONE task or slices that are feature-gated/isolated enough to be merged separately instead.
- You can inspect the workflow with `kent workflow list` and `kent workflow inspect` for context on what will actually be done for any given task. Adapt the level of detail and how you write requirements in tasks to the workflow you're working with. More info in the `kent-workflows` skill.
- Task titles should be under 40 characters of plain text.
- Task bodies should avoid excessive fancy formatting, tables, LaTeX, file trees, verbatim code blocks, and H1 task-level headers like "My task" that duplicate the task title field. Task bodies are for **agents** first, and humans second.
- Code samples are fine if they are small, but do not attempt to write the task's code in its body.
- You do not know **when** tasks will be done - it might be in 10 minutes, or 6 months from now. Avoid including references to any information that can be lost in tasks, such as paths to files on the local user machine outside the workspace (~/Desktop etc.), `/tmp/`-based paths, or ephemeral files. Comprehensively include the information that the agent who picks it up will need to successfully and fully complete it.
- Tasks are **public** to other users of the machine and **not encrypted**, so NEVER include sensitive info, PII, or credentials in tasks. Discuss with the user how the necessary credentials may be provided to agents that want them (`.env`? keychain? etc).
- Be an effective project manager for agents that will work on the task. The agents will begin working with **ZERO** extra knowledge or memory, beyond **only the task body, workflow node instructions, and worktree repo state**, as its context. Give them context into WHY something is being done, what is the DoD, how to verify completion, what are project and product requirements for the task, what are the completion criteria, what are the caveats to watch out for, what are the decisions made and background for this task. Ask the user questions to clarify anything that might be confusing, up to interpretation, double-edged, ambiguous, uncertain, or imply tradeoffs. Treat the user as the CPO and yourself as the PM. Being effective also implies **avoiding micromanaging** the agents: by default, don't dictate approaches to work, tools to use, filenames, code shapes, skills to use, files to read in the task, unless the user clarifies that's needed per their workflow.

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

## Approvals And Manual Moves
Some workflow transitions wait for human approval before target runs start. Inspect the task before approving, rejecting, or moving it so you know which workflow transition is pending.

Manual moves reject a pending approval and only apply to transition paths whose context source is supported by the manual-move path.
The runtime rejects `selected_node`, `node:<node-key>`, `previous_target`, and `previous_target_or_new`; use normal workflow transitions for those cases.

## Comments
Task comments are task-local notes mostly for **agents**. They are useful for design discussion, decision logs, review notes, cross-agent comms, and work logs that should not be committed into a worktree but need to be preserved beyond your memory. Good candidates are notes about the approaches taken, discussion between agents, hacks, caveats, etc. that don't fit in the repository or project's existing documentation paradigms. Generally, feel free to comment under tasks as much as you want if there is no better explicit location specified for the info you're trying to save.

```bash
kent task comment add [--project] <short-id-or-task-id> --body "Please prioritize the failing scheduler test."
kent task comment list [--project] <short-id-or-task-id>
kent task comment replace <comment-id> --body "Updated note."
```
