import type {
  WorkflowGraphEdge,
  WorkflowGraphNode,
  WorkflowGraphPoint,
} from "../../features/workflow-editor/workflowGraphLayout";
import { z } from "zod";

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

const endpointHandleIDSchema = z.string();
const endpointPortSchema = z.object({
  id: z.string(),
  side: z.union([z.literal("source"), z.literal("target")]),
  y: z.number(),
});

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
  const parent = node.parentId === undefined ? undefined : requireWorkflowGraphNode(nodes, node.parentId);
  const parentRect = parent === undefined ? { x: 0, y: 0 } : workflowGraphAbsoluteNodeRect(parent, nodes);
  return {
    height: Number(node.style?.height ?? 0),
    width: Number(node.style?.width ?? 0),
    x: parentRect.x + node.position.x,
    y: parentRect.y + node.position.y,
  };
}

export function requireWorkflowGraphNode(nodes: readonly WorkflowGraphNode[], id: string): WorkflowGraphNode {
  const node = nodes.find((item) => item.id === id);
  if (node === undefined) {
    throw new Error(`Node ${id} not found`);
  }
  return node;
}

export function requireWorkflowGraphEdge(edges: readonly WorkflowGraphEdge[], id: string): WorkflowGraphEdge {
  const edge = edges.find((item) => item.id === id);
  if (edge === undefined) {
    throw new Error(`Edge ${id} not found`);
  }
  return edge;
}

export function requireWorkflowGraphRoutePoints(edge: WorkflowGraphEdge): readonly WorkflowGraphPoint[] {
  const points = edge.data?.routePoints ?? [];
  if (points.length < 2) {
    throw new Error(`Edge ${edge.id} has no routed points`);
  }
  return points;
}

export function workflowGraphRouteIsOrthogonal(points: readonly WorkflowGraphPoint[]): boolean {
  return points.every((point, index) => {
    const previous = points[index - 1];
    return previous === undefined || point.x === previous.x || point.y === previous.y;
  });
}

export function workflowGraphRouteHasCorner(points: readonly WorkflowGraphPoint[]): boolean {
  return points.some((point, index) => {
    const previous = points[index - 1];
    const next = points[index + 1];
    return (
      previous !== undefined &&
      next !== undefined &&
      (previous.x - point.x) * (next.y - point.y) !== (previous.y - point.y) * (next.x - point.x)
    );
  });
}

function workflowGraphEndpointPort(
  node: WorkflowGraphNode,
  handleID: string | null | undefined,
  side: WorkflowGraphEndpointSide,
): WorkflowGraphEndpointPort {
  const endpointHandleID = endpointHandleIDSchema.safeParse(handleID);
  if (!endpointHandleID.success || node.data.entityKind !== "node") {
    throw new Error(`Endpoint port ${handleID ?? ""} not found for ${node.id}`);
  }
  const ports: unknown = node.data.endpointPorts;
  if (!Array.isArray(ports)) {
    throw new Error(`Endpoint port ${endpointHandleID.data} not found for ${node.id}`);
  }
  const port = ports
    .filter(isWorkflowGraphEndpointPort)
    .find((item) => item.id === endpointHandleID.data && item.side === side);
  if (port === undefined) {
    throw new Error(`Endpoint port ${endpointHandleID.data} not found for ${node.id}`);
  }
  return port;
}

function isWorkflowGraphEndpointPort(value: unknown): value is WorkflowGraphEndpointPort {
  return endpointPortSchema.safeParse(value).success;
}
