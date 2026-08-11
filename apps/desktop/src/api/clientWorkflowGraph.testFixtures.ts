import type { WorkflowGraphDraft } from "./workflowGraphModels";

export const workflowGraphDraftIDs = {
  agentNode: "10000000-0000-4000-8000-000000000002",
  startEdge: "40000000-0000-4000-8000-000000000001",
  startNode: "10000000-0000-4000-8000-000000000001",
  startTransitionGroup: "30000000-0000-4000-8000-000000000001",
} as const;

export const workflowBoundaryGraphIDs = {
  doneNode: "10000000-0000-4000-8000-000000000003",
  edge: "40000000-0000-4000-8000-000000000001",
  joinNode: "10000000-0000-4000-8000-000000000004",
  node: "10000000-0000-4000-8000-000000000002",
  nodeGroup: "20000000-0000-4000-8000-000000000001",
  joinOnlyNodeGroup: "20000000-0000-4000-8000-000000000002",
  relatedEdge: "40000000-0000-4000-8000-000000000002",
  transitionGroup: "30000000-0000-4000-8000-000000000001",
} as const;

export const workflowGraphDraft: WorkflowGraphDraft = {
  nodeGroups: [],
  nodes: [
    {
      id: workflowGraphDraftIDs.startNode,
      key: "backlog",
      kind: "start",
      name: "Backlog",
      groupID: null,
      joinInputProviders: [],
    },
  ],
  transitionGroups: [
    {
      id: workflowGraphDraftIDs.startTransitionGroup,
      sourceNodeID: workflowGraphDraftIDs.startNode,
      transitionID: "start",
      name: "Start",
      description: "Start the workflow.",
    },
  ],
  edges: [
    {
      id: workflowGraphDraftIDs.startEdge,
      transitionGroupID: workflowGraphDraftIDs.startTransitionGroup,
      key: "start",
      targetNodeID: workflowGraphDraftIDs.agentNode,
      assigneeSelection: "configured",
      thinkingSelection: "configured",
      requiresApproval: false,
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
      promptTemplate: "Start from {{.TaskTitle}}.",
      parameters: [{ key: "brief", description: "Brief", purpose: "ordinary" }],
    },
  ],
};
