import { parseRpcResponse } from "./clientParse";
import { compactJsonObject } from "./json";
import type {
  TaskDependencyDirection,
  TaskDependencyListResponse,
  TaskDependencyMutationResponse,
} from "./models";
import {
  taskDependencyAddResponseSchema,
  taskDependencyListResponseSchema,
  taskDependencyRemoveResponseSchema,
} from "./schemas/workflowBoard";
import type { RpcTransport } from "./transport";

export async function addTaskDependency(
  transport: RpcTransport,
  blockerTaskID: string,
  blockedTaskID: string,
): Promise<TaskDependencyMutationResponse> {
  return parseRpcResponse(
    "workflow.task.dependency.add",
    taskDependencyAddResponseSchema,
    await transport.call("workflow.task.dependency.add", {
      blocker_task_id: blockerTaskID,
      blocked_task_id: blockedTaskID,
    }),
  );
}

export async function removeTaskDependency(
  transport: RpcTransport,
  blockerTaskID: string,
  blockedTaskID: string,
): Promise<TaskDependencyMutationResponse> {
  return parseRpcResponse(
    "workflow.task.dependency.remove",
    taskDependencyRemoveResponseSchema,
    await transport.call("workflow.task.dependency.remove", {
      blocker_task_id: blockerTaskID,
      blocked_task_id: blockedTaskID,
    }),
  );
}

export async function listTaskDependencies(
  transport: RpcTransport,
  taskID: string,
  direction?: TaskDependencyDirection,
): Promise<TaskDependencyListResponse> {
  return parseRpcResponse(
    "workflow.task.dependency.list",
    taskDependencyListResponseSchema,
    await transport.call("workflow.task.dependency.list", compactJsonObject({ task_id: taskID, direction })),
  );
}
