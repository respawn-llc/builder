import type { WorkflowGraphNode } from "./workflowGraphLayout";
import { z } from "zod";

export type NodeLayoutOffset = Readonly<{ x: number; y: number }>;
const layoutNumberSchema = z.union([z.number(), z.string().transform((value) => Number.parseFloat(value))]);

export type WorkflowGraphNodeRect = Readonly<{
  groupID: string;
  height: number;
  kind: string;
  width: number;
  x: number;
  y: number;
}>;

export type WorkflowGraphEndpointPortSide = "source" | "target";

export type WorkflowGraphEndpointPort = Readonly<{
  id: string;
  nodeID: string;
  side: WorkflowGraphEndpointPortSide;
  y: number;
}>;

export const workflowNodeWidth = 220;
export const workflowNodeHeight = 92;
export const workflowJoinNodeSize = 56;
export const workflowJoinOutgoingRouteJog = workflowJoinNodeSize / 2;
export const workflowJoinGroupGap = 80;
export const emptyGroupWidth = 260;
export const emptyGroupHeight = 140;
export const workflowEndpointPortSize = 1;

const workflowEndpointPortPadding = 14;
const workflowCreationHandleSlotClearance = 14;

export function graphNodeWidth(node: WorkflowGraphNode): number {
  return positiveFiniteNumber(node.style?.width, workflowNodeWidth);
}

export function graphNodeHeight(node: WorkflowGraphNode): number {
  return positiveFiniteNumber(node.style?.height, workflowNodeHeight);
}

export function workflowGraphEndpointPortID(edgeID: string, side: WorkflowGraphEndpointPortSide): string {
  return `workflow-${side}-endpoint-${edgeID}`;
}

export function workflowGraphCreationHandleID(nodeID: string): string {
  return `workflow-create-transition-${nodeID}`;
}

export function workflowGraphTargetConnectionHandleID(nodeID: string): string {
  return `workflow-target-connection-${nodeID}`;
}

export function workflowGraphEndpointPortY(index: number, count: number, height: number): number {
  const resolvedHeight = positiveFiniteNumber(height, workflowNodeHeight);
  const resolvedCount = Math.max(1, count);
  const resolvedIndex = Math.min(Math.max(0, index), resolvedCount - 1);
  const centerY = resolvedHeight / 2;
  const topCount = Math.ceil(resolvedCount / 2);
  const bottomCount = resolvedCount - topCount;
  if (resolvedIndex < topCount) {
    return distributedSlotY(
      Math.min(workflowEndpointPortPadding, centerY),
      Math.max(workflowEndpointPortPadding, centerY - workflowCreationHandleSlotClearance),
      resolvedIndex,
      topCount,
    );
  }
  return distributedSlotY(
    Math.min(resolvedHeight - workflowEndpointPortPadding, centerY + workflowCreationHandleSlotClearance),
    Math.max(resolvedHeight - workflowEndpointPortPadding, centerY),
    resolvedIndex - topCount,
    bottomCount,
  );
}

function positiveFiniteNumber(value: unknown, fallback: number): number {
  const parsed = layoutNumberSchema.safeParse(value);
  return parsed.success && Number.isFinite(parsed.data) && parsed.data > 0 ? parsed.data : fallback;
}

function distributedSlotY(minY: number, maxY: number, index: number, count: number): number {
  if (count <= 0) {
    return minY;
  }
  const min = Math.min(minY, maxY);
  const max = Math.max(minY, maxY);
  return min + ((max - min) * (index + 1)) / (count + 1);
}
