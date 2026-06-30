import type { WorkflowGraphNode, WorkflowGraphPoint } from "../../features/workflow-editor/workflowGraphLayout";

type WorkflowGraphEndpointSide = "source" | "target";

type WorkflowGraphEndpointPort = Readonly<{
  id: string;
  side: WorkflowGraphEndpointSide;
  y: number;
}>;

type WorkflowGraphNodeAbsoluteRect = Readonly<{
  height: number;
  width: number;
  x: number;
  y: number;
}>;

export function workflowGraphEndpointPoint(
  node: WorkflowGraphNode,
  handleID: string | null | undefined,
  side: WorkflowGraphEndpointSide,
  nodes: readonly WorkflowGraphNode[],
): WorkflowGraphPoint {
  const rect = workflowGraphAbsoluteNodeRect(node, nodes);
  const port = workflowGraphEndpointPort(node, handleID, side);
  return {
    x: side === "source" ? rect.x + rect.width : rect.x,
    y: rect.y + port.y,
  };
}

export function workflowGraphAbsoluteNodeRect(
  node: WorkflowGraphNode,
  nodes: readonly WorkflowGraphNode[],
): WorkflowGraphNodeAbsoluteRect {
  const parent = node.parentId === undefined ? undefined : workflowGraphNodeByID(nodes, node.parentId);
  const parentRect = parent === undefined ? { x: 0, y: 0 } : workflowGraphAbsoluteNodeRect(parent, nodes);
  return {
    height: Number(node.style?.height ?? 0),
    width: Number(node.style?.width ?? 0),
    x: parentRect.x + node.position.x,
    y: parentRect.y + node.position.y,
  };
}

function workflowGraphEndpointPort(
  node: WorkflowGraphNode,
  handleID: string | null | undefined,
  side: WorkflowGraphEndpointSide,
): WorkflowGraphEndpointPort {
  if (typeof handleID !== "string" || node.data.entityKind !== "node") {
    throw new Error(`Endpoint port ${handleID ?? ""} not found for ${node.id}`);
  }
  const ports: unknown = node.data.endpointPorts;
  if (!Array.isArray(ports)) {
    throw new Error(`Endpoint port ${handleID} not found for ${node.id}`);
  }
  const port = ports.filter(isWorkflowGraphEndpointPort).find((item) => item.id === handleID && item.side === side);
  if (port === undefined) {
    throw new Error(`Endpoint port ${handleID} not found for ${node.id}`);
  }
  return port;
}

function workflowGraphNodeByID(nodes: readonly WorkflowGraphNode[], id: string): WorkflowGraphNode {
  const node = nodes.find((item) => item.id === id);
  if (node === undefined) {
    throw new Error(`Node ${id} not found`);
  }
  return node;
}

function isWorkflowGraphEndpointPort(value: unknown): value is WorkflowGraphEndpointPort {
  if (!isRecord(value)) {
    return false;
  }
  return (
    typeof value.id === "string" &&
    (value.side === "source" || value.side === "target") &&
    typeof value.y === "number"
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
