# Projects And Workspaces

## Relationships

- A Project must retain at least one attached workspace.
- A Project has one default workspace, and that workspace must belong to the Project.
- The same workspace path may belong to several Projects, but it may belong to one Project only once.
- Attaching a workspace creates only the Project-workspace relationship.
- Detaching a workspace removes only the selected Project-workspace relationship.
- Attaching, detaching, and changing the default workspace never delete or move files.
- Detaching never deletes or migrates Tasks, Sessions, worktrees, retained Workflow state, or artifacts.

## Detach Safety

- Detach requires an explicit Project and one workspace selected by path or workspace ID.
- Path selection is scoped only to the selected Project.
- If the selected workspace is not attached to the selected Project, detach fails without revealing its relationships to other Projects.
- A saved workspace path may be detached while its directory is missing or inaccessible.
- When path identity cannot be recovered, the operator can select the workspace by ID.
- Detach is blocked for a default workspace, including a Project's sole workspace, non-terminal dependent Tasks, live Session execution, worktree dependencies, or missing retained Session location information.
- Detach is allowed when references are only terminal Tasks and retained Sessions whose locations remain readable.
- A blocked or concurrently invalidated detach changes nothing.
- A concurrently invalidated detach reports that retrying is safe.
- Detach returns a bounded blocker summary. It does not list dependent Task, Session, or worktree IDs.
- Detach never requires direct database access.

## API

- The workspace-detach API requires a Project ID and exactly one workspace selector: workspace ID or workspace path.
- The default-workspace API requires a Project ID and exactly one workspace selector: workspace ID or workspace path.
- Workspace-ID requests remain supported.
- Each API operation resolves its selector inside the selected Project and applies the requested change as one operation.
- A resolved detach result identifies the selected Project and workspace.
- A blocked detach returns every blocker with its stable code, human-readable message, and count when present.
- The detach success result contains exactly `project_id` and `workspace_id`.
- The default-workspace success result contains exactly `project`, whose value is the authoritative updated Project.
- Mutation error objects contain only `code`, `message`, resolved Project/workspace IDs when resolution succeeded, bounded `blockers` when detach is blocked, and `retryable: true` for a concurrent detach conflict.
- Canonical workspace roots are never included in mutation success or error JSON results.

## CLI Project Deletion

- `kent project delete <project-id>` selects a Project only by its canonical Project ID.
- Project deletion never deletes or moves workspace files.
- The server owns Project deletion and its blockers. The CLI never deletes Project data or workspace files directly.
- Project deletion is non-interactive.
- The CLI requires `--confirm` before it requests deletion.
- Before checking confirmation, the CLI checks whether the Project contains any Task whose state is not terminal, including a Backlog Task.
- If unfinished work exists and the command is running inside a Kent agent shell, the command is human-only and reports exactly `Project deletion is human-only because project <project-id> contains unfinished work.`
- The unfinished-work check establishes only whether unfinished work exists. It does not count or list Tasks.
- The unfinished-work check uses a command-time snapshot. A concurrent Task change can occur between that check and the deletion request.
- If unfinished work appears after the snapshot, an agent's Project deletion may still complete. The CLI does not provide atomic human-only enforcement.
- The server applies its authoritative deletion blockers when it processes the deletion request.
- If confirmation is absent after the conditional human-only check permits the caller, the command makes no deletion request and reports exactly `Project deletion was not confirmed. Rerun with --confirm to delete project <project-id>.`
- Successful plain output is exactly `Deleted project <project-id>. Workspace files were not deleted.`
- Blocked plain output preserves every server blocker in server order with its code, message, and positive count when present. The CLI adds no blocker guidance.
- A successful deletion exits with status 0.
- Missing confirmation, conditional human-only denial, blocked deletion, Project lookup failure, and other operational failures exit with status 1.
- Usage errors exit with status 2.
- `--json` emits exactly one JSON object on stdout for every operational outcome after valid argument parsing.
- Successful JSON uses `status: "ok"` and includes the canonical Project ID as `result.project_id`.
- Failed JSON uses `status: "error"` and includes an `error` object with `code` and `message`.
- Every failed JSON result after valid argument parsing includes the canonical requested Project ID as `error.project_id`.
- A blocked JSON result includes every server blocker in server order at `error.blockers`.
- Each JSON blocker preserves the server's `code` and `message`. It includes `count` only when the count is positive.
- Non-blocked JSON errors omit `blockers`.
- Stable Project deletion error codes are `confirmation_required`, `human_only_unfinished_work`, `project_not_found`, `project_delete_blocked`, and `request_failed`.

