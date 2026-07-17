import type { ElkExtendedEdge } from "elkjs/lib/elk-api";

import type { WorkflowDefinition } from "@/api";
import {
  workflowGraphEndpointPortID,
  workflowGraphEndpointPortY,
  workflowJoinNodeSize,
  workflowNodeHeight,
  type WorkflowGraphEndpointPort,
  type WorkflowGraphEndpointPortSide,
} from "./workflowGraphLayoutGeometry";

export type WorkflowGraphEdgeErrorMarkers = Readonly<{
  edgeIDs: ReadonlySet<string>;
  transitionGroupIDs: ReadonlySet<string>;
}>;

export type WorkflowGraphEdgeModel = Readonly<{
  contextMode: string;
  edgeID: string;
  hasError: boolean;
  id: string;
  label: string;
  sourcePort: WorkflowGraphEndpointPort;
  sourceNodeID: string;
  targetPort: WorkflowGraphEndpointPort;
  targetNodeID: string;
  transitionGroupID: string;
  elk: ElkExtendedEdge;
}>;

type WorkflowGraphEdgeModelInput = Readonly<{
  contextMode: string;
  edgeID: string;
  hasError: boolean;
  label: string;
  preservesModelDirection: boolean;
  sourceNodeID: string;
  targetNodeID: string;
  transitionGroupID: string;
}>;

export function visibleWorkflowGraphEdgeModels(
  definition: WorkflowDefinition,
  transitionGroupsByID: ReadonlyMap<string, WorkflowDefinition["transitionGroups"][number]>,
  errorMarkers: WorkflowGraphEdgeErrorMarkers,
): readonly WorkflowGraphEdgeModel[] {
  const inputs: WorkflowGraphEdgeModelInput[] = [];
  const nodeOrderByID = new Map(definition.nodes.map((node, index) => [node.id, index]));
  for (const edge of definition.edges) {
    const group = transitionGroupsByID.get(edge.transitionGroupID);
    if (group === undefined) {
      continue;
    }
    const sourceOrder = nodeOrderByID.get(group.sourceNodeID) ?? 0;
    const targetOrder = nodeOrderByID.get(edge.targetNodeID) ?? sourceOrder;
    inputs.push({
      contextMode: edge.contextMode,
      edgeID: edge.id,
      hasError: edgeHasError(edge, group, errorMarkers),
      label: edgeLabel(edge.key, group, definition.edges),
      preservesModelDirection: sourceOrder <= targetOrder,
      sourceNodeID: group.sourceNodeID,
      targetNodeID: edge.targetNodeID,
      transitionGroupID: group.id,
    });
  }
  const nodeHeightByID = workflowNodeHeightByID(definition.nodes);
  const sourcePortByEdgeID = endpointPortByEdgeID(inputs, nodeHeightByID, "source");
  const targetPortByEdgeID = endpointPortByEdgeID(inputs, nodeHeightByID, "target");
  return inputs.map((input) =>
    workflowGraphEdgeModel({
      ...input,
      sourcePort:
        sourcePortByEdgeID.get(input.edgeID) ?? fallbackEndpointPort(input, "source", nodeHeightByID),
      targetPort:
        targetPortByEdgeID.get(input.edgeID) ?? fallbackEndpointPort(input, "target", nodeHeightByID),
    }),
  );
}

function workflowGraphEdgeModel(
  input: WorkflowGraphEdgeModelInput &
    Readonly<{ sourcePort: WorkflowGraphEndpointPort; targetPort: WorkflowGraphEndpointPort }>,
): WorkflowGraphEdgeModel {
  return {
    contextMode: input.contextMode,
    edgeID: input.edgeID,
    hasError: input.hasError,
    id: input.edgeID,
    label: input.label,
    sourcePort: input.sourcePort,
    sourceNodeID: input.sourceNodeID,
    targetPort: input.targetPort,
    targetNodeID: input.targetNodeID,
    transitionGroupID: input.transitionGroupID,
    elk: {
      id: input.edgeID,
      layoutOptions: workflowEdgeLayoutOptions(input.preservesModelDirection),
      sources: [input.sourcePort.id],
      targets: [input.targetPort.id],
    },
  };
}

function workflowEdgeLayoutOptions(preservesModelDirection: boolean): Record<string, string> {
  return preservesModelDirection
    ? {
        "elk.layered.priority.direction": "100",
        "elk.layered.priority.shortness": "20",
        "elk.layered.priority.straightness": "20",
      }
    : {
        "elk.layered.priority.direction": "0",
        "elk.layered.priority.shortness": "0",
        "elk.layered.priority.straightness": "0",
      };
}

function edgeHasError(
  edge: WorkflowDefinition["edges"][number],
  group: WorkflowDefinition["transitionGroups"][number],
  errorMarkers: WorkflowGraphEdgeErrorMarkers,
): boolean {
  return errorMarkers.edgeIDs.has(edge.id) || errorMarkers.transitionGroupIDs.has(group.id);
}

function edgeLabel(
  edgeKey: string,
  group: WorkflowDefinition["transitionGroups"][number],
  edges: WorkflowDefinition["edges"],
): string {
  const base = group.name.length > 0 ? group.name : group.transitionID;
  const groupEdgeCount = edges.filter((edge) => edge.transitionGroupID === group.id).length;
  return groupEdgeCount > 1 ? `${base} / ${edgeKey}` : base;
}

function workflowNodeHeightByID(
  nodes: readonly WorkflowDefinition["nodes"][number][],
): ReadonlyMap<string, number> {
  return new Map(
    nodes.map((node) => [node.id, node.kind === "join" ? workflowJoinNodeSize : workflowNodeHeight]),
  );
}

function endpointPortByEdgeID(
  inputs: readonly WorkflowGraphEdgeModelInput[],
  nodeHeightByID: ReadonlyMap<string, number>,
  side: WorkflowGraphEndpointPortSide,
): ReadonlyMap<string, WorkflowGraphEndpointPort> {
  const inputsByNodeID = new Map<string, WorkflowGraphEdgeModelInput[]>();
  for (const input of inputs) {
    const nodeID = endpointNodeID(input, side);
    const nodeInputs = inputsByNodeID.get(nodeID);
    if (nodeInputs === undefined) {
      inputsByNodeID.set(nodeID, [input]);
      continue;
    }
    nodeInputs.push(input);
  }
  const ports = new Map<string, WorkflowGraphEndpointPort>();
  for (const [nodeID, nodeInputs] of inputsByNodeID) {
    const height = nodeHeightByID.get(nodeID) ?? workflowNodeHeight;
    nodeInputs.forEach((input, index) => {
      ports.set(input.edgeID, {
        id: workflowGraphEndpointPortID(input.edgeID, side),
        nodeID,
        side,
        y: workflowGraphEndpointPortY(index, nodeInputs.length, height),
      });
    });
  }
  return ports;
}

function fallbackEndpointPort(
  input: WorkflowGraphEdgeModelInput,
  side: WorkflowGraphEndpointPortSide,
  nodeHeightByID: ReadonlyMap<string, number>,
): WorkflowGraphEndpointPort {
  const nodeID = endpointNodeID(input, side);
  return {
    id: workflowGraphEndpointPortID(input.edgeID, side),
    nodeID,
    side,
    y: workflowGraphEndpointPortY(0, 1, nodeHeightByID.get(nodeID) ?? workflowNodeHeight),
  };
}

function endpointNodeID(input: WorkflowGraphEdgeModelInput, side: WorkflowGraphEndpointPortSide): string {
  return side === "source" ? input.sourceNodeID : input.targetNodeID;
}
