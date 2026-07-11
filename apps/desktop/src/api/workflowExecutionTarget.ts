import type { WorkflowExecutionPolicy } from "./models";

export type WorkflowTaskExecutionTargetSelectionMode = "none" | "head" | "default_branch" | "custom_ref";

export type WorkflowTaskExecutionTargetSelection =
  | Readonly<{ mode: "custom_ref"; customRef: string }>
  | Readonly<{
      mode: Exclude<WorkflowTaskExecutionTargetSelectionMode, "custom_ref">;
      customRef: null;
    }>;

export type WorkflowTaskExecutionTargetSource =
  | Readonly<{ kind: "non_git"; namedRef: null; commit: null }>
  | Readonly<{ kind: "named_ref"; namedRef: string; commit: string }>
  | Readonly<{ kind: "detached_commit"; namedRef: null; commit: string }>
  | Readonly<{ kind: "unavailable"; namedRef: null; commit: null }>;

export type WorkflowTaskLockedExecutionTargetSource = Extract<
  WorkflowTaskExecutionTargetSource,
  Readonly<{ kind: "named_ref" | "detached_commit" }>
>;

export type TaskExecutionTarget =
  | Readonly<{ policy: "none"; customRef: null; source: null }>
  | Readonly<{
      policy: "head" | "default_branch";
      customRef: null;
      source: WorkflowTaskLockedExecutionTargetSource;
    }>
  | Readonly<{
      policy: "custom_ref";
      customRef: string;
      source: WorkflowTaskLockedExecutionTargetSource;
    }>;

export type WorkflowTaskExecutionTargetSelectionRequired = Readonly<{
  taskID: string;
  generation: string;
  sourceWorkspaceID: string;
  source: WorkflowTaskExecutionTargetSource;
  supportedSelections: readonly WorkflowTaskExecutionTargetSelectionMode[];
  configuredPolicy: WorkflowExecutionPolicy;
  recoveryCause: string | null;
}>;

export type WorkflowTaskExecutionTargetMaterializationProgress = Readonly<{
  taskID: string;
  phase: "materializing" | "recovery_queued" | "recovering";
}>;

export type WorkflowTaskExecutionTargetNegotiationConflict = Readonly<{
  taskID: string;
}>;