## CLI Detach

- `kent detach` is the canonical detach command.
- `kent project detach` is an equivalent alias exposed in live help but omitted from public documentation.
- Both forms require `--project <project-id>`.
- Both forms accept either one positional workspace path or `--workspace <workspace-id>`.
- The selectors are mutually exclusive.
- The path defaults to the current directory when neither selector is supplied.
- Before making the API request, the CLI resolves `.` from its loaded current workspace and converts every path selector to an absolute path. The server never interprets a CLI-relative path against its own working directory.
- Paths refer to the Kent server's filesystem.
- Detach applies immediately without a prompt or confirmation flag.
- Successful plain output is the detached workspace ID followed by a newline.
- Blocked plain output reports only the first blocker in Kent's response order, including its code, count when present, explanation, and next-step guidance.
- Blocked plain output exits with status 1 and changes nothing.
- Plain operational failures write no successful result and exit with status 1.
- Usage errors exit with status 2.
- `--json` emits exactly one JSON object on stdout for every operational outcome after valid argument parsing.
- Successful JSON uses `status: "ok"` and includes `project_id` and `workspace_id` in `result`.
- Failed JSON uses `status: "error"` and includes an `error` object with `code` and `message`.
- A failed JSON result includes `project_id` and `workspace_id` together only after the workspace relationship has resolved.
- A blocked JSON result includes a non-empty `blockers` array containing every bounded blocker.
- Each blocker includes `code`, `message`, and `guidance`. It includes `count` only when the count is positive.
- A retryable concurrent conflict includes `retryable: true`.
- `result`, `error`, Project/workspace IDs, `blockers`, `count`, and `retryable` are omitted when absent. They are never represented by `null`, `false`, zero, or an empty collection.
- Stable detach error codes are `project_not_found`, `workspace_not_attached`, `workspace_detach_blocked`, `workspace_detach_conflict`, and `request_failed`.
- When Kent cannot recover an inaccessible selected path's identity, `request_failed` directs the operator to retry with `--workspace <workspace-id>`.
- Other `request_failed` outcomes do not include workspace-ID fallback guidance.
- Successful operation exits with status 0. Failed operation exits with status 1.

## CLI Default Workspace

- `kent project default` requires `--project <project-id>`.
- It accepts either one positional workspace path or `--workspace <workspace-id>`.
- The selectors are mutually exclusive.
- The path defaults to the current directory when neither selector is supplied.
- Path selectors use the same pre-request absolute-path resolution as detach.
- The command applies immediately without confirmation.
- Selecting the current default workspace succeeds without changing the Project.
- Successful plain output is exactly `done` followed by a newline.
- Successful JSON uses `status: "ok"` and returns the authoritative updated Project at `result.project`.
- Failed JSON uses the same typed error envelope and exit-status policy as detach.
- Stable default-workspace error codes are `project_not_found`, `workspace_not_attached`, and `request_failed`.
- When the selected replacement is not attached, Kent directs the operator to attach it before retrying default selection.
- When Kent cannot recover an inaccessible selected path's identity, `request_failed` directs the operator to retry with `--workspace <workspace-id>`; unrelated request failures do not.

## Detach Blocker Guidance

- `default_workspace` directs the operator to choose another attached default workspace with `kent project default`, then retry detach. If the replacement is not attached, the operator attaches it with `kent attach` first.
- `active_sessions` directs the operator to stop active runs or rebind Sessions that use the workspace, then retry detach.
- `non_terminal_tasks` directs the operator to move editable Backlog Tasks to another source workspace, or complete, manually move, or delete dependent Tasks, then retry detach.
- `executable_current_nodes` directs the operator to stop execution and move, complete, or delete affected Tasks until no executable Current Node uses the workspace, then retry detach.
- `managed_owned_worktrees` directs the operator to delete dependent worktrees or their owning quiescent Tasks, then retry detach.
- `missing_history_snapshot` directs the operator to re-save an editable Task's source workspace. If the affected Task history cannot be edited, the operator keeps the binding and reports the blocker because detach is unsafe.
- For an unknown blocker code, Kent preserves the server message, directs the operator to resolve that code and retry, and tells the operator to update the CLI and server together when the CLI does not recognize the code. Kent never invents a command for an unknown blocker.
