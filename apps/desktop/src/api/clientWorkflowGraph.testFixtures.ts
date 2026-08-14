import type { WorkflowGraphDraft } from "./workflowGraphModels";

export const workflowGraphDraft: WorkflowGraphDraft = {
  nodeGroups: [],
  nodes: [
    {
      id: "node-start",
      key: "backlog",
      kind: "start",
      name: "Backlog",
      groupID: null,
      joinInputProviders: [],
    },
  ],
  transitionGroups: [
    {
      id: "group-start",
      sourceNodeID: "node-start",
      transitionID: "start",
      name: "Start",
      description: "Start the workflow.",
    },
  ],
  edges: [
    {
      id: "edge-start",
      transitionGroupID: "group-start",
      key: "start",
      targetNodeID: "node-agent",
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
