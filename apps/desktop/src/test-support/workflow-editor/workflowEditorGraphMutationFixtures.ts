import { emptyWorkflowDerivedWiring, type WorkflowDefinition, type WorkflowNode } from "../../api";
import type { draftDefinitionFromSource } from "../../features/workflow-editor/workflowEditorDraft";

export const workflowDefinition: WorkflowDefinition = {
  workflow: {
    description: "",
    id: "workflow-1",
    name: "Workflow",
    version: 1,
  },
  derivedWiring: emptyWorkflowDerivedWiring,
  edges: [
    {
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
      id: "edge-start",
      inputBindings: [],
      key: "start",
      outputRequirements: [],
      parameters: [],
      promptTemplate: "",
      requiresApproval: false,
      targetNodeID: "node-agent",
      transitionGroupID: "group-start",
      workflowID: "workflow-1",
    },
    {
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
      id: "edge-done",
      inputBindings: [],
      key: "done",
      outputRequirements: [],
      parameters: [],
      promptTemplate: "",
      requiresApproval: false,
      targetNodeID: "node-done",
      transitionGroupID: "group-done",
      workflowID: "workflow-1",
    },
  ],
  nodeGroups: [],
  nodes: [
    workflowNode("node-start", "backlog", "start", "Backlog"),
    workflowNode("node-agent", "implement", "agent", "Implement"),
    workflowNode("node-done", "done", "terminal", "Done"),
  ],
  transitionGroups: [
    {
      description: "",
      id: "group-start",
      name: "Start",
      sourceNodeID: "node-start",
      transitionID: "start",
      workflowID: "workflow-1",
    },
    {
      description: "",
      id: "group-done",
      name: "Done",
      sourceNodeID: "node-agent",
      transitionID: "done",
      workflowID: "workflow-1",
    },
  ],
};

export const groupableWorkflowDefinition: WorkflowDefinition = {
  ...workflowDefinition,
  edges: [
    {
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
      id: "edge-start",
      inputBindings: [],
      key: "start",
      outputRequirements: [],
      parameters: [],
      promptTemplate: "",
      requiresApproval: false,
      targetNodeID: "node-source",
      transitionGroupID: "group-start",
      workflowID: "workflow-1",
    },
    {
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
      id: "edge-source-agent",
      inputBindings: [],
      key: "implement",
      outputRequirements: [],
      parameters: [],
      promptTemplate: "",
      requiresApproval: false,
      targetNodeID: "node-agent",
      transitionGroupID: "group-source-agent",
      workflowID: "workflow-1",
    },
    requiredItem(workflowDefinition.edges, 1),
  ],
  nodes: [
    workflowNode("node-start", "backlog", "start", "Backlog"),
    workflowNode("node-source", "plan", "agent", "Plan"),
    workflowNode("node-agent", "implement", "agent", "Implement"),
    workflowNode("node-done", "done", "terminal", "Done"),
  ],
  transitionGroups: [
    requiredItem(workflowDefinition.transitionGroups, 0),
    {
      description: "",
      id: "group-source-agent",
      name: "Implement",
      sourceNodeID: "node-source",
      transitionID: "implement",
      workflowID: "workflow-1",
    },
    requiredItem(workflowDefinition.transitionGroups, 1),
  ],
};

export const joinWorkflowDefinition: WorkflowDefinition = {
  ...workflowDefinition,
  edges: [
    ...workflowDefinition.edges,
    {
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
      id: "edge-branch-a-join",
      inputBindings: [],
      key: "join_a",
      outputRequirements: [],
      parameters: [],
      promptTemplate: "",
      requiresApproval: false,
      targetNodeID: "node-join",
      transitionGroupID: "group-branch-a-join",
      workflowID: "workflow-1",
    },
    {
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
      id: "edge-branch-b-join",
      inputBindings: [],
      key: "join_b",
      outputRequirements: [],
      parameters: [],
      promptTemplate: "",
      requiresApproval: false,
      targetNodeID: "node-join",
      transitionGroupID: "group-branch-b-join",
      workflowID: "workflow-1",
    },
    {
      contextMode: "continue_session",
      contextSource: { kind: "selected_node", nodeKey: "branch_a" },
      id: "edge-join-done",
      inputBindings: [],
      key: "done",
      outputRequirements: [],
      parameters: [],
      promptTemplate: "",
      requiresApproval: false,
      targetNodeID: "node-done",
      transitionGroupID: "group-join-done",
      workflowID: "workflow-1",
    },
  ],
  nodes: [
    ...workflowDefinition.nodes,
    workflowNode("node-branch-a", "branch_a", "agent", "Branch A"),
    workflowNode("node-branch-b", "branch_b", "agent", "Branch B"),
    {
      ...workflowNode("node-join", "join", "join", "Join"),
      joinInputProviders: [
        { inputName: "plan", providerEdgeID: "edge-branch-a-join" },
        { inputName: "risk", providerEdgeID: "edge-branch-b-join" },
      ],
    },
  ],
  transitionGroups: [
    ...workflowDefinition.transitionGroups,
    {
      description: "",
      id: "group-branch-a-join",
      name: "Join",
      sourceNodeID: "node-branch-a",
      transitionID: "join",
      workflowID: "workflow-1",
    },
    {
      description: "",
      id: "group-branch-b-join",
      name: "Join",
      sourceNodeID: "node-branch-b",
      transitionID: "join",
      workflowID: "workflow-1",
    },
    {
      description: "",
      id: "group-join-done",
      name: "Done",
      sourceNodeID: "node-join",
      transitionID: "done",
      workflowID: "workflow-1",
    },
  ],
};

export function edgesForTransition(
  draft: ReturnType<typeof draftDefinitionFromSource>,
  transitionGroupID: string,
) {
  return draft.edges.filter((edge) => edge.transitionGroupID === transitionGroupID);
}

function workflowNode(id: string, key: string, kind: WorkflowNode["kind"], name: string) {
  return {
    groupID: "",
    groupKey: "",
    id,
    inputFields: [],
    joinInputProviders: [],
    key,
    kind,
    name,
    completionMode: "",
    outputFields: [],
    promptTemplate: kind === "agent" ? "Do work." : "",
    subagentRole: kind === "agent" ? "coder" : "",
    workflowID: "workflow-1",
  };
}

function requiredItem<T>(items: readonly T[], index: number): T {
  const item = items[index];
  if (item === undefined) {
    throw new Error(`fixture item ${index.toString()} is missing`);
  }
  return item;
}
