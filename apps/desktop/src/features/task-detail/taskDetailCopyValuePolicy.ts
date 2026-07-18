export type TaskDetailCopyValueKind =
  | Readonly<{ kind: "source_workspace_path" }>
  | Readonly<{ kind: "managed_worktree_path" }>
  | Readonly<{ kind: "commit" }>
  | Readonly<{ kind: "transition_output"; outputName: string }>;

type NonInterpolatedTranslationKey =
  | "task.copySourceWorkspacePath"
  | "task.sourceWorkspacePathCopied"
  | "task.sourceWorkspacePathCopyFailed"
  | "task.copyManagedWorktreePath"
  | "task.managedWorktreePathCopied"
  | "task.managedWorktreePathCopyFailed"
  | "task.copyResolvedCommit"
  | "task.resolvedCommitCopied"
  | "task.resolvedCommitCopyFailed";

type InterpolatedTranslationKey =
  "task.copyOutputValue" | "task.outputValueCopied" | "task.outputValueCopyFailed";

export type TaskDetailCopyValueLocalization =
  | Readonly<{ titleKey: NonInterpolatedTranslationKey }>
  | Readonly<{
      titleKey: InterpolatedTranslationKey;
      interpolation: Readonly<{ name: string }>;
    }>;

export type TaskDetailCopyValueNoticePolicy = Readonly<{
  copyLabel: TaskDetailCopyValueLocalization;
  success: TaskDetailCopyValueLocalization & Readonly<{ id: string }>;
  failure: TaskDetailCopyValueLocalization & Readonly<{ id: string }>;
}>;

export function taskDetailCopyValueNoticePolicy(
  value: TaskDetailCopyValueKind,
): TaskDetailCopyValueNoticePolicy {
  switch (value.kind) {
    case "source_workspace_path":
      return {
        copyLabel: { titleKey: "task.copySourceWorkspacePath" },
        success: {
          id: "task-source-workspace-path-copied",
          titleKey: "task.sourceWorkspacePathCopied",
        },
        failure: {
          id: "task-source-workspace-path-copy-failed",
          titleKey: "task.sourceWorkspacePathCopyFailed",
        },
      };
    case "managed_worktree_path":
      return {
        copyLabel: { titleKey: "task.copyManagedWorktreePath" },
        success: {
          id: "task-managed-worktree-path-copied",
          titleKey: "task.managedWorktreePathCopied",
        },
        failure: {
          id: "task-managed-worktree-path-copy-failed",
          titleKey: "task.managedWorktreePathCopyFailed",
        },
      };
    case "commit":
      return {
        copyLabel: { titleKey: "task.copyResolvedCommit" },
        success: {
          id: "task-commit-copied",
          titleKey: "task.resolvedCommitCopied",
        },
        failure: {
          id: "task-commit-copy-failed",
          titleKey: "task.resolvedCommitCopyFailed",
        },
      };
    case "transition_output": {
      const interpolation = { name: value.outputName };
      return {
        copyLabel: {
          interpolation,
          titleKey: "task.copyOutputValue",
        },
        success: {
          id: `task-transition-output-copied-${value.outputName}`,
          interpolation,
          titleKey: "task.outputValueCopied",
        },
        failure: {
          id: `task-transition-output-copy-failed-${value.outputName}`,
          interpolation,
          titleKey: "task.outputValueCopyFailed",
        },
      };
    }
  }
}
