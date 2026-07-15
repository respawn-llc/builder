---
title: Worktrees
description: Create, enter, and delete Git worktrees from Kent.
---

Kent can create and manage worktrees for you. Agents will enter new worktrees if they need to, and workflows can automatically create worktrees for their tasks (see [workflows](../workflows)). If you want to manually manage worktrees, run `/wt` in the TUI, or ask the agent to use the CLI:

```bash
kent worktree status
kent worktree list
kent worktree create <branch-or-ref> [path]
kent worktree enter <selector>
kent worktree leave
kent worktree delete <selector>
```

Every command supports `--json`.

- `list` labels worktrees by availability:
  - **registered**: available to Git and managed by Kent
  - **external**: available to Git but not managed by Kent; entering it registers it
  - **missing**: managed by Kent, but absent from Git
- `create` prepares the checkout and runs its setup script, but does not move the agent into it yet.
The agent can then call `enter` to teleport itself into a worktree, and `leave` to go back to the main checkout.

Dirty worktrees, or worktrees whose state cannot be determined, require `--force`. Agent-shell deletion by default retains branches, so instruct the agent to use --force or delete the branch if you want full cleanup.

## Configuration

Use a setup script to prepare new worktrees with local data such as `.env` files, encryption credentials, Gradle wrappers, installed dependencies, local skills, docs, or config.

```toml
[worktrees]
base_dir = "~/.kent/worktrees"
# setup_script = "scripts/setup-worktree.sh"
# setup_timeout_seconds = 60
```

- `base_dir` sets the root directory for Kent-managed worktrees.
- `setup_script` runs after Kent creates a worktree and before the create command or a workflow run uses it. Relative paths resolve from the source workspace root.
- `setup_timeout_seconds` sets the setup script timeout. The default is `60`; `0` or a negative value disables the timeout.

- Kent waits for setup to finish before the agent can proceed, so the script should be relatively fast and shell out to async processes if it needs a lot of time to complete. 
- If setup fails, times out, or is canceled, the worktree is not cleaned up, leaving it possibly in an incomplete state, so design the script to, ideally, be idempotent and reversible.

Kent invokes the script with the new worktree as its cwd and three positional arguments:

1. source workspace root
2. branch name
3. worktree root

Kent supplies these reserved environment variables:

- `KENT_WORKTREE_SOURCE_WORKSPACE_ROOT` - Original/main workspace root that created the worktree, e.g. `/home/user/dev/app` or `C:\Users\user\dev\app`.
- `KENT_WORKTREE_BRANCH_NAME` - Branch/ref name selected for the new worktree, e.g. `feature/search-fix`.
- `KENT_WORKTREE_ROOT` - Filesystem path to the newly created worktree; setup script runs with this as cwd, e.g. `/home/user/.kent/worktrees/app/search-fix`.
- `KENT_WORKTREE_SESSION_ID` - Kent session id that requested the worktree, e.g. `b31234ab-78ce-43d1-8f4c-2d6c6d4adbc1`. Present only when a session initiates creation; workflow task setup omits it.
- `KENT_WORKTREE_PROJECT_ID` - Kent project id for the workspace/project, e.g. `project-94b18685-19ed-4513-96bb-bcffa10410ff`.
- `KENT_WORKTREE_WORKSPACE_ID` - Kent workspace binding id for the source workspace, e.g. `workspace-2f7b6d4a`.
- `KENT_WORKTREE_WORKTREE_ID` - UUID for the created worktree, e.g. `c4aaf0cf-4c50-4560-b6a2-6c294d0b1495`.
- `KENT_WORKTREE_CREATED_BRANCH` - Whether Kent created a new branch for this worktree, e.g. `true` or `false`.
- `KENT_WORKTREE_PAYLOAD_JSON` - Full setup payload as one JSON string containing all fields above, e.g. `{"source_workspace_root":"/repo","branch_name":"feature/x","worktree_root":"/repo-wt","session_id":null,"project_id":"...","workspace_id":"...","worktree_id":"...","created_branch":true}`.

It also receives the same payload as JSON on stdin:

```json
{
    "source_workspace_root": "/path/to/main/workspace",
    "branch_name": "feature/name",
    "worktree_root": "/path/to/new/worktree",
    "session_id": null,
    "project_id": "...",
    "workspace_id": "...",
    "worktree_id": "...",
    "created_branch": true
}
```

`session_id` is nullable: workflow task setup supplies `null`, while session-originated creation supplies the requesting session ID.
