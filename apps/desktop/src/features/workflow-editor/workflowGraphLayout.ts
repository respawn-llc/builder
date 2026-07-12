import { MarkerType, type Edge, type Node } from "@xyflow/react";
import ELK from "elkjs/lib/elk.bundled.js";
import type { ElkNode } from "elkjs/lib/elk-api";

import type { WorkflowDefinition, WorkflowNodeKind, WorkflowValidation } from "../../api";
import { workflowEdgeColor } from "./workflowGraphColors";
import { visibleWorkflowGraphEdgeModels, type WorkflowGraphEdgeModel } from "./workflowGraphEdges";
import {
  absoluteLayoutOffsetByID,
  layoutEdgeByID,
  workflowGraphEdgeRoutePoints,
} from "./workflowGraphLayoutEdgeRoutes";
import {
  emptyGroupHeight,
  emptyGroupWidth,
  workflowEndpointPortSize,
  type WorkflowGraphEndpointPort,
  workflowJoinNodeSize,
  workflowNodeHeight,
  workflowNodeWidth,
} from "./workflowGraphLayoutGeometry";
import { isWorkflowGroupIslandMember, layoutWorkflowGraphNodes } from "./workflowGraphLayoutNodes";

export type WorkflowGraphNodeData = Readonly<{
  [key: string]: unknown;
  entityID: string;
  entityKind: "node";
  groupID: string;
  endpointPorts?: readonly WorkflowGraphEndpointPort[] | undefined;
  creationHandleID?: string | undefined;
  key: string;
  kind: WorkflowNodeKind;
  label: string;
  role: string;
  hasError: boolean;
}>;

export type WorkflowGraphGroupData = Readonly<{
  [key: string]: unknown;
  entityID: string;
  entityKind: "group";
  kind: "group";
  label: string;
  empty: boolean;
  hasError: boolean;
}>;

export type WorkflowGraphEdgeData = Readonly<{
  [key: string]: unknown;
  contextMode: string;
  entityID: string;
  entityKind: "edge";
  label: string;
  hasError: boolean;
  routePoints: readonly WorkflowGraphPoint[];
  transitionGroupID: string;
}>;

export type WorkflowGraphPoint = Readonly<{ x: number; y: number }>;

type WorkflowGraphRenderClassName = Readonly<{
  className?: string | undefined;
}>;

type WorkflowGraphElkPort = Readonly<{
  id: string;
  width: number;
  height: number;
  x: number;
  y: number;
  layoutOptions: Record<string, string>;
}>;

export type WorkflowGraphNode = Node<WorkflowGraphNodeData | WorkflowGraphGroupData> &
  WorkflowGraphRenderClassName;
export type WorkflowGraphWorkflowNode = Node<WorkflowGraphNodeData> & WorkflowGraphRenderClassName;
export type WorkflowGraphGroupNode = Node<WorkflowGraphGroupData> & WorkflowGraphRenderClassName;
export type WorkflowGraphEdge = Edge<WorkflowGraphEdgeData> & WorkflowGraphRenderClassName;

export type WorkflowGraphLayout = Readonly<{
  nodes: readonly WorkflowGraphNode[];
  edges: readonly WorkflowGraphEdge[];
}>;

const elk = new ELK();

