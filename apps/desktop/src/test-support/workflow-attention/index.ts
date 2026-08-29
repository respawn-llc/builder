import type { JsonValue } from "@/api";

export const workflowAttentionRpcMethods = {
  list: "workflow.attention.list",
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
