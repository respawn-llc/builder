export type WorkflowExecutionTargetMode =
  "none" | "head" | "default_branch" | "custom_ref" | "ask_on_first_execution";

export type WorkflowExecutionTargetPolicy = Readonly<{
  mode: WorkflowExecutionTargetMode;
  customRef: string | null;
}>;

export type WorkflowExecutionTargetSelectionMode = Exclude<
  WorkflowExecutionTargetMode,
  "ask_on_first_execution"
>;

export type WorkflowExecutionTargetSelection = Readonly<{
  mode: WorkflowExecutionTargetSelectionMode;
  customRef: string | null;
}>;

export type WorkflowExecutionTargetUnavailableCause =
  "invalid_revision" | "non_commit" | "default_branch_missing" | "default_branch_ambiguous" | "git_failure";

export type WorkflowExecutionTargetSelectionRequirement =
  | Readonly<{
      reason: "policy_requires_selection";
    }>
  | Readonly<{
      reason: "configured_target_unavailable";
      configuredTarget: Readonly<{
        mode: Exclude<WorkflowExecutionTargetSelectionMode, "none">;
        requestedRef: string | null;
      }>;
      unavailableCause: WorkflowExecutionTargetUnavailableCause;
    }>;

export type WorkflowExecutionTargetProvenance = "resolved" | "legacy_observed";

export type WorkflowNoManagedExecutionTarget = Readonly<{
  mode: "none";
  requestedRef: null;
  resolvedRef: null;
  commitOID: null;
  provenance: "resolved";
}>;

export type WorkflowManagedExecutionTarget = Readonly<{
  mode: Exclude<WorkflowExecutionTargetSelectionMode, "none">;
  requestedRef: string;
  resolvedRef: string | null;
  commitOID: string;
  provenance: WorkflowExecutionTargetProvenance;
}>;

export type WorkflowExecutionTarget = WorkflowNoManagedExecutionTarget | WorkflowManagedExecutionTarget;

export const defaultWorkflowExecutionTargetPolicy: WorkflowExecutionTargetPolicy = {
  mode: "ask_on_first_execution",
  customRef: null,
};

import type { TaskCurrentNode } from "./models";

export type TaskStartApplied = Readonly<{ currentNodes: readonly TaskCurrentNode[] }>;
export type TaskResumeApplied = Readonly<{ currentNodes: readonly TaskCurrentNode[] }>;

export type TaskMoveApplied = Readonly<{ currentNodes: readonly TaskCurrentNode[] }>;
export type TaskMoveNoOp = Readonly<{ currentNodes: readonly TaskCurrentNode[] }>;

export type TaskApproveApplied = Readonly<{ taskID: string; currentNodes: readonly TaskCurrentNode[] }>;

export type WorkflowExecutionTargetActionResponse<TApplied> =
  | Readonly<{
      outcome: "applied";
      applied: TApplied;
    }>
  | Readonly<{
      outcome: "selection_required";
      selectionRequired: WorkflowExecutionTargetSelectionRequirement;
    }>
  | Readonly<{
      outcome: "dependency_confirmation_required";
      unsatisfiedDependencyCount: number;
    }>;

export type TaskStartResponse = WorkflowExecutionTargetActionResponse<TaskStartApplied>;
export type TaskResumeResponse =
  | Readonly<{ outcome: "applied"; applied: TaskResumeApplied }>
  | Readonly<{
      outcome: "selection_required";
      selectionRequired: WorkflowExecutionTargetSelectionRequirement;
    }>;
export type TaskMoveResponse =
  | WorkflowExecutionTargetActionResponse<TaskMoveApplied>
  | Readonly<{ outcome: "no_op"; noOp: TaskMoveNoOp }>;
export type TaskApproveResponse = WorkflowExecutionTargetActionResponse<TaskApproveApplied>;

export type TaskMovePreviewBlocker =
  | "invalid_workflow"
  | "no_source_position"
  | "unsupported_destination"
  | "waiting_question"
  | "lifecycle_conflict"
  | "context_session_unavailable"
  | "no_usable_transition"
  | "parallel_branch_requires_fan_out";

export type TaskMoveRequiredValue = Readonly<{
  nodeKey: string;
  outputName: string;
  description: string | null;
  resolvedValue: string | null;
}>;

export type TaskMovePreviewChoice = Readonly<{
  transitionKey: string;
  label: string;
  sourceNodeDisplayName: string;
  requiredValues: readonly TaskMoveRequiredValue[];
}>;

export type TaskMovePreviewResponse =
  | Readonly<{ outcome: "no_op"; noOp: TaskMoveNoOp }>
  | Readonly<{ outcome: "direct"; direct: Readonly<Record<string, never>> }>
  | Readonly<{
      outcome: "transition";
      transition: Readonly<{ choices: readonly TaskMovePreviewChoice[] }>;
    }>
  | Readonly<{ outcome: "blocked"; blocked: Readonly<{ reason: TaskMovePreviewBlocker }> }>;
