import { Position } from "@xyflow/react";
import type { ElkNode } from "elkjs/lib/elk-api";

import type { WorkflowDefinition, WorkflowNodeKind } from "../../api";
import {
  emptyGroupHeight,
  emptyGroupWidth,
  graphNodeHeight,
  graphNodeWidth,
  type NodeLayoutOffset,
  type WorkflowGraphEndpointPort,
  type WorkflowGraphNodeRect,
  workflowGraphCreationHandleID,
  workflowJoinGroupGap,
  workflowJoinNodeSize,
  workflowNodeHeight,
  workflowNodeWidth,
} from "./workflowGraphLayoutGeometry";
import type { WorkflowGraphNode } from "./workflowGraphLayout";

export type WorkflowGraphNodeLayout = Readonly<{
  alignedJoinNodeIDs: ReadonlySet<string>;
  groupNodeByGroupID: ReadonlyMap<string, WorkflowGraphNode>;
  nodes: WorkflowGraphNode[];
  rectByNodeID: ReadonlyMap<string, WorkflowGraphNodeRect>;
}>;

type WorkflowGraphValidationMarkers = Readonly<{
  nodeIDs: ReadonlySet<string>;
  relatedIDs: ReadonlySet<string>;
}>;

export function layoutWorkflowGraphNodes(
  result: ElkNode,
  definition: WorkflowDefinition,
  errorMarkers: WorkflowGraphValidationMarkers,
  endpointPortsByNodeID: ReadonlyMap<string, readonly WorkflowGraphEndpointPort[]> = new Map(),
): WorkflowGraphNodeLayout {
  const workflowNodesByID = new Map(
    definition.nodes.map((node) => [node.id, node]),
  );
  const layoutByID = layoutNodeByID(result);
  const groupLayout = workflowGroupNodes(definition, layoutByID, errorMarkers);
  const alignedJoinLayoutByID = alignedJoinLayouts(definition, layoutByID, groupLayout.groupNodeByGroupID);
  const groupNodeByGraphID = new Map(groupLayout.nodes.map((node) => [node.id, node]));
  const out: WorkflowGraphNode[] = [...groupLayout.nodes];
  const rectByNodeID = new Map<string, WorkflowGraphNodeRect>();
  for (const node of definition.nodes) {
    const layout = alignedJoinLayoutByID.get(node.id) ?? layoutByID.get(node.id);
    if (layout === undefined || !workflowNodesByID.has(node.id)) {
      continue;
    }
    const renderedNode = workflowNode(layout, node, {
      errorMarkers,
      endpointPorts: endpointPortsByNodeID.get(node.id) ?? [],
      offset: groupLayout.memberOffsetByNodeID.get(node.id),
      parentID: groupLayout.memberParentIDByNodeID.get(node.id),
    });
    out.push(renderedNode);
    rectByNodeID.set(node.id, workflowGraphNodeRect(renderedNode, node, groupNodeByGraphID));
  }
  return {
    alignedJoinNodeIDs: new Set(alignedJoinLayoutByID.keys()),
    groupNodeByGroupID: groupLayout.groupNodeByGroupID,
    nodes: out,
    rectByNodeID,
  };
}

export function isWorkflowGroupIslandMember(node: WorkflowDefinition["nodes"][number]): boolean {
  return node.groupID.length > 0 && node.kind !== "join";
}

function layoutNodeByID(root: ElkNode): ReadonlyMap<string, ElkNode> {
  const out = new Map<string, ElkNode>();
  function visit(node: ElkNode): void {
    out.set(node.id, node);
    for (const child of node.children ?? []) {
      visit(child);
    }
  }
  visit(root);
  return out;
}

