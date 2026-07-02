import type { WorkflowEdge } from "../../api";
import type { DraftWorkflowDefinition } from "./workflowEditorDraft";
import { uniqueWorkflowModelKey } from "./workflowEditorGraphKeys";
import {
  edgesForTransitionGroup,
  removeEdges,
  transitionIDsForSource,
} from "./workflowEditorGraphMutationHelpers";
import {
  emptySummary,
  workflowEditorGraphMutationWarnings,
  type ConnectWorkflowNodesInput,
  type EditWorkflowEdgeRouteInput,
  type ReconnectWorkflowEdgeInput,
  type WorkflowEditorCascadeSummary,
  type WorkflowEditorGraphMutationResult,
  workflowSelection,
  unchanged,
} from "./workflowEditorGraphMutationTypes";

export function connectWorkflowNodes(
  draft: DraftWorkflowDefinition,
  input: ConnectWorkflowNodesInput,
): WorkflowEditorGraphMutationResult {
  const source = draft.nodes.find((node) => node.id === input.sourceNodeID);
  const target = draft.nodes.find((node) => node.id === input.targetNodeID);
  if (source === undefined || target === undefined) {
    return unchanged(draft, workflowEditorGraphMutationWarnings.missingConnectNodes);
  }
  if (source.kind === "terminal") {
    return unchanged(draft, workflowEditorGraphMutationWarnings.terminalOutgoingEdge);
  }
  if (target.kind === "start") {
    return unchanged(draft, workflowEditorGraphMutationWarnings.startIncomingEdge);
  }
  const nodeGroupFanout = nodeGroupFanoutConnection(draft, source.id, target.id);
  if (nodeGroupFanout !== null) {
    return connectNodeGroupFanoutBranch(draft, input, nodeGroupFanout);
  }
  const transitionID =
    input.transitionID ??
    uniqueWorkflowModelKey(target.key, transitionIDsForSource(draft, input.sourceNodeID));
  const transitionName = input.transitionName ?? target.name;
  const edgeKey =
    input.edgeKey ?? uniqueWorkflowModelKey(
      target.key,
      edgesForTransitionGroup(draft, input.transitionGroupID).map((edge) => edge.key),
    );
  const transitionGroup = {
    description: input.transitionDescription ?? "",
    id: input.transitionGroupID,
    name: transitionName,
    sourceNodeID: input.sourceNodeID,
    transitionID,
    workflowID: draft.workflow.id,
  };
  const edge: WorkflowEdge = {
    contextMode: "new_session",
    contextSource: { kind: "immediate_source", nodeKey: "" },
    id: input.edgeID,
    inputBindings: [],
    key: edgeKey,
    outputRequirements: [],
    parameters: [],
    promptTemplate: "",
    requiresApproval: false,
    targetNodeID: input.targetNodeID,
    transitionGroupID: input.transitionGroupID,
    workflowID: draft.workflow.id,
  };
  return {
    draft: {
      ...draft,
      edges: [...draft.edges, edge],
      transitionGroups: [...draft.transitionGroups, transitionGroup],
    },
    nextSelection: { edgeID: edge.id, kind: "edge" },
    summary: emptySummary,
    warnings: [],
  };
}

type NodeGroupFanoutConnection = Readonly<{
  transitionGroupID: string;
  existingEdgeID: string | null;
  existingTransitionGroupID: string | null;
}>;

