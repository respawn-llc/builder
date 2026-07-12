import type { WorkflowContextSource, WorkflowNode, WorkflowNodeKind } from "../../api";
import type { DraftWorkflowDefinition } from "./workflowEditorDraftTypes";

export type WorkflowEditorSelection =
  | Readonly<{ kind: "workflow" }>
  | Readonly<{ kind: "node"; nodeID: string }>
  | Readonly<{ kind: "edge"; edgeID: string }>
  | Readonly<{ kind: "group"; groupID: string }>;

export type WorkflowEditorCascadeSummary = Readonly<{
  removedNodeIDs: readonly string[];
  removedEdgeIDs: readonly string[];
  removedTransitionGroupIDs: readonly string[];
}>;

export type WorkflowEditorGraphMutationResult = Readonly<{
  draft: DraftWorkflowDefinition;
  summary: WorkflowEditorCascadeSummary;
  warnings: readonly string[];
  nextSelection: WorkflowEditorSelection;
}>;

export type AddWorkflowNodeInput = Readonly<{
  id: string;
  kind: WorkflowNode["kind"];
  name?: string | undefined;
  key?: string | undefined;
  subagentRole?: string | undefined;
  promptTemplate?: string | undefined;
}>;

export type CreatableWorkflowNodeKind = Extract<WorkflowNodeKind, "agent" | "script" | "terminal">;

export type AddConnectedWorkflowNodeInput = Readonly<{
  edgeID: string;
  kind: CreatableWorkflowNodeKind;
  nodeID: string;
  sourceNodeID: string;
  transitionGroupID: string;
}>;

export type ConnectWorkflowNodesInput = Readonly<{
  edgeID: string;
  sourceNodeID: string;
  targetNodeID: string;
  transitionGroupID: string;
  transitionID?: string | undefined;
  transitionName?: string | undefined;
  transitionDescription?: string | undefined;
  edgeKey?: string | undefined;
}>;

export type EditWorkflowEdgeRouteInput = Readonly<{
  edgeID: string;
  transitionID?: string | undefined;
  transitionName?: string | undefined;
  transitionDescription?: string | undefined;
  edgeKey?: string | undefined;
  requiresApproval?: boolean | undefined;
  contextMode?: string | undefined;
  contextSource?: WorkflowContextSource | undefined;
}>;

export type ReconnectWorkflowEdgeInput =
  | Readonly<{ edgeID: string; endpoint: "source"; sourceNodeID: string }>
  | Readonly<{ edgeID: string; endpoint: "target"; targetNodeID: string }>;

export type CreateWorkflowNodeGroupInput = Readonly<{
  groupID: string;
  joinNodeID: string;
  nodeID: string;
  groupName?: string | undefined;
  groupKey?: string | undefined;
}>;

export type AddWorkflowNodeToGroupInput = Readonly<{
  inferredTopologyIDs?: InferredNodeGroupTopologyIDs | undefined;
  nodeID: string;
  groupID: string;
}>;

export type ExtractWorkflowNodeFromGroupInput = Readonly<{
  nodeID: string;
  rehomedIncomingTransitionGroupID: string;
}>;

export type InferredNodeGroupTopologyIDs = Readonly<{
  addedBranchJoinEdgeID: string;
  addedBranchJoinTransitionGroupID: string;
  existingBranchJoinEdgeID: string;
  existingBranchJoinTransitionGroupID: string;
  fanoutEdgeID: string;
}>;

export const emptySummary: WorkflowEditorCascadeSummary = {
  removedEdgeIDs: [],
  removedNodeIDs: [],
  removedTransitionGroupIDs: [],
};

export const workflowEditorGraphMutationWarnings = {
  edgeNotFound: "edge was not found",
  fanoutSourceReconnectUnsupported: "fan-out transition branches cannot reconnect their source",
  lastTerminalDelete: "last terminal node cannot be deleted",
  missingConnectNodes: "source and target nodes are required",
  nodeNotFound: "node was not found",
  nodeGroupNotFound: "node group was not found",
  nodeGroupRequiresAgentMembership: "node group membership can be changed for agent nodes only",
  nodeGroupRequiresAgent: "node groups can be created from agent nodes only",
  nodeGroupExtractionTopologyFailed: "node group extraction topology could not be inferred safely",
  nodeGroupTopologyInferenceFailed: "node group topology could not be inferred safely",
  nodeGroupRequiresUngroupedNode: "node already belongs to a node group",
  startIncomingEdge: "start nodes cannot have incoming edges",
  startNodeDelete: "start node cannot be deleted",
  terminalOutgoingEdge: "terminal nodes cannot have outgoing edges",
  transitionGroupNotFound: "transition group was not found",
} as const;

export const workflowSelection: WorkflowEditorSelection = { kind: "workflow" };

export function unchanged(
  draft: DraftWorkflowDefinition,
  warning: string,
): WorkflowEditorGraphMutationResult {
  return { draft, nextSelection: workflowSelection, summary: emptySummary, warnings: [warning] };
}
