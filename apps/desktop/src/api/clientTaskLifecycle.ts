import type { TaskMoveInput, TaskResumeInput, TaskStartInput } from "./clientInputs";
import { parseRpcResponse } from "./clientParse";
import { compactJsonObject } from "./json";
import type {
  TaskApproveResponse,
  TaskMoveResponse,
  TaskResumeResponse,
  TaskMovePreviewResponse,
  TaskStartResponse,
  WorkflowExecutionTargetSelection,
} from "./models";
import {
  taskApproveResponseSchema,
  taskMoveResponseSchema,
  taskResumeResponseSchema,
  taskMovePreviewResponseSchema,
  taskStartResponseSchema,
} from "./schemas/workflowBoard";
import { newSetupOperationID } from "./setupOperationID";
import type { RpcTransport } from "./transport";

export async function startTask(transport: RpcTransport, input: TaskStartInput): Promise<TaskStartResponse> {
  const setupOperationID = input.setupOperationID ?? newSetupOperationID();
  return parseRpcResponse(
    "workflow.task.start",
    taskStartResponseSchema,
    await transport.call(
      "workflow.task.start",
      compactJsonObject({
        task_id: input.taskID,
        setup_operation_id: setupOperationID.toJSONValue(),
        execution_target: executionTargetPayload(input.executionTarget),
        proceed_despite_dependencies: input.proceedDespiteDependencies ?? false,
      }),
      { timeoutMs: null },
    ),
  );
}

export async function moveTask(transport: RpcTransport, input: TaskMoveInput): Promise<TaskMoveResponse> {
  const response = parseRpcResponse(
    "workflow.task.move",
    taskMoveResponseSchema,
    await transport.call(
      "workflow.task.move",
      compactJsonObject({
        task_id: input.taskID,
        target_node_id: input.targetNodeID,
        transition_key: input.transitionKey,
        values: input.values,
        setup_operation_id: (input.setupOperationID ?? newSetupOperationID()).toJSONValue(),
        execution_target: executionTargetPayload(input.executionTarget),
        proceed_despite_dependencies: input.proceedDespiteDependencies ?? false,
      }),
      { timeoutMs: null },
    ),
  );
  return response;
}

export async function previewMoveTask(
  transport: RpcTransport,
  taskID: string,
  targetNodeID: string,
): Promise<TaskMovePreviewResponse> {
  return parseRpcResponse(
    "workflow.task.move.preview",
    taskMovePreviewResponseSchema,
    await transport.call(
      "workflow.task.move.preview",
      { task_id: taskID, target_node_id: targetNodeID },
      { timeoutMs: null },
    ),
  );
}

export async function approveApproval(
  transport: RpcTransport,
  approvalID: string,
): Promise<TaskApproveResponse> {
  return parseRpcResponse(
    "workflow.task.approve",
    taskApproveResponseSchema,
    await transport.call("workflow.task.approve", { approval_id: approvalID }, { timeoutMs: null }),
  );
}

export async function resumeTask(
  transport: RpcTransport,
  input: TaskResumeInput,
): Promise<TaskResumeResponse> {
  const setupOperationID = input.setupOperationID ?? newSetupOperationID();
  return parseRpcResponse(
    "workflow.task.resume",
    taskResumeResponseSchema,
    await transport.call(
      "workflow.task.resume",
      compactJsonObject({
        task_id: input.taskID,
        setup_operation_id: setupOperationID.toJSONValue(),
        execution_target: executionTargetPayload(input.executionTarget),
      }),
      { timeoutMs: null },
    ),
  );
}

function executionTargetPayload(selection: WorkflowExecutionTargetSelection | undefined) {
  if (selection === undefined) {
    return undefined;
  }
  return compactJsonObject({
    mode: selection.mode,
    custom_ref: selection.customRef ?? undefined,
  });
}
