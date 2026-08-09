# Projects And Workspaces

## Relationships

- A Project must retain at least one attached workspace.
- A Project has one default workspace, and that workspace must belong to the Project.
- The same workspace path may belong to several Projects, but it may belong to one Project only once.
- Attaching a workspace creates only the Project-workspace relationship.
- Detaching a workspace removes only the selected Project-workspace relationship.
- Attaching, detaching, and changing the default workspace never delete or move files.
- Detaching never deletes or migrates Tasks, Sessions, worktrees, retained Workflow state, or artifacts.
- Project Workspace collections contain at most the 500 most recently attached Workspaces.
- Older Workspaces remain attached and remain selectable by exact Project-scoped path or Workspace ID when omitted from a Project Workspace collection.
- The global collection limit bounds memory and response growth for Project consumers, including unpaginated Project overview reads.

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