function nodeGroupFanoutConnection(
  draft: DraftWorkflowDefinition,
  sourceNodeID: string,
  targetNodeID: string,
): NodeGroupFanoutConnection | null {
  const target = draft.nodes.find((node) => node.id === targetNodeID);
  if (target === undefined || !workflowBranchNodeKind(target.kind) || target.groupID.trim() === "") {
    return null;
  }
  const groupedBranchIDs = groupedSiblingBranchIDs(draft, target.groupID, targetNodeID);
  if (groupedBranchIDs.size === 0) {
    return null;
  }
  const sourceTransitionGroupIDs = transitionGroupIDsForSourceNode(draft, sourceNodeID);
  const candidateTransitionGroupIDs = transitionGroupIDsCoveringBranches(
    draft,
    sourceTransitionGroupIDs,
    groupedBranchIDs,
  );
  const transitionGroupID = singleTransitionGroupID(candidateTransitionGroupIDs);
  if (transitionGroupID === null) {
    return null;
  }
  const existingConnection = existingSourceTargetConnection(
    draft,
    sourceTransitionGroupIDs,
    targetNodeID,
    transitionGroupID,
  );
  if (existingConnection === "already_connected" || existingConnection === "ambiguous") {
    return null;
  }
  return {
    existingEdgeID: existingConnection?.id ?? null,
    existingTransitionGroupID: existingConnection?.transitionGroupID ?? null,
    transitionGroupID,
  };
}

function groupedSiblingBranchIDs(
  draft: DraftWorkflowDefinition,
  groupID: string,
  targetNodeID: string,
): ReadonlySet<string> {
  return new Set(
    draft.nodes
      .filter((node) => workflowBranchNodeKind(node.kind) && node.groupID === groupID && node.id !== targetNodeID)
      .map((node) => node.id),
  );
}

function workflowBranchNodeKind(kind: DraftWorkflowDefinition["nodes"][number]["kind"]): boolean {
  return kind === "agent" || kind === "script";
}

function transitionGroupIDsForSourceNode(
  draft: DraftWorkflowDefinition,
  sourceNodeID: string,
): ReadonlySet<string> {
  return new Set(
    draft.transitionGroups.filter((group) => group.sourceNodeID === sourceNodeID).map((group) => group.id),
  );
}

function transitionGroupIDsCoveringBranches(
  draft: DraftWorkflowDefinition,
  sourceTransitionGroupIDs: ReadonlySet<string>,
  branchIDs: ReadonlySet<string>,
): ReadonlySet<string> {
  return new Set(
    Array.from(sourceTransitionGroupIDs).filter((transitionGroupID) =>
      transitionGroupTargetsExactly(draft, transitionGroupID, branchIDs),
    ),
  );
}

function transitionGroupTargetsExactly(
  draft: DraftWorkflowDefinition,
  transitionGroupID: string,
  targetNodeIDs: ReadonlySet<string>,
): boolean {
  const edges = edgesForTransitionGroup(draft, transitionGroupID);
  return (
    edges.length === targetNodeIDs.size &&
    setsEqual(new Set(edges.map((edge) => edge.targetNodeID)), targetNodeIDs)
  );
}

function setsEqual(left: ReadonlySet<string>, right: ReadonlySet<string>): boolean {
  return left.size === right.size && Array.from(left).every((item) => right.has(item));
}

function singleTransitionGroupID(transitionGroupIDs: ReadonlySet<string>): string | null {
  if (transitionGroupIDs.size !== 1) {
    return null;
  }
  return Array.from(transitionGroupIDs)[0] ?? null;
}

function existingSourceTargetConnection(
  draft: DraftWorkflowDefinition,
  sourceTransitionGroupIDs: ReadonlySet<string>,
  targetNodeID: string,
  fanoutTransitionGroupID: string,
): "already_connected" | "ambiguous" | DraftWorkflowDefinition["edges"][number] | null {
  const existingSourceTargetEdges = draft.edges.filter(
    (edge) => sourceTransitionGroupIDs.has(edge.transitionGroupID) && edge.targetNodeID === targetNodeID,
  );
  if (existingSourceTargetEdges.some((edge) => edge.transitionGroupID === fanoutTransitionGroupID)) {
    return "already_connected";
  }
  if (existingSourceTargetEdges.length > 1) {
    return "ambiguous";
  }
  return existingSourceTargetEdges[0] ?? null;
}

