import type { JsonValue } from "@/api";

export const workflowAttentionRpcMethods = {
  list: "workflow.attention.list",
  projectEvent: "workflow.project",
  subscribeProject: "workflow.subscribeProject",
} as const;

type RpcCallLog = Readonly<{
  method: string;
  params: JsonValue;
}>;

type RpcCallSource = Readonly<{
  calls: readonly RpcCallLog[];
}>;

export function workflowAttentionCalls(transport: RpcCallSource): readonly RpcCallLog[] {
  return transport.calls.filter((call) => call.method === workflowAttentionRpcMethods.list);
}

export function workflowAttentionCallCount(transport: RpcCallSource): number {
  return workflowAttentionCalls(transport).length;
}