function workflowGroupNodes(
  definition: WorkflowDefinition,
  layoutByID: ReadonlyMap<string, ElkNode>,
  errorMarkers: WorkflowGraphValidationMarkers,
): Readonly<{
  groupNodeByGroupID: ReadonlyMap<string, WorkflowGraphNode>;
  memberOffsetByNodeID: ReadonlyMap<string, NodeLayoutOffset>;
  memberParentIDByNodeID: ReadonlyMap<string, string>;
  nodes: WorkflowGraphNode[];
}> {
  const nodes: WorkflowGraphNode[] = [];
  const groupNodeByGroupID = new Map<string, WorkflowGraphNode>();
  const memberParentIDByNodeID = new Map<string, string>();
  const memberOffsetByNodeID = new Map<string, NodeLayoutOffset>();
  for (const group of definition.nodeGroups) {
    const members = groupMembers(definition, layoutByID, group.id);
    const layout = layoutByID.get(groupNodeID(group.id));
    const groupNode =
      members.length === 0
        ? emptyGroupNode(group, layout, errorMarkers)
        : populatedGroupNode(group, members, layout, errorMarkers);
    nodes.push(groupNode.node);
    groupNodeByGroupID.set(group.id, groupNode.node);
    for (const nodeID of groupNode.memberNodeIDs) {
      memberParentIDByNodeID.set(nodeID, groupNode.node.id);
      memberOffsetByNodeID.set(nodeID, groupNode.offset);
    }
  }
  return { groupNodeByGroupID, memberOffsetByNodeID, memberParentIDByNodeID, nodes };
}

function groupMembers(
  definition: WorkflowDefinition,
  layoutByID: ReadonlyMap<string, ElkNode>,
  groupID: string,
): readonly ElkNode[] {
  return definition.nodes
    .filter((node) => node.groupID === groupID && isWorkflowGroupIslandMember(node))
    .map((node) => layoutByID.get(node.id))
    .filter((node): node is ElkNode => node !== undefined);
}

function emptyGroupNode(
  group: WorkflowDefinition["nodeGroups"][number],
  layout: ElkNode | undefined,
  errorMarkers: WorkflowGraphValidationMarkers,
): Readonly<{ memberNodeIDs: readonly string[]; node: WorkflowGraphNode; offset: NodeLayoutOffset }> {
  return {
    memberNodeIDs: [],
    node: {
      id: groupNodeID(group.id),
      type: "workflowGroup",
      position: { x: layout?.x ?? 0, y: layout?.y ?? 0 },
      selectable: true,
      draggable: false,
      data: {
        kind: "group",
        entityID: group.id,
        entityKind: "group",
        label: group.name || group.key,
        empty: true,
        hasError: errorMarkers.relatedIDs.has(group.id),
      },
      style: { width: layout?.width ?? emptyGroupWidth, height: layout?.height ?? emptyGroupHeight },
    },
    offset: { x: 0, y: 0 },
  };
}

function populatedGroupNode(
  group: WorkflowDefinition["nodeGroups"][number],
  members: readonly ElkNode[],
  layout: ElkNode | undefined,
  errorMarkers: WorkflowGraphValidationMarkers,
): Readonly<{ memberNodeIDs: readonly string[]; node: WorkflowGraphNode; offset: NodeLayoutOffset }> {
  const bounds = groupBounds(members, layout);
  const offset = layout === undefined ? { x: bounds.minX, y: bounds.minY } : { x: 0, y: 0 };
  return {
    memberNodeIDs: members.map((member) => member.id),
    node: {
      id: groupNodeID(group.id),
      type: "workflowGroup",
      position: { x: bounds.minX, y: bounds.minY },
      selectable: true,
      draggable: false,
      data: {
        kind: "group",
        entityID: group.id,
        entityKind: "group",
        label: group.name || group.key,
        empty: false,
        hasError: errorMarkers.relatedIDs.has(group.id),
      },
      style: { width: bounds.maxX - bounds.minX, height: bounds.maxY - bounds.minY },
    },
    offset,
  };
}

function groupBounds(members: readonly ElkNode[], layout: ElkNode | undefined) {
  if (layout?.width !== undefined && layout.height !== undefined) {
    return {
      minX: layout.x ?? 0,
      minY: layout.y ?? 0,
      maxX: (layout.x ?? 0) + layout.width,
      maxY: (layout.y ?? 0) + layout.height,
    };
  }
  return {
    minX: Math.min(...members.map((member) => member.x ?? 0)) - 24,
    minY: Math.min(...members.map((member) => member.y ?? 0)) - 56,
    maxX: Math.max(...members.map((member) => (member.x ?? 0) + (member.width ?? workflowNodeWidth))) + 24,
    maxY: Math.max(...members.map((member) => (member.y ?? 0) + (member.height ?? workflowNodeHeight))) + 24,
  };
}

