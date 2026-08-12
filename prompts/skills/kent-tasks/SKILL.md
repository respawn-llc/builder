---
name: kent-tasks
description: Use the Kent CLI to create, inspect, start, list, and comment on workflow tasks. Use when the user asks to manage tickets/tasks, or mentions Jira-like IDs such as KENT-42.
---

## How tasks work
A task is a durable user-facing unit of work moving through one workflow.

- Tasks live under the intersection of a **Workflow**, which defines the process graph, and a **Project**, which defines the execution environment and source workspaces.
- Projects are sets of workspace directories, with one primary workspace (e.g. `~/kent`) and secondary ones (e.g. `~/kent-marketing`).
- Tasks follow the project's default workflow unless another linked workflow is selected. Each task has a source workspace.
- The workflow's git worktree policy is evaluated when an unlocked task first reaches executable work. It may run directly in the source workspace or create a git worktree. More info in the kent-workflows docs.
- Every workflow runs in **the same environment as you do** - it may be the user's local machine, or if you are running on a server, there. You can generally assume that what you have available here in this environment will also be available for agents that work on tasks (such as main-workspace docs, git repos, committed files, system utilities or tools), but you should avoid leaking PII, credentials, or references to outside-workspace paths (e.g. ~/Desktop, ~/Documents) that are unstable and may be moved or deleted by the user.
- Task body and comments are formatted as markdown.

## Principles of task management
- User will be asking you to create tasks, or you might decide to interact with tasks on your own. Both are fine, with the exception that you do **NOT** execute destructive (task delete, task edit with removal of information, task comment delete on others comments) and cost-incurring (task start, task move, task approve) ops without explicit user consent or request.
- You may start, interrupt, resume, approve, or move another Task, but never the Task assigned to your workflow Session. Do not attempt to evade this restriction.
- Structure worktree-based tasks so each task is independently shippable. Tasks do not share unmerged worktree changes. Keep coupled work in one task, create dependent tasks that can merge sequentially, or select a shared git worktree policy when starting the task.
- You can inspect the workflow with `kent workflow list` + `kent workflow inspect` for context on what will actually be done for any given task. Adapt the level of detail and how you write requirements in tasks to the workflow you're working with. More info in the `kent-workflows` docs.
- Task titles should be under 40 characters of plain text.
- Task bodies should avoid excessive fancy formatting, tables, LaTeX, file trees, verbatim code blocks, and H1 task-level headers like "# Task Title" that duplicate the task title field. Task bodies are for **agents** first, and humans second, keep them on-point.
  - Code samples are fine if they are small, but do not attempt to write the task's code in its body.
- You do not know **when** tasks will be done - it might be in 10 minutes, or 6 months from now. Avoid including references to any information that can be lost in tasks, such as paths to files on the local user machine outside the workspace (~/Desktop etc.), `/tmp/`-based paths, or ephemeral files. 
- Tasks are public to other users of the machine and not encrypted, so NEVER include sensitive info, PII, or credentials in tasks. Discuss with the user how the necessary credentials may be provided to agents that want them (`.env`? keychain? etc).
- Be an effective project manager for agents that will work on the task. The agents will begin working with **zero** extra knowledge or memory beyond **only the task body, workflow node instructions, repo guidance, and worktree repo state**, as their context. Give them context into **why** something is being done, what is the DoD, how to verify completion, what are project and product requirements for the task, what are the completion criteria, what are the caveats to watch out for, what are the decisions made and background for this task. Ask the user questions to clarify anything that might be confusing, up to interpretation, double-edged, ambiguous, uncertain, or imply tradeoffs. Treat the user as the CPO and yourself as the PM. Being effective also implies **avoiding micromanaging** the agents: by default, don't dictate approaches to work, tools to use, filenames, code shapes, skills to use, files to read in the task, unless the user clarifies that's needed per their workflow.
- Do not speculatively include or invent product behavior, completion criteria, features, requirements the user did not call for when creating tasks. Instead, mark anything tentative as TBD with the user, or ask upfront.
- Proactively explore and assign task labels and dependencies if the meaning is clear.

