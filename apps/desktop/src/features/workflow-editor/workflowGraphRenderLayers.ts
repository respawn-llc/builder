import type { Node } from "@xyflow/react";

import type { WorkflowGraphEdge, WorkflowGraphNode } from "./workflowGraphLayout";
import type { WorkflowGraphSelection } from "./workflowGraphSelection";
import { workflowGraphLayerClassNames } from "./workflowGraphZOrder";

export function workflowGraphRenderNodes(
  nodes: readonly WorkflowGraphNode[],
  selection: WorkflowGraphSelection | null = null,
): Node[] {
  return nodes.map((node) => ({
    ...node,
    className: workflowGraphLayerClassName(node.className, workflowGraphNodeLayerClassName(node)),
    selected:
      selection?.kind === "node" && node.data.entityKind === "node"
        ? selection.nodeID === node.data.entityID
        : false,
  }));
}

export function workflowGraphRenderEdges(
  edges: readonly WorkflowGraphEdge[],
  selection: WorkflowGraphSelection | null = null,
): WorkflowGraphEdge[] {
  return edges.map((edge) => ({
    ...edge,
    className: workflowGraphLayerClassName(edge.className, workflowGraphLayerClassNames.edge),
    selected: selection?.kind === "edge" && selection.edgeID === edge.data?.entityID,
  }));
}

function workflowGraphNodeLayerClassName(node: WorkflowGraphNode): string {
  return node.data.entityKind === "group"
    ? workflowGraphLayerClassNames.group
    : workflowGraphLayerClassNames.node;
}

function workflowGraphLayerClassName(existing: string | undefined, layer: string): string {
  return existing === undefined || existing.length === 0 ? layer : `${existing} ${layer}`;
}