function connectNodeGroupFanoutBranch(
  draft: DraftWorkflowDefinition,
  input: ConnectWorkflowNodesInput,
  connection: NodeGroupFanoutConnection,
): WorkflowEditorGraphMutationResult {
  const target = draft.nodes.find((node) => node.id === input.targetNodeID);
  if (target === undefined) {
    return unchanged(draft, workflowEditorGraphMutationWarnings.missingConnectNodes);
  }
  if (connection.existingEdgeID !== null) {
    const edges = draft.edges.map((edge) =>
      edge.id === connection.existingEdgeID
        ? { ...edge, key: input.edgeKey ?? edge.key, transitionGroupID: connection.transitionGroupID }
        : edge,
    );
    const nextDraft = {
      ...draft,
      edges,
      transitionGroups: removeTransitionGroupIfEmpty(draft, edges, connection.existingTransitionGroupID),
    };
    return {
      draft: nextDraft,
      nextSelection: { edgeID: connection.existingEdgeID, kind: "edge" },
      summary: removedTransitionGroupSummary(connection.existingTransitionGroupID, draft, nextDraft),
      warnings: [],
    };
  }
  const edgeKey = input.edgeKey ?? uniqueWorkflowModelKey(
    target.key,
    edgesForTransitionGroup(draft, connection.transitionGroupID).map((edge) => edge.key),
  );
  const edge = {
    contextMode: "new_session",
    contextSource: { kind: "immediate_source", nodeKey: "" },
    id: input.edgeID,
    inputBindings: [],
    key: edgeKey,
    outputRequirements: [],
    parameters: [],
    promptTemplate: "",
    requiresApproval: false,
    targetNodeID: input.targetNodeID,
    transitionGroupID: connection.transitionGroupID,
    workflowID: draft.workflow.id,
  };
  return {
    draft: {
      ...draft,
      edges: [...draft.edges, edge],
    },
    nextSelection: { edgeID: edge.id, kind: "edge" },
    summary: emptySummary,
    warnings: [],
  };
}

function removeTransitionGroupIfEmpty(
  draft: DraftWorkflowDefinition,
  edges: DraftWorkflowDefinition["edges"],
  transitionGroupID: string | null,
): DraftWorkflowDefinition["transitionGroups"] {
  if (transitionGroupID === null) {
    return draft.transitionGroups;
  }
  const transitionGroupIDs = new Set(edges.map((edge) => edge.transitionGroupID));
  return draft.transitionGroups.filter(
    (group) => group.id !== transitionGroupID || transitionGroupIDs.has(group.id),
  );
}

function removedTransitionGroupSummary(
  transitionGroupID: string | null,
  before: DraftWorkflowDefinition,
  after: DraftWorkflowDefinition,
): WorkflowEditorCascadeSummary {
  if (transitionGroupID === null) {
    return emptySummary;
  }
  const wasPresent = before.transitionGroups.some((group) => group.id === transitionGroupID);
  const isPresent = after.transitionGroups.some((group) => group.id === transitionGroupID);
  return {
    removedEdgeIDs: [],
    removedNodeIDs: [],
    removedTransitionGroupIDs: wasPresent && !isPresent ? [transitionGroupID] : [],
  };
}

export function deleteWorkflowEdge(
  draft: DraftWorkflowDefinition,
  edgeID: string,
): WorkflowEditorGraphMutationResult {
  const edge = draft.edges.find((item) => item.id === edgeID);
  if (edge === undefined) {
    return unchanged(draft, workflowEditorGraphMutationWarnings.edgeNotFound);
  }
  return removeEdges(draft, [edgeID], workflowSelection);
}

export function reconnectWorkflowEdge(
  draft: DraftWorkflowDefinition,
  input: ReconnectWorkflowEdgeInput,
): WorkflowEditorGraphMutationResult {
  const edge = draft.edges.find((item) => item.id === input.edgeID);
  if (edge === undefined) {
    return unchanged(draft, workflowEditorGraphMutationWarnings.edgeNotFound);
  }
  if (input.endpoint === "target") {
    return reconnectWorkflowEdgeTarget(draft, edge, input.targetNodeID);
  }
  return reconnectWorkflowEdgeSource(draft, edge, input.sourceNodeID);
}

