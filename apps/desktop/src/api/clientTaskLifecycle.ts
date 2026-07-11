import type { TaskApproveInput, TaskMoveInput, TaskStartInput } from "./clientInputs";
import { parseRpcResponse } from "./clientParse";
import { compactJsonObject } from "./json";
import type { WorkflowTaskInitiatingActionResult } from "./models";
import { workflowTaskInitiatingActionResultSchema } from "./schemas/workflowLifecycle";
import { newSetupOperationID } from "./setupOperationID";
import type { RpcTransport } from "./transport";

export async function startTask(
  transport: RpcTransport,
  input: TaskStartInput,
): Promise<WorkflowTaskInitiatingActionResult> {
  return parseRpcResponse(
    "workflow.task.start",
    workflowTaskInitiatingActionResultSchema,
    await transport.call(
      "workflow.task.start",
      taskInitiatingActionPayload({
        task_id: input.taskID,
        setup_operation_id: (input.setupOperationID ?? newSetupOperationID()).toJSONValue(),
        selection_generation: input.selectionGeneration,
        selection: input.selection,
      }),
      { timeoutMs: null },
    ),
  );
}

export async function moveTask(
  transport: RpcTransport,
  input: TaskMoveInput,
): Promise<WorkflowTaskInitiatingActionResult> {
  const response = parseRpcResponse(
    "workflow.task.move",
    workflowTaskInitiatingActionResultSchema,
    await transport.call(
      "workflow.task.move",
      taskInitiatingActionPayload({
        task_id: input.taskID,
        target_node_id: input.targetNodeID,
        output_values: input.outputValues ?? {},
        allow_missing_edge: input.allowMissingEdge,
        auto_approve: input.autoApprove,
        setup_operation_id: (input.setupOperationID ?? newSetupOperationID()).toJSONValue(),
        selection_generation: input.selectionGeneration,
        selection: input.selection,
      }),
      { timeoutMs: null },
    ),
  );
  if (response.outcome === "moved" && response.moved.approvalError.length > 0) {
    throw new Error(response.moved.approvalError);
  }
  return response;
}

export async function approveTransition(
  transport: RpcTransport,
  input: TaskApproveInput,
): Promise<WorkflowTaskInitiatingActionResult> {
  return parseRpcResponse(
    "workflow.task.approve",
    workflowTaskInitiatingActionResultSchema,
    await transport.call(
      "workflow.task.approve",
      taskInitiatingActionPayload({
        task_transition_id: input.taskTransitionID,
        setup_operation_id: (input.setupOperationID ?? newSetupOperationID()).toJSONValue(),
        selection_generation: input.selectionGeneration,
        selection: input.selection,
      }),
      { timeoutMs: null },
    ),
  );
}

function taskInitiatingActionPayload(
  value: Readonly<{
    setup_operation_id: string;
    selection_generation?: string | undefined;
    selection?: Readonly<{ mode: string; customRef: string | null }> | undefined;
    task_id?: string | undefined;
    task_transition_id?: string | undefined;
    target_node_id?: string | undefined;
    output_values?: Readonly<Record<string, string>> | undefined;
    allow_missing_edge?: boolean | undefined;
    auto_approve?: boolean | undefined;
  }>,
) {
  return compactJsonObject({
    ...value,
    selection:
      value.selection === undefined
        ? undefined
        : compactJsonObject({
            mode: value.selection.mode,
            custom_ref: value.selection.customRef ?? undefined,
          }),
  });
}