## How to control tasks
Authoritative command details are always the live CLI at `kent task --help` Args marked with `[]` are optional and attempt to autoresolve from your environment. `--json` is **very verbose**, only use it for scripting or if the regular outputs are not sufficient.

Example: creating a task:

```bash
kent workflow list --project . # pick a workflow
kent task label list # pick labels
kent task search "possible duplicates" # look for duplicates or deps
kent task create --title "Fix flaky workflow tests" --body ".md content" --label "Bug" --label "P0"
kent task dep add --blocker <task> --blocked <task> [--project <project>] # add dependencies for task chains
kent task start <short-id> # start only if the user asked to
```

Example: inspecting tasks:

```bash
kent task list [--project] # paginate through tasks
kent task show <short-id-or-task-id> # get more details
kent task dep list <task> [--direction blocks|blocked-by] [--project <project>]
kent task comments list <short-id> # read comments
```

## Comments
Task comments are task-local notes mostly for **agents**. They are useful for design discussion, decision logs, cross-agent comms, notes or caveats that should not be committed into a worktree but need to be preserved beyond your memory. Good candidates are notes about the approaches taken, discussion between agents, possible followups, caveats, etc. that don't fit in the repository or project's existing documentation paradigms. Generally, feel free to comment under tasks as much as you want if there is no better explicit location specified for the info you're trying to save. Agents that work on the task may or may not read your comments.

```bash
kent task comment add [--project] <short-id-or-task-id> --body "Please prioritize the failing scheduler test."
kent task comment list [--project] <short-id-or-task-id>
kent task comment replace <comment-id> --body "Updated note."
```

## Approvals, Interruptions, Resumes and Starts
Some workflow transitions wait for approval before target runs start. If the user is asking you to manage tasks for them, understand that starting tasks incurs costs, moving tasks can confuse the agents who worked on them, and approving a task you are not 100% confident the user actually would approve themselves may result in unauthorized destructive changes.

- If you're not sure if the user would approve the task themselves, ask them.
- If you're about to move a task but work has been executed on it (for example, you're moving a task from Implementation back to Planning), the **workspace changes, worktree, and local edits are NOT undone** when that happens. Moving a task like that can confuse the agent who will see the task assigned for planning be already implemented, for example.
- If you're starting a task, manage the task concurrency, spending and the machine's capacity, e.g., 20 concurrent tasks may place strain on the machine and user's budget/rate limits.
- If the user did not ask you to move, approve, or start tasks, never do it proactively.
- Interrupting and resuming tasks is generally safe and can be done without consequence, but a task that was interrupted for more than 30-60 minutes can incur additional costs when resumed.

Task start and executable Task move check task dependencies before starting. Rerun with `--ignore-dependencies` to run the task despite them only if the user explicitly said to ignore deps:

```bash
kent task start <short-id-or-task-id> --ignore-dependencies
kent task move <short-id-or-task-id> <target-node-id> --ignore-dependencies
```

When approval or movement requires a Git execution target, rerun it with the same concrete selectors:

```bash
kent task approve <transition-id> --execution-target none|head|default-branch|ref:<revision>
kent task move <task> <target-node-id> --execution-target none|head|default-branch|ref:<revision>
```

## Supervising tasks
The user may ask you to "babysit", supervise or manage running tasks.
You can learn what agent sessions are running/ran for a task using `kent task sessions` command.
You can track task completion with `kent task watch` and `kent task wait` commands. 
- `watch` will block and wait until the task needs any sort of attention - interruption, completion, **question**, approval or error. 
- You can watch tasks as background shells to get notified about them, then take action, like answering questions with `kent question` command. 
- `wait` will block until the task completes or errors, excluding attention.
- You can communicate with the agents working on a task using the `kent run steer` command. Avoid directly resuming task sessions with `kent run --continue`, that may confuse the agent or detach the session from the task.
