import { describe, expect, it } from "vitest";

import { emptyWorkflowDerivedWiring, type WorkflowDefinition, type WorkflowNode } from "../../api";
import {
  workflowDeleteNeedsConfirmation,
  workflowDeletionConfirmationCounts,
} from "./workflowDeleteConfirmationPolicy";
import { draftDefinitionFromSource } from "./workflowEditorDraft";
import { deleteWorkflowEdge } from "./workflowEditorGraphMutations";

describe("workflowDeleteConfirmationPolicy", () => {
  it("requires confirmation for a single fan-out branch when deleting prompt text", () => {
    const draft = draftDefinitionFromSource(fanoutWorkflowDefinition);
    const deleted = deleteWorkflowEdge(draft, "edge-review");
    const counts = workflowDeletionConfirmationCounts(draft, deleted.summary);

    expect(deleted.summary).toEqual({
      removedEdgeIDs: ["edge-review"],
      removedNodeIDs: [],
      removedTransitionGroupIDs: [],
    });
    expect(counts).toEqual({
      edgeCount: 1,
      nodeCount: 0,
      promptCount: 1,
      transitionGroupCount: 0,
    });
    expect(workflowDeleteNeedsConfirmation(counts)).toBe(true);
  });

  it("does not require confirmation for one unprompted fan-out branch", () => {
    const draft = draftDefinitionFromSource(fanoutWorkflowDefinition);
    const deleted = deleteWorkflowEdge(draft, "edge-audit");
    const counts = workflowDeletionConfirmationCounts(draft, deleted.summary);

    expect(counts).toEqual({
      edgeCount: 1,
      nodeCount: 0,
      promptCount: 0,
      transitionGroupCount: 0,
    });
    expect(workflowDeleteNeedsConfirmation(counts)).toBe(false);
  });
});

const agentNode = {
  groupID: "",
  groupKey: "",
  inputFields: [],
  joinInputProviders: [],
  kind: "agent" satisfies WorkflowNode["kind"],
  outputFields: [],
  promptTemplate: "",
  subagentRole: "default",
  workflowID: "workflow-1",
};

const fanoutWorkflowDefinition: WorkflowDefinition = {
  derivedWiring: emptyWorkflowDerivedWiring,
  edges: [
    {
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
      id: "edge-review",
      inputBindings: [],
      key: "review",
      outputRequirements: [],
      parameters: [],
      promptTemplate: "Review the task.",
      requiresApproval: false,
      targetNodeID: "node-review",
      transitionGroupID: "transition-fanout",
      workflowID: "workflow-1",
    },
    {
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
      id: "edge-audit",
      inputBindings: [],
      key: "audit",
      outputRequirements: [],
      parameters: [{ description: "Audit notes", key: "audit_notes" }],
      promptTemplate: "",
      requiresApproval: false,
      targetNodeID: "node-audit",
      transitionGroupID: "transition-fanout",
      workflowID: "workflow-1",
    },
  ],
  nodeGroups: [],
  nodes: [
    { ...agentNode, id: "node-source", key: "source", name: "Source" },
    { ...agentNode, id: "node-review", key: "review", name: "Review" },
    { ...agentNode, id: "node-audit", key: "audit", name: "Audit" },
  ],
  transitionGroups: [
    {
      description: "",
      id: "transition-fanout",
      name: "Review",
      sourceNodeID: "node-source",
      transitionID: "review",
      workflowID: "workflow-1",
    },
  ],
  workflow: {
    description: "",
    id: "workflow-1",
    name: "Workflow",
    version: 1,
  },
};