export function editWorkflowEdgeRoute(
  draft: DraftWorkflowDefinition,
  input: EditWorkflowEdgeRouteInput,
): WorkflowEditorGraphMutationResult {
  const edge = draft.edges.find((item) => item.id === input.edgeID);
  if (edge === undefined) {
    return unchanged(draft, workflowEditorGraphMutationWarnings.edgeNotFound);
  }
  return {
    draft: {
      ...draft,
      edges: draft.edges.map((item) =>
        item.id === input.edgeID
          ? {
              ...item,
              contextMode: input.contextMode ?? item.contextMode,
              contextSource: input.contextSource ?? item.contextSource,
              key: input.edgeKey ?? item.key,
              requiresApproval: input.requiresApproval ?? item.requiresApproval,
            }
          : item,
      ),
      transitionGroups: draft.transitionGroups.map((group) =>
        group.id === edge.transitionGroupID
          ? {
              ...group,
              description: input.transitionDescription ?? group.description,
              name: input.transitionName ?? group.name,
              transitionID: input.transitionID ?? group.transitionID,
            }
          : group,
      ),
    },
    nextSelection: { edgeID: input.edgeID, kind: "edge" },
    summary: emptySummary,
    warnings: [],
  };
}

function reconnectWorkflowEdgeTarget(
  draft: DraftWorkflowDefinition,
  edge: WorkflowEdge,
  targetNodeID: string,
): WorkflowEditorGraphMutationResult {
  const target = draft.nodes.find((node) => node.id === targetNodeID);
  if (target === undefined) {
    return unchangedEdge(draft, edge.id, workflowEditorGraphMutationWarnings.missingConnectNodes);
  }
  if (target.kind === "start") {
    return unchangedEdge(draft, edge.id, workflowEditorGraphMutationWarnings.startIncomingEdge);
  }
  if (edge.targetNodeID === target.id) {
    return unchangedEdge(draft, edge.id);
  }
  return {
    draft: {
      ...draft,
      edges: draft.edges.map((item) =>
        item.id === edge.id ? { ...item, targetNodeID: target.id } : item,
      ),
    },
    nextSelection: { edgeID: edge.id, kind: "edge" },
    summary: emptySummary,
    warnings: [],
  };
}

function reconnectWorkflowEdgeSource(
  draft: DraftWorkflowDefinition,
  edge: WorkflowEdge,
  sourceNodeID: string,
): WorkflowEditorGraphMutationResult {
  const source = draft.nodes.find((node) => node.id === sourceNodeID);
  if (source === undefined) {
    return unchangedEdge(draft, edge.id, workflowEditorGraphMutationWarnings.missingConnectNodes);
  }
  if (source.kind === "terminal") {
    return unchangedEdge(draft, edge.id, workflowEditorGraphMutationWarnings.terminalOutgoingEdge);
  }
  const transitionGroup = draft.transitionGroups.find((group) => group.id === edge.transitionGroupID);
  if (transitionGroup === undefined) {
    return unchangedEdge(draft, edge.id, workflowEditorGraphMutationWarnings.transitionGroupNotFound);
  }
  if (edgesForTransitionGroup(draft, transitionGroup.id).length > 1) {
    return unchangedEdge(draft, edge.id, workflowEditorGraphMutationWarnings.fanoutSourceReconnectUnsupported);
  }
  if (transitionGroup.sourceNodeID === source.id) {
    return unchangedEdge(draft, edge.id);
  }
  return {
    draft: {
      ...draft,
      transitionGroups: draft.transitionGroups.map((group) =>
        group.id === transitionGroup.id ? { ...group, sourceNodeID: source.id } : group,
      ),
    },
    nextSelection: { edgeID: edge.id, kind: "edge" },
    summary: emptySummary,
    warnings: [],
  };
}

function unchangedEdge(
  draft: DraftWorkflowDefinition,
  edgeID: string,
  warning?: string,
): WorkflowEditorGraphMutationResult {
  return {
    draft,
    nextSelection: { edgeID, kind: "edge" },
    summary: emptySummary,
    warnings: warning === undefined ? [] : [warning],
  };
}
