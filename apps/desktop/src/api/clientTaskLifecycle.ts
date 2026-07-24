import type { TaskMoveInput } from "./clientInputs";
import { parseRpcResponse } from "./clientParse";
import { compactJsonObject } from "./json";
import type {
  TaskApproveResponse,
  TaskMoveResponse,
  TaskStartResponse,
  WorkflowExecutionTargetSelection,
} from "./models";
import {
  taskApproveResponseSchema,
  taskMoveResponseSchema,
  taskStartResponseSchema,
} from "./schemas/workflowBoard";
import { newSetupOperationID, type SetupOperationID } from "./setupOperationID";
import type { RpcTransport } from "./transport";

export async function startTask(
  transport: RpcTransport,
  taskID: string,
  setupOperationID: SetupOperationID = newSetupOperationID(),
  executionTarget?: WorkflowExecutionTargetSelection,
): Promise<TaskStartResponse> {
  return parseRpcResponse(
    "workflow.task.start",
    taskStartResponseSchema,
    await transport.callLongRunning(
      "workflow.task.start",
      compactJsonObject({
        task_id: taskID,
        setup_operation_id: setupOperationID.toJSONValue(),
        execution_target: executionTargetPayload(executionTarget),
      }),
    ),
  );
}

export async function moveTask(transport: RpcTransport, input: TaskMoveInput): Promise<TaskMoveResponse> {
  const response = parseRpcResponse(
    "workflow.task.move",
    taskMoveResponseSchema,
    await transport.callLongRunning(
      "workflow.task.move",
      compactJsonObject({
        task_id: input.taskID,
        target_node_id: input.targetNodeID,
        output_values: input.outputValues ?? {},
        allow_missing_edge: input.allowMissingEdge,
        auto_approve: input.autoApprove,
        setup_operation_id: (input.setupOperationID ?? newSetupOperationID()).toJSONValue(),
        execution_target: executionTargetPayload(input.executionTarget),
      }),
    ),
  );
  return response;
}

export async function approveTransition(
  transport: RpcTransport,
  taskTransitionID: string,
  setupOperationID: SetupOperationID = newSetupOperationID(),
  executionTarget?: WorkflowExecutionTargetSelection,
): Promise<TaskApproveResponse> {
  return parseRpcResponse(
    "workflow.task.approve",
    taskApproveResponseSchema,
    await transport.callLongRunning(
      "workflow.task.approve",
      compactJsonObject({
        task_transition_id: taskTransitionID,
        setup_operation_id: setupOperationID.toJSONValue(),
        execution_target: executionTargetPayload(executionTarget),
      }),
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
