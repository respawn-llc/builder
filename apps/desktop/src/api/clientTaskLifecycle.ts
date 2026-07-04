import type { TaskMoveInput } from "./clientInputs";
import { parseRpcResponse } from "./clientParse";
import { compactJsonObject } from "./json";
import type { TaskMoveResponse } from "./models";
import { taskMoveResponseSchema } from "./schemas/workflowBoard";
import { newSetupOperationID, type SetupOperationID } from "./setupOperationID";
import type { RpcTransport } from "./transport";

export async function startTask(
  transport: RpcTransport,
  taskID: string,
  setupOperationID: SetupOperationID = newSetupOperationID(),
): Promise<void> {
  await transport.call(
    "workflow.task.start",
    { task_id: taskID, setup_operation_id: setupOperationID.toJSONValue() },
    { timeoutMs: null },
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
        output_values: input.outputValues ?? {},
        allow_missing_edge: input.allowMissingEdge,
        auto_approve: input.autoApprove,
        setup_operation_id: (input.setupOperationID ?? newSetupOperationID()).toJSONValue(),
      }),
      { timeoutMs: null },
    ),
  );
  if (response.approvalError.length > 0) {
    throw new Error(response.approvalError);
  }
  return response;
}

export async function approveTransition(
  transport: RpcTransport,
  taskTransitionID: string,
  setupOperationID: SetupOperationID = newSetupOperationID(),
): Promise<void> {
  await transport.call(
    "workflow.task.approve",
    { task_transition_id: taskTransitionID, setup_operation_id: setupOperationID.toJSONValue() },
    { timeoutMs: null },
  );
}
