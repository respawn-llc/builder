export type WorkflowTopologyIDKind = "node" | "edge" | "transitionGroup" | "nodeGroup";

export function newWorkflowTopologyID(_kind: WorkflowTopologyIDKind): string {
  return globalThis.crypto.randomUUID();
}
