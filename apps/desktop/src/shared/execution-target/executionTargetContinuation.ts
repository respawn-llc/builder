import type {
  ApiService,
  TaskMoveInput,
  TaskMoveResponse,
  TaskResumeResponse,
  TaskStartResponse,
  WorkflowExecutionTargetSelection,
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
} from "@/api";
import { newSetupOperationID, type SetupOperationID } from "@/api";

export type TaskInitiatingAction =
  | Readonly<{
      kind: "start";
      taskID: string;
      setupOperationID: SetupOperationID;
      proceedDespiteDependencies: boolean;
    }>
  | Readonly<{
      kind: "move";
      input: TaskMoveInput;
    }>
  | Readonly<{
      kind: "resume";
      taskID: string;
      setupOperationID: SetupOperationID;
    }>;

export type TaskInitiatingActionResult =
  | Readonly<{
      kind: "start";
      action: Extract<TaskInitiatingAction, { kind: "start" }>;
      response: TaskStartResponse;
    }>
  | Readonly<{
      kind: "move";
      action: Extract<TaskInitiatingAction, { kind: "move" }>;
      response: TaskMoveResponse;
    }>
  | Readonly<{
      kind: "resume";
      action: Extract<TaskInitiatingAction, { kind: "resume" }>;
      response: TaskResumeResponse;
    }>;

export type ExecutionTargetSelectionDraft = Readonly<{
  mode: WorkflowExecutionTargetSelectionMode;
  customRef: string;
}>;

export function startTaskInitiatingAction(
  taskID: string,
  setupOperationID: SetupOperationID = newSetupOperationID(),
): Extract<TaskInitiatingAction, { kind: "start" }> {
  return {
    kind: "start",
    taskID,
    setupOperationID,
    proceedDespiteDependencies: false,
  };
}

export function moveTaskInitiatingAction(
  input: TaskMoveInput,
): Extract<TaskInitiatingAction, { kind: "move" }> {
  return { kind: "move", input };
}

export function resumeTaskInitiatingAction(
  taskID: string,
  setupOperationID: SetupOperationID = newSetupOperationID(),
): Extract<TaskInitiatingAction, { kind: "resume" }> {
  return { kind: "resume", taskID, setupOperationID };
}

export function proceedWithTaskInitiatingAction(action: TaskInitiatingAction): TaskInitiatingAction {
  if (action.kind === "start") {
    return { ...action, proceedDespiteDependencies: true };
  }
  if (action.kind === "resume") {
    return action;
  }
  return {
    ...action,
    input: { ...action.input, proceedDespiteDependencies: true },
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

export async function executeTaskInitiatingAction(
  api: ApiService,
  action: TaskInitiatingAction,
  selection?: WorkflowExecutionTargetSelection,
): Promise<TaskInitiatingActionResult> {
  switch (action.kind) {
    case "start":
      return {
        kind: action.kind,
        action,
        response: await api.startTask({
          taskID: action.taskID,
          setupOperationID: action.setupOperationID,
          executionTarget: selection,
          proceedDespiteDependencies: action.proceedDespiteDependencies,
        }),
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
    case "resume":
      return {
        kind: action.kind,
        action,
        response: await api.resumeTask({
          taskID: action.taskID,
          setupOperationID: action.setupOperationID,
          executionTarget: selection,
        }),
      };
  }
}

export function taskInitiatingActionTaskID(action: TaskInitiatingAction): string {
  return action.kind === "move" ? action.input.taskID : action.taskID;
}
