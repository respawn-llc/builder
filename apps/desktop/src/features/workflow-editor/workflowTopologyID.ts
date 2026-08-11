export type WorkflowTopologyIDKind = "node" | "edge" | "transitionGroup" | "nodeGroup";

export function newWorkflowTopologyID(_kind: WorkflowTopologyIDKind): string {
  void _kind;
  return globalThis.crypto.randomUUID();
}
