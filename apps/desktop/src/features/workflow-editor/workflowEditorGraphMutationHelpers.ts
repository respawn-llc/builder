import type { WorkflowEdge } from "../../api";
import type { DraftWorkflowDefinition, DraftWorkflowNode } from "./workflowEditorDraftTypes";
import {
  type WorkflowEditorGraphMutationResult,
  type WorkflowEditorSelection,
  workflowSelection,
} from "./workflowEditorGraphMutationTypes";

export function removeEdges(
  draft: DraftWorkflowDefinition,
  edgeIDs: readonly string[],
  nextSelection: WorkflowEditorSelection,
): WorkflowEditorGraphMutationResult {
  const removed = new Set(edgeIDs);
  const nextDraft = removeEdgesInternal(draft, removed);
  return {
    draft: nextDraft,
    nextSelection,
    summary: {
      removedEdgeIDs: edgeIDs,
      removedNodeIDs: [],
      removedTransitionGroupIDs: transitionGroupDifference(draft, nextDraft),
    },
    warnings: [],
  };
}

export function deleteNodeIDsInternal(
  draft: DraftWorkflowDefinition,
  nodeIDs: ReadonlySet<string>,
): DraftWorkflowDefinition {
  const removedTransitionGroupIDs = new Set(
    draft.transitionGroups.filter((group) => nodeIDs.has(group.sourceNodeID)).map((group) => group.id),
  );
  const removedEdgeIDs = new Set(
    draft.edges
      .filter((edge) => nodeIDs.has(edge.targetNodeID) || removedTransitionGroupIDs.has(edge.transitionGroupID))
      .map((edge) => edge.id),
  );
  const afterEdges = removeEdgesInternal(draft, removedEdgeIDs);
  const removedNodeKeys = new Set(draft.nodes.filter((node) => nodeIDs.has(node.id)).map((node) => node.key));
  const remainingNodes = afterEdges.nodes
    .filter((item) => !nodeIDs.has(item.id))
    .map((item) => ({
      ...item,
      joinInputProviders: item.joinInputProviders.filter(
        (provider) => !removedEdgeIDs.has(provider.providerEdgeID),
      ),
    }));
  return {
    ...afterEdges,
    edges: cleanupSelectedContextSources(afterEdges.edges, remainingNodes, removedNodeKeys),
    nodeGroups: refreshNodeGroupMembership(afterEdges.nodeGroups, remainingNodes),
    nodes: remainingNodes,
  };
}

export function removeEdgesInternal(
  draft: DraftWorkflowDefinition,
  edgeIDs: ReadonlySet<string>,
): DraftWorkflowDefinition {
  const edges = draft.edges.filter((edge) => !edgeIDs.has(edge.id));
  const nonEmptyTransitionGroupIDs = new Set(edges.map((edge) => edge.transitionGroupID));
  return {
    ...draft,
    edges,
    nodes: draft.nodes.map((node) => ({
      ...node,
      joinInputProviders: node.joinInputProviders.filter(
        (provider) => !edgeIDs.has(provider.providerEdgeID),
      ),
    })),
    transitionGroups: draft.transitionGroups.filter((group) => nonEmptyTransitionGroupIDs.has(group.id)),
  };
}

export function refreshNodeGroupMembership(
  groups: DraftWorkflowDefinition["nodeGroups"],
  nodes: readonly DraftWorkflowNode[],
): DraftWorkflowDefinition["nodeGroups"] {
  return groups.map((group) => ({
    ...group,
    nodeIDs: nodes.filter((node) => node.groupID === group.id).map((node) => node.id),
  }));
}

export function transitionGroupDifference(
  before: DraftWorkflowDefinition,
  after: DraftWorkflowDefinition,
): readonly string[] {
  const afterIDs = new Set(after.transitionGroups.map((group) => group.id));
  return before.transitionGroups.filter((group) => !afterIDs.has(group.id)).map((group) => group.id);
}

export function incidentEdges(
  draft: DraftWorkflowDefinition,
  nodeID: string,
): readonly WorkflowEdge[] {
  const outgoingTransitionGroupIDs = new Set(
    draft.transitionGroups.filter((group) => group.sourceNodeID === nodeID).map((group) => group.id),
  );
  return draft.edges.filter(
    (edge) => edge.targetNodeID === nodeID || outgoingTransitionGroupIDs.has(edge.transitionGroupID),
  );
}

export function transitionIDsForSource(
  draft: DraftWorkflowDefinition,
  sourceNodeID: string,
): readonly string[] {
  return draft.transitionGroups
    .filter((group) => group.sourceNodeID === sourceNodeID)
    .map((group) => group.transitionID);
}

export function edgesForTransitionGroup(
  draft: DraftWorkflowDefinition,
  transitionGroupID: string,
): readonly WorkflowEdge[] {
  return draft.edges.filter((edge) => edge.transitionGroupID === transitionGroupID);
}

export function workflowTransitionGroup(input: Readonly<{
  description?: string | undefined;
  id: string;
  name: string;
  sourceNodeID: string;
  transitionID: string;
  workflowID: string;
}>) {
  return {
    description: input.description ?? "",
    id: input.id,
    name: input.name,
    sourceNodeID: input.sourceNodeID,
    transitionID: input.transitionID,
    workflowID: input.workflowID,
  };
}

export function workflowEdge(input: Readonly<{
  id: string;
  key: string;
  targetNodeID: string;
  transitionGroupID: string;
  workflowID: string;
}>): WorkflowEdge {
  return {
    contextMode: "new_session",
    contextSource: { kind: "immediate_source", nodeKey: "" },
    id: input.id,
    inputBindings: [],
    key: input.key,
    outputRequirements: [],
    parameters: [],
    promptTemplate: "",
    requiresApproval: false,
    targetNodeID: input.targetNodeID,
    transitionGroupID: input.transitionGroupID,
    workflowID: input.workflowID,
  };
}

export function unchangedWorkflowSelection(): WorkflowEditorSelection {
  return workflowSelection;
}

function cleanupSelectedContextSources(
  edges: readonly WorkflowEdge[],
  remainingNodes: readonly DraftWorkflowNode[],
  removedNodeKeys: ReadonlySet<string>,
): readonly WorkflowEdge[] {
  return edges.map((edge) =>
    edge.contextSource.kind === "selected_node" &&
    removedNodeKeys.has(edge.contextSource.nodeKey) &&
    !remainingNodes.some((node) => node.key === edge.contextSource.nodeKey)
      ? { ...edge, contextSource: { kind: "immediate_source", nodeKey: "" } }
      : edge,
  );
}
