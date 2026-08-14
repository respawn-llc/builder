import { describe, expect, it } from "vitest";

import { emptyWorkflowDerivedWiring, type WorkflowDefinition } from "@/api";
import {
  draftDefinitionFromSource,
  initializeWorkflowEditorDraft,
  workflowDefinitionFromDraft,
  workflowEditorDraftReducer,
} from "./workflowEditorDraft";
import { reconnectWorkflowEdge } from "./workflowEditorGraphEdgeMutations";
import { workflowGraphsEqual } from "./workflowDraftEquality";

const workflowID = "11111111-1111-4111-8111-111111111111";

function definitionWithEdge(edge: WorkflowDefinition["edges"][number]): WorkflowDefinition {
  return {
    derivedWiring: emptyWorkflowDerivedWiring,
    edges: [edge],
    nodeGroups: [],
    nodes: [
      {
        completionMode: "",
        groupID: "",
        groupKey: "",
        id: "source",
        joinInputProviders: [],
        key: "source",
        kind: "agent",
        name: "Source",
        scriptPath: null,
        subagentRole: "default",
        workflowID,
      },
      {
        completionMode: "",
        groupID: "",
        groupKey: "",
        id: "target",
        joinInputProviders: [],
        key: "target",
        kind: "agent",
        name: "Target",
        scriptPath: null,
        subagentRole: "default",
        workflowID,
      },
    ],
    transitionGroups: [
      {
        description: "",
        id: "group",
        name: "Next",
        sourceNodeID: "source",
        transitionID: "next",
        workflowID,
      },
    ],
    workflow: {
      description: "",
      executionTargetPolicy: { customRef: null, mode: "default_branch" },
      id: workflowID,
      name: "Workflow",
      version: 1,
    },
  };
}

const edge = {
  assigneeSelection: "configured" as const,
  contextMode: "new_session",
  contextSource: { kind: "immediate_source", nodeKey: "" },
  id: "edge",
  inputBindings: [],
  key: "edge",
  outputRequirements: [],
  parameters: [
    { description: "ordinary", key: "ordinary", purpose: "ordinary" as const },
  ],
  promptTemplate: "",
  requiresApproval: false,
  targetNodeID: "target",
  thinkingSelection: "configured" as const,
  transitionGroupID: "group",
  workflowID,
};

describe("workflow editor Edge-owned selectors", () => {
  it("initializes a protected row when enabling and retains it when disabling", () => {
    const state = initializeWorkflowEditorDraft(definitionWithEdge(edge));
    const enabled = workflowEditorDraftReducer(state, {
      edgeID: "edge",
      selection: "previous_node",
      type: "setEdgeAssigneeSelection",
    });
    const enabledEdge = enabled.draft.edges[0];
    expect(enabledEdge?.assigneeSelection).toBe("previous_node");
    expect(enabledEdge?.parameters).toEqual([
      { description: "ordinary", key: "ordinary", purpose: "ordinary", rowID: "edge:parameter:0" },
      {
        description: "",
        key: "agent_role",
        purpose: "target_assignee",
        rowID: "edge:parameter:target_assignee",
      },
    ]);

    const disabled = workflowEditorDraftReducer(enabled, {
      edgeID: "edge",
      selection: "configured",
      type: "setEdgeAssigneeSelection",
    });
    expect(disabled.draft.edges[0]?.assigneeSelection).toBe("configured");
    expect(disabled.draft.edges[0]?.parameters.some((parameter) => parameter.purpose === "target_assignee")).toBe(
      true,
    );
  });

  it("rejects protected parameter deletion while preserving ordinary deletion", () => {
    const source = definitionWithEdge({
      ...edge,
      assigneeSelection: "previous_node",
      parameters: [
        { description: "", key: "agent_role", purpose: "target_assignee" },
        { description: "ordinary", key: "ordinary", purpose: "ordinary" },
      ],
    });
    const state = initializeWorkflowEditorDraft(source);
    const protectedRowID = state.draft.edges[0]?.parameters[0]?.rowID ?? "";
    const ordinaryRowID = state.draft.edges[0]?.parameters[1]?.rowID ?? "";
    const afterProtectedDelete = workflowEditorDraftReducer(state, {
      edgeID: "edge",
      parameterRowID: protectedRowID,
      type: "deleteEdgeParameter",
    });
    expect(afterProtectedDelete.draft.edges[0]?.parameters).toHaveLength(2);

    const afterOrdinaryDelete = workflowEditorDraftReducer(afterProtectedDelete, {
      edgeID: "edge",
      parameterRowID: ordinaryRowID,
      type: "deleteEdgeParameter",
    });
    expect(afterOrdinaryDelete.draft.edges[0]?.parameters).toHaveLength(1);
    expect(afterOrdinaryDelete.draft.edges[0]?.parameters[0]?.purpose).toBe("target_assignee");
  });

  it("preserves selector state and protected rows through reconnect", () => {
    const source = definitionWithEdge({
      ...edge,
      assigneeSelection: "previous_node",
      thinkingSelection: "previous_node",
      parameters: [
        { description: "role", key: "role", purpose: "target_assignee" },
        { description: "thinking", key: "thinking", purpose: "target_thinking" },
      ],
    });
    const draft = draftDefinitionFromSource(source);
    const result = reconnectWorkflowEdge(draft, {
      edgeID: "edge",
      endpoint: "target",
      targetNodeID: "source",
    });
    const reconnected = result.draft.edges[0];
    expect(reconnected?.targetNodeID).toBe("source");
    expect(reconnected?.assigneeSelection).toBe("previous_node");
    expect(reconnected?.thinkingSelection).toBe("previous_node");
    expect(reconnected?.parameters.map((parameter) => parameter.purpose)).toEqual([
      "target_assignee",
      "target_thinking",
    ]);
  });

  it("treats selector modes and parameter purposes as graph identity", () => {
    const left = definitionWithEdge(edge);
    const right = definitionWithEdge({
      ...edge,
      assigneeSelection: "previous_node",
      parameters: [{ description: "", key: "agent_role", purpose: "target_assignee" }],
    });
    expect(workflowGraphsEqual(left, right)).toBe(false);
    expect(workflowGraphsEqual(left, workflowDefinitionFromDraft(draftDefinitionFromSource(left)))).toBe(true);
  });
});