function alignedJoinLayouts(
  definition: WorkflowDefinition,
  layoutByID: ReadonlyMap<string, ElkNode>,
  groupNodeByGroupID: ReadonlyMap<string, WorkflowGraphNode>,
): ReadonlyMap<string, ElkNode> {
  const out = new Map<string, ElkNode>();
  for (const group of definition.nodeGroups) {
    const groupNode = groupNodeByGroupID.get(group.id);
    if (groupNode === undefined) {
      continue;
    }
    for (const join of definition.nodes.filter((node) => node.groupID === group.id && node.kind === "join")) {
      const layout = layoutByID.get(join.id);
      if (layout === undefined) {
        continue;
      }
      const width = layout.width ?? workflowJoinNodeSize;
      const height = layout.height ?? workflowJoinNodeSize;
      out.set(join.id, {
        ...layout,
        height,
        width,
        x: groupNode.position.x + graphNodeWidth(groupNode) + workflowJoinGroupGap,
        y: groupNode.position.y + graphNodeHeight(groupNode) / 2 - height / 2,
      });
    }
  }
  return out;
}

function workflowGraphNodeRect(
  graphNode: WorkflowGraphNode,
  workflowNodeModel: WorkflowDefinition["nodes"][number],
  groupNodeByGraphID: ReadonlyMap<string, WorkflowGraphNode>,
): WorkflowGraphNodeRect {
  const parentNode = graphNode.parentId === undefined ? undefined : groupNodeByGraphID.get(graphNode.parentId);
  const parentOffset = parentNode?.position ?? { x: 0, y: 0 };
  return {
    groupID: workflowNodeModel.groupID,
    height: graphNodeHeight(graphNode),
    kind: workflowNodeKind(workflowNodeModel.kind),
    width: graphNodeWidth(graphNode),
    x: parentOffset.x + graphNode.position.x,
    y: parentOffset.y + graphNode.position.y,
  };
}

function workflowNode(
  layoutNode: ElkNode,
  node: WorkflowDefinition["nodes"][number],
  options: Readonly<{
    endpointPorts: readonly WorkflowGraphEndpointPort[];
    errorMarkers: WorkflowGraphValidationMarkers;
    offset: NodeLayoutOffset | undefined;
    parentID: string | undefined;
  }>,
): WorkflowGraphNode {
  const offset = options.offset ?? { x: 0, y: 0 };
  return {
    id: node.id,
    type: node.kind === "join" ? "workflowJoin" : "workflowNode",
    ...(options.parentID === undefined ? {} : parentNodeOptions(options.parentID)),
    position: { x: (layoutNode.x ?? 0) - offset.x, y: (layoutNode.y ?? 0) - offset.y },
    sourcePosition: Position.Right,
    targetPosition: Position.Left,
    draggable: node.kind === "agent" || node.kind === "script",
    data: {
      kind: workflowNodeKind(node.kind),
      key: node.key,
      entityID: node.id,
      entityKind: "node",
      endpointPorts: options.endpointPorts,
      creationHandleID: workflowGraphCreationHandleIDForNode(node),
      groupID: node.groupID,
      label: node.name,
      role: workflowGraphNodeRole(node),
      hasError: options.errorMarkers.nodeIDs.has(node.id) || options.errorMarkers.relatedIDs.has(node.id),
    },
    style: { width: layoutNode.width ?? workflowNodeWidth, height: layoutNode.height ?? workflowNodeHeight },
  };
}

function workflowNodeKind(kind: string): WorkflowNodeKind {
  if (kind === "agent" || kind === "join" || kind === "script" || kind === "start" || kind === "terminal") {
    return kind;
  }
  throw new Error(`Unsupported workflow node kind in graph layout: ${kind}`);
}

function workflowGraphCreationHandleIDForNode(node: WorkflowDefinition["nodes"][number]): string | undefined {
  return node.kind === "terminal" ? undefined : workflowGraphCreationHandleID(node.id);
}

function workflowGraphNodeRole(node: WorkflowDefinition["nodes"][number]): string {
  return node.kind === "script" ? node.scriptPath ?? "" : node.subagentRole;
}

function parentNodeOptions(parentId: string): Pick<WorkflowGraphNode, "extent" | "parentId"> {
  return { extent: "parent", parentId };
}

function groupNodeID(id: string): string {
  return `workflow-group-${id}`;
}
