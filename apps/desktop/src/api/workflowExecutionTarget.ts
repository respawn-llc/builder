export type WorkflowExecutionTargetMode =
  | "none"
  | "head"
  | "default_branch"
  | "custom_ref"
  | "ask_on_first_execution";

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
  | "invalid_revision"
  | "non_commit"
  | "default_branch_missing"
  | "default_branch_ambiguous"
  | "git_failure";

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
export type WorkflowExecutionTargetWorktreeAvailability =
  | "available"
  | "missing"
  | "inaccessible";

export type WorkflowExecutionTargetWorktree = Readonly<{
  id: string;
  displayName: string;
  canonicalRoot: string;
  availability: WorkflowExecutionTargetWorktreeAvailability;
}>;

export type WorkflowNoManagedExecutionTarget = Readonly<{
  mode: "none";
  effectiveRoot: string;
  requestedRef: null;
  resolvedRef: null;
  commitOID: null;
  provenance: "resolved";
  currentBranch: null;
  managedWorktree: null;
}>;

export type WorkflowManagedExecutionTarget = Readonly<{
  mode: Exclude<WorkflowExecutionTargetSelectionMode, "none">;
  effectiveRoot: string | null;
  requestedRef: string;
  resolvedRef: string | null;
  commitOID: string;
  provenance: WorkflowExecutionTargetProvenance;
  currentBranch: string | null;
  managedWorktree: WorkflowExecutionTargetWorktree | null;
}>;

export type WorkflowExecutionTarget =
  | WorkflowNoManagedExecutionTarget
  | WorkflowManagedExecutionTarget;

export const defaultWorkflowExecutionTargetPolicy: WorkflowExecutionTargetPolicy = {
  mode: "ask_on_first_execution",
  customRef: null,
};

export type TaskStartApplied = Readonly<{
  transitionID: string;
  placementID: string;
  runID: string;
}>;

export type TaskMoveApplied = Readonly<{
  transitionID: string;
  state: string;
  placementIDs: readonly string[];
  runIDs: readonly string[];
}>;

export type TaskApproveApplied = Readonly<{
  transitionID: string;
  taskID: string;
  state: string;
  placementIDs: readonly string[];
  runIDs: readonly string[];
}>;

export type WorkflowExecutionTargetActionResponse<TApplied> =
  | Readonly<{
      outcome: "applied";
      applied: TApplied;
    }>
  | Readonly<{
      outcome: "selection_required";
      selectionRequired: WorkflowExecutionTargetSelectionRequirement;
    }>;

export type TaskStartResponse = WorkflowExecutionTargetActionResponse<TaskStartApplied>;
export type TaskMoveResponse = WorkflowExecutionTargetActionResponse<TaskMoveApplied>;
export type TaskApproveResponse = WorkflowExecutionTargetActionResponse<TaskApproveApplied>;
