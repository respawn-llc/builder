import type {
  ApiService,
  TaskMoveInput,
  TaskMoveResponse,
  TaskStartResponse,
  WorkflowExecutionTargetSelection,
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
} from "@/api";
import { newSetupOperationID, type SetupOperationID } from "@/api";

export type ExecutionTargetContinuationAction =
  | Readonly<{
      kind: "start";
      taskID: string;
      setupOperationID: SetupOperationID;
    }>
  | Readonly<{
      kind: "move";
      input: TaskMoveInput & Readonly<{ setupOperationID: SetupOperationID }>;
    }>

export type ExecutionTargetActionResult =
  | Readonly<{
      kind: "start";
      action: Extract<ExecutionTargetContinuationAction, { kind: "start" }>;
      response: TaskStartResponse;
    }>
  | Readonly<{
      kind: "move";
      action: Extract<ExecutionTargetContinuationAction, { kind: "move" }>;
      response: TaskMoveResponse;
    }>

export type ExecutionTargetSelectionDraft = Readonly<{
  mode: WorkflowExecutionTargetSelectionMode;
  customRef: string;
}>;

export function startExecutionTargetAction(
  taskID: string,
  setupOperationID: SetupOperationID = newSetupOperationID(),
): Extract<ExecutionTargetContinuationAction, { kind: "start" }> {
  return { kind: "start", taskID, setupOperationID };
}

export function moveExecutionTargetAction(
  input: TaskMoveInput,
): Extract<ExecutionTargetContinuationAction, { kind: "move" }> {
  return {
    kind: "move",
    input: {
      ...input,
      setupOperationID: input.setupOperationID ?? newSetupOperationID(),
    },
  };
}

export function initialExecutionTargetSelectionDraft(
  requirement: WorkflowExecutionTargetSelectionRequirement,
): ExecutionTargetSelectionDraft {
  if (requirement.reason === "configured_target_unavailable") {
    return {
      mode: requirement.configuredTarget.mode,
      customRef: requirement.configuredTarget.requestedRef ?? "",
    };
  }
  return { mode: "default_branch", customRef: "" };
}

export function executionTargetSelectionFromDraft(
  draft: ExecutionTargetSelectionDraft,
): WorkflowExecutionTargetSelection | null {
  if (draft.mode !== "custom_ref") {
    return { mode: draft.mode, customRef: null };
  }
  const customRef = draft.customRef.trim();
  return customRef.length === 0 ? null : { mode: draft.mode, customRef };
}

export async function executeExecutionTargetAction(
  api: ApiService,
  action: ExecutionTargetContinuationAction,
  selection?: WorkflowExecutionTargetSelection,
): Promise<ExecutionTargetActionResult> {
  switch (action.kind) {
    case "start":
      return {
        kind: action.kind,
        action,
        response: await api.startTask(action.taskID, action.setupOperationID, selection),
      };
    case "move":
      return {
        kind: action.kind,
        action,
        response: await api.moveTask({
          ...action.input,
          executionTarget: selection,
        }),
      };
  }
}