export async function layoutWorkflowGraph(
  definition: WorkflowDefinition,
  validation: WorkflowValidation,
): Promise<WorkflowGraphLayout> {
  const errorMarkers = validationMarkers(validation);
  const transitionGroupsByID = new Map(definition.transitionGroups.map((group) => [group.id, group]));
  const visibleNodes = definition.nodes;
  const edgeModels = visibleWorkflowGraphEdgeModels(definition, transitionGroupsByID, errorMarkers);
  const groupedNodeIDs = new Set(visibleNodes.filter(isWorkflowGroupIslandMember).map((node) => node.id));
  const populatedGroupIDs = new Set(
    visibleNodes.filter(isWorkflowGroupIslandMember).map((node) => node.groupID),
  );
  const emptyGroups = definition.nodeGroups.filter((group) => !populatedGroupIDs.has(group.id));
  const endpointPortsByNodeID = workflowGraphEndpointPortsByNodeID(edgeModels);
  const children: ElkNode[] = [
    ...visibleNodes
      .filter((node) => !groupedNodeIDs.has(node.id))
      .map((node) => elkWorkflowNode(node, endpointPortsByNodeID.get(node.id) ?? [])),
    ...definition.nodeGroups
      .filter((group) => populatedGroupIDs.has(group.id))
      .map((group) => elkWorkflowGroupNode(group, visibleNodes, endpointPortsByNodeID)),
    ...emptyGroups.map((group) => ({
      id: groupNodeID(group.id),
      width: emptyGroupWidth,
      height: emptyGroupHeight,
    })),
  ];
  const graph: ElkNode = {
    id: "workflow-root",
    children,
    edges: edgeModels.map((edge) => edge.elk),
    layoutOptions: {
      "elk.algorithm": "layered",
      "elk.direction": "RIGHT",
      "elk.edgeRouting": "ORTHOGONAL",
      "elk.layered.considerModelOrder.strategy": "PREFER_NODES",
      "elk.layered.cycleBreaking.strategy": "DFS_NODE_ORDER",
      "elk.layered.feedbackEdges": "true",
      "elk.spacing.edgeEdge": "28",
      "elk.spacing.edgeNode": "44",
      "elk.spacing.nodeNode": "80",
      "elk.layered.spacing.edgeEdgeBetweenLayers": "28",
      "elk.layered.spacing.edgeNodeBetweenLayers": "56",
      "elk.layered.spacing.nodeNodeBetweenLayers": "120",
      "elk.hierarchyHandling": "INCLUDE_CHILDREN",
    },
  };
  const result = await elk.layout(graph);
  const nodeLayout = layoutWorkflowGraphNodes(result, definition, errorMarkers, endpointPortsByNodeID);
  const edgeLayoutByID = layoutEdgeByID(result);
  const containerOffsetByID = absoluteLayoutOffsetByID(result);
  const edges = edgeModels.map(
    (model): WorkflowGraphEdge => ({
      id: model.id,
      source: model.sourceNodeID,
      sourceHandle: model.sourcePort.id,
      target: model.targetNodeID,
      targetHandle: model.targetPort.id,
      type: "workflow",
      reconnectable: true,
      markerEnd: { color: workflowEdgeColor(model.contextMode, model.hasError), type: MarkerType.ArrowClosed },
      data: {
        label: model.label,
        contextMode: model.contextMode,
        entityID: model.edgeID,
        entityKind: "edge",
        hasError: model.hasError,
        routePoints: workflowGraphEdgeRoutePoints(model, edgeLayoutByID.get(model.id), {
          containerOffsetByID,
          groupNodeByGroupID: nodeLayout.groupNodeByGroupID,
          rectByNodeID: nodeLayout.rectByNodeID,
          alignedJoinNodeIDs: nodeLayout.alignedJoinNodeIDs,
        }),
        transitionGroupID: model.transitionGroupID,
      },
    }),
  );
  return { nodes: nodeLayout.nodes, edges };
}

export function workflowGraphLayoutWithDraftProjection(
  layout: WorkflowGraphLayout,
  definition: WorkflowDefinition,
  validation: WorkflowValidation,
): WorkflowGraphLayout {
  const errorMarkers = validationMarkers(validation);
  const nodesByID = new Map(definition.nodes.map((node) => [node.id, node]));
  const groupsByGraphID = new Map(definition.nodeGroups.map((group) => [groupNodeID(group.id), group]));
  const transitionGroupsByID = new Map(definition.transitionGroups.map((group) => [group.id, group]));
  const edgeModelsByID = new Map(
    visibleWorkflowGraphEdgeModels(definition, transitionGroupsByID, errorMarkers).map((model) => [
      model.edgeID,
      model,
    ]),
  );
  return {
    // Entities deleted in the draft no longer resolve to a model; drop them
    // during projection instead of carrying the stale layout node/edge forward
    // (otherwise a deleted node/group/edge stays visible and selectable until
    // the next ELK layout result, or indefinitely if layout fails).
    nodes: layout.nodes.flatMap<WorkflowGraphNode>((node) => {
      if (node.data.entityKind === "node") {
        const model = nodesByID.get(node.data.entityID);
        if (model === undefined) {
          return [];
        }
        // A draft change to group membership leaves the laid-out node's
        // structural fields (parentId/extent and the position they imply)
        // stale; we cannot recompute relative geometry without ELK. Drop the
        // node until the next layout rather than rendering it in the wrong
        // container, mirroring the deleted-entity drop above.
        // Join nodes are positioned as root-level siblings of their group even
        // when they carry a groupID, so their React Flow parentId is always undefined.
        const expectedParentID =
          model.groupID && model.kind !== "join" ? groupNodeID(model.groupID) : undefined;
        if (node.parentId !== expectedParentID) {
          return [];
        }
        return [
          {
            ...node,
            data: {
              ...node.data,
              groupID: model.groupID,
              hasError: errorMarkers.nodeIDs.has(model.id) || errorMarkers.relatedIDs.has(model.id),
              key: model.key,
              kind: model.kind,
              label: model.name,
              role: model.subagentRole,
            },
          },
        ];
      }
      const group = groupsByGraphID.get(node.id);
      if (group === undefined) {
        return [];
      }
      return [
        {
          ...node,
          data: {
            ...node.data,
            hasError: errorMarkers.relatedIDs.has(group.id),
            label: group.name || group.key,
          },
        },
      ];
    }),
    edges: layout.edges.flatMap<WorkflowGraphEdge>((edge) => {
      const model = edgeModelsByID.get(edge.id);
      if (model === undefined) {
        return [];
      }
      if (edge.data === undefined) {
        return [edge];
      }
      // Reconnect endpoints from the draft model so a re-targeted edge attaches
      // to the correct nodes/handles immediately. Clear stale route points when
      // endpoints changed — the old geometry no longer connects the new handles
      // and would be used by workflowEdgePath until ELK relayouts.
      const endpointsChanged =
        edge.source !== model.sourceNodeID ||
        edge.sourceHandle !== model.sourcePort.id ||
        edge.target !== model.targetNodeID ||
        edge.targetHandle !== model.targetPort.id;
      return [
        {
          ...edge,
          source: model.sourceNodeID,
          sourceHandle: model.sourcePort.id,
          target: model.targetNodeID,
          targetHandle: model.targetPort.id,
          markerEnd: { color: workflowEdgeColor(model.contextMode, model.hasError), type: MarkerType.ArrowClosed },
          data: {
            ...edge.data,
            contextMode: model.contextMode,
            hasError: model.hasError,
            label: model.label,
            transitionGroupID: model.transitionGroupID,
            ...(endpointsChanged ? { routePoints: [] } : undefined),
          },
        },
      ];
    }),
  };
}

