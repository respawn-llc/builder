---
title: Worktrees
description: Create, switch, and delete git worktrees from Kent.
---

Kent can manage git worktrees for you. Creating or switching a worktree moves that session into the selected checkout and gives the agent necessary context. Managed worktrees are created under `config.toml`'s `[worktrees].base_dir`, and Kent switches the session into the new worktree after creation and setup succeed. Run `/wt` to get started.

## Switch

`<target>` must resolve uniquely. Kent matches, in order:

- worktree id
- canonical path
- display name
- branch name
- `main` for the main workspace worktree

## Delete

The main workspace worktree cannot be deleted. Deletion is blocked when another session targets that worktree or when background processes are running inside it.

:::tip
If you delete the active worktree, Kent moves the session back to the main workspace without preserving file edits or commits.
:::

## Configuration

Since worktrees are raw git checkouts, you can set a custom worktree creation script that prepares newly created checkouts with local data such as `.env`, encryption credentials, Gradle wrappers, installed dependencies, local skills, docs, or config.

```toml
[worktrees]
base_dir = "~/.kent/worktrees"
# setup_script = "scripts/setup-worktree.sh"
# setup_timeout_seconds = 60
```

- `base_dir` sets the root directory for Kent-managed worktrees.
- `setup_script` runs after Kent creates and records a new worktree, before Kent switches a session into it or starts a workflow run that uses it. Relative paths resolve from the source workspace root.
- `setup_timeout_seconds` sets the setup script timeout. The default is `60`; `0` or a negative value disables the timeout.

Setup is blocking because worktree-local context can change what the agent sees. Files created by setup, including local skills under `.kent/skills` and workspace-local docs, are present before Kent locks the new session or workflow run context.

While setup is running, Kent reports live setup progress with the resolved script path and worktree path. If setup fails, times out, or is canceled, the foreground create/start operation fails and Kent does not switch the session or start the workflow run against that worktree. The created worktree and Kent metadata remain available so you can inspect, repair, or delete them.

Kent invokes the script with the new worktree as its cwd and three positional arguments:

1. source workspace root
2. branch name
3. worktree root

Kent supplies these reserved environment variables, replacing conflicting inherited values:

- `KENT_WORKTREE_SOURCE_WORKSPACE_ROOT` - Original/main workspace root that created the worktree, e.g. `/home/user/dev/app` or `C:\Users\user\dev\app`.
- `KENT_WORKTREE_BRANCH_NAME` - Branch/ref name selected for the new worktree, e.g. `feature/search-fix`.
- `KENT_WORKTREE_ROOT` - Filesystem path to the newly created worktree; setup script runs with this as cwd, e.g. `/home/user/.kent/worktrees/app/search-fix`.
- `KENT_WORKTREE_SESSION_ID` - Kent session id that requested the worktree, e.g. `b31234ab-78ce-43d1-8f4c-2d6c6d4adbc1`. Present only when a session initiates creation; workflow task setup omits it.
- `KENT_WORKTREE_PROJECT_ID` - Kent project id for the workspace/project, e.g. `project-94b18685-19ed-4513-96bb-bcffa10410ff`.
- `KENT_WORKTREE_WORKSPACE_ID` - Kent workspace binding id for the source workspace, e.g. `workspace-2f7b6d4a`.
- `KENT_WORKTREE_WORKTREE_ID` - Kent metadata id for the created worktree, e.g. `worktree-8c9a0e3f`.
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
