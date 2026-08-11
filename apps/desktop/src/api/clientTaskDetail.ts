import { parseRpcResponse } from "./clientParse";
import { requireTaskBoundItems } from "./clientParse";
import type { ActivityPage, CommentPage, PendingAsk, TaskAttention, TaskComment, TaskDetail } from "./models";
import {
  activityPageSchema,
  commentAddResponseSchema,
  commentPageSchema,
  pendingAskListSchema,
  taskAttentionSchema,
  taskDetailSchema,
} from "./schemas/workflowBoard";
import type { RpcTransport } from "./transport";

export async function listTaskAttention(transport: RpcTransport, taskID: string): Promise<TaskAttention> {
  const response = parseRpcResponse(
    "workflow.task.attention.list",
    taskAttentionSchema,
    await transport.call("workflow.task.attention.list", { task_id: taskID }),
  );
  requireTaskBoundItems(taskID, response.items);
  return response;
}

export async function getTask(transport: RpcTransport, taskID: string): Promise<TaskDetail> {
  return parseRpcResponse(
    "workflow.task.get",
    taskDetailSchema,
    await transport.call("workflow.task.get", { task_id: taskID }),
  );
}

export async function listTaskActivity(
  transport: RpcTransport,
  taskID: string,
  offset: number,
): Promise<ActivityPage> {
  const response = parseRpcResponse(
    "workflow.task.activity.list",
    activityPageSchema,
    await transport.call("workflow.task.activity.list", {
      task_id: taskID,
      offset,
      limit: 50,
    }),
  );
  requireTaskBoundItems(taskID, response.items);
  return response;
}

export async function listTaskComments(
  transport: RpcTransport,
  taskID: string,
  offset: number,
): Promise<CommentPage> {
  return parseRpcResponse(
    "workflow.task.comment.list",
    commentPageSchema,
    await transport.call("workflow.task.comment.list", {
      task_id: taskID,
      offset,
      limit: 50,
    }),
  );
}

export async function addComment(
  transport: RpcTransport,
  taskID: string,
  body: string,
  author: string,
): Promise<TaskComment> {
  return parseRpcResponse(
    "workflow.task.comment.add",
    commentAddResponseSchema,
    await transport.call("workflow.task.comment.add", {
      task_id: taskID,
      body,
      author,
    }),
  ).comment;
}

export async function listPendingAsks(
  transport: RpcTransport,
  sessionID: string,
): Promise<readonly PendingAsk[]> {
  return parseRpcResponse(
    "ask.listPendingBySession",
    pendingAskListSchema,
    await transport.callAttachedSession(sessionID, "ask.listPendingBySession", {
      SessionID: sessionID,
    }),
  );
}