function elkWorkflowNode(
  node: WorkflowDefinition["nodes"][number],
  endpointPorts: readonly WorkflowGraphEndpointPort[],
): ElkNode {
  const width = node.kind === "join" ? workflowJoinNodeSize : workflowNodeWidth;
  const height = node.kind === "join" ? workflowJoinNodeSize : workflowNodeHeight;
  return {
    id: node.id,
    width,
    height,
    ...(endpointPorts.length === 0
      ? {}
      : {
          layoutOptions: { "elk.portConstraints": "FIXED_POS" },
          ports: endpointPorts.map((port) => elkEndpointPort(port, width)),
        }),
  };
}

function elkWorkflowGroupNode(
  group: WorkflowDefinition["nodeGroups"][number],
  nodes: readonly WorkflowDefinition["nodes"][number][],
  endpointPortsByNodeID: ReadonlyMap<string, readonly WorkflowGraphEndpointPort[]>,
): ElkNode {
  return {
    id: groupNodeID(group.id),
    children: nodes
      .filter((node) => node.groupID === group.id && isWorkflowGroupIslandMember(node))
      .map((node) => elkWorkflowNode(node, endpointPortsByNodeID.get(node.id) ?? [])),
    layoutOptions: {
      "elk.algorithm": "layered",
      "elk.direction": "RIGHT",
      "elk.padding": "[top=56,left=24,bottom=24,right=24]",
      "elk.spacing.edgeNode": "44",
      "elk.spacing.nodeNode": "80",
      "elk.layered.spacing.nodeNodeBetweenLayers": "120",
    },
  };
}

function elkEndpointPort(port: WorkflowGraphEndpointPort, nodeWidth: number): WorkflowGraphElkPort {
  return {
    id: port.id,
    width: workflowEndpointPortSize,
    height: workflowEndpointPortSize,
    x:
      port.side === "source"
        ? nodeWidth - workflowEndpointPortSize / 2
        : -workflowEndpointPortSize / 2,
    y: port.y - workflowEndpointPortSize / 2,
    layoutOptions: { "elk.port.side": port.side === "source" ? "EAST" : "WEST" },
  };
}

function workflowGraphEndpointPortsByNodeID(
  edgeModels: readonly WorkflowGraphEdgeModel[],
): ReadonlyMap<string, readonly WorkflowGraphEndpointPort[]> {
  const portsByNodeID = new Map<string, WorkflowGraphEndpointPort[]>();
  for (const edge of edgeModels) {
    appendEndpointPort(portsByNodeID, edge.sourcePort);
    appendEndpointPort(portsByNodeID, edge.targetPort);
  }
  return portsByNodeID;
}

function appendEndpointPort(
  portsByNodeID: Map<string, WorkflowGraphEndpointPort[]>,
  port: WorkflowGraphEndpointPort,
): void {
  const ports = portsByNodeID.get(port.nodeID);
  if (ports === undefined) {
    portsByNodeID.set(port.nodeID, [port]);
    return;
  }
  ports.push(port);
}

function groupNodeID(id: string): string {
  return `workflow-group-${id}`;
}

function validationMarkers(validation: WorkflowValidation) {
  return {
    edgeIDs: new Set(
      validation.errors.flatMap((error) => [error.edgeID, error.details.providerEdgeID]).filter(nonEmpty),
    ),
    nodeIDs: new Set(validation.errors.map((error) => error.nodeID).filter(nonEmpty)),
    relatedIDs: new Set(validation.errors.flatMap((error) => error.relatedIDs).filter(nonEmpty)),
    transitionGroupIDs: new Set(validation.errors.map((error) => error.transitionGroupID).filter(nonEmpty)),
  };
}

function nonEmpty(value: string): boolean {
  return value.length > 0;
}
