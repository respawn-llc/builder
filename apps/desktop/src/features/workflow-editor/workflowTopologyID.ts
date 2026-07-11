export type WorkflowTopologyIDKind = "node" | "edge" | "transitionGroup" | "nodeGroup";

const topologyIDPrefixes: Readonly<Record<WorkflowTopologyIDKind, string>> = {
  edge: "workflow-edge",
  node: "workflow-node",
  nodeGroup: "workflow-node-group",
  transitionGroup: "workflow-transition-group",
};

export function newWorkflowTopologyID(kind: WorkflowTopologyIDKind): string {
  return workflowTopologyIDFromUUID(kind, randomUUID());
}

export function workflowTopologyIDFromUUID(kind: WorkflowTopologyIDKind, uuid: string): string {
  const normalized = uuid.trim().toLowerCase();
  if (normalized.length === 0) {
    throw new Error("workflow topology id uuid is required");
  }
  return `${topologyIDPrefixes[kind]}-${normalized}`;
}

function randomUUID(): string {
  return randomUUIDFrom(globalThis.crypto);
}

function randomUUIDFrom(crypto: Partial<Pick<Crypto, "randomUUID">>): string {
  if (crypto.randomUUID !== undefined) {
    return crypto.randomUUID();
  }
  throw new Error("crypto.randomUUID is required to create workflow topology ids");
}
