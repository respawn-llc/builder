import { describe, expect, it } from "vitest";

import { emptyWorkflowDerivedWiring, type WorkflowDefinition } from "../../api";
import {
  initializeWorkflowEditorDraft,
  workflowDefinitionFromDraft,
  workflowEditorDirtyState,
  workflowEditorDraftGraph,
  workflowEditorDraftReducer,
} from "./workflowEditorDraft";

describe("workflowEditorDraft", () => {
  it("tracks metadata and agent node edits separately", () => {
    const initial = initializeWorkflowEditorDraft(workflowDefinition);
    expect(workflowEditorDirtyState(initial)).toEqual({
      dirty: false,
      graphDirty: false,
      metadataDirty: false,
    });

    const metadata = workflowEditorDraftReducer(initial, {
      description: "Updated description",
      name: "Updated workflow",
      type: "editWorkflowMetadata",
    });
    expect(workflowEditorDirtyState(metadata)).toEqual({
      dirty: true,
      graphDirty: false,
      metadataDirty: true,
    });
    expect(metadata.version).toBe(initial.version + 1);
    expect(metadata.graphVersion).toBe(initial.graphVersion);

    const graph = workflowEditorDraftReducer(initial, {
      nodeID: "node-agent",
      patch: { completionMode: "shell_command", name: "Edited agent" },
      type: "editAgentNode",
    });
    expect(workflowEditorDirtyState(graph)).toEqual({ dirty: true, graphDirty: true, metadataDirty: false });
    expect(graph.version).toBe(initial.version + 1);
    expect(graph.graphVersion).toBe(initial.graphVersion);
    expect(workflowDefinitionFromDraft(graph.draft).nodes[0]?.name).toBe("Edited agent");
    expect(workflowDefinitionFromDraft(graph.draft).nodes[0]?.completionMode).toBe("shell_command");
    expect(workflowEditorDraftGraph(graph).nodes[0]?.completionMode).toBe("shell_command");
    expect(workflowDefinitionFromDraft(graph.draft).nodes[0]?.outputFields).toEqual([
      { description: "Summary", name: "summary" },
    ]);
  });

  it("keeps draft versions stable when topology mutations are blocked", () => {
    const initial = initializeWorkflowEditorDraft(workflowDefinition);
    const blocked = workflowEditorDraftReducer(initial, {
      nodeID: "missing-node",
      type: "deleteNode",
    });

    expect(blocked.version).toBe(initial.version);
    expect(blocked.graphVersion).toBe(initial.graphVersion);
    expect(blocked.lastTopologyMutation?.warnings).toEqual(["node was not found"]);
  });

  it("edits edge prompts and serializes branch parameters", () => {
    const prompted = workflowEditorDraftReducer(initializeWorkflowEditorDraft(workflowDefinition), {
      edgeID: "edge-1",
      promptTemplate: "Review {{.Params.plan}}.",
      type: "editEdgePrompt",
    });
    const added = workflowEditorDraftReducer(initializeWorkflowEditorDraft(workflowDefinition), {
      edgeID: "edge-1",
      type: "addEdgeParameter",
    });
    const addedParameterRowID = expectDefined(
      added.draft.edges[0]?.parameters[0]?.rowID,
      "Expected added parameter row ID.",
    );
    const updated = workflowEditorDraftReducer(added, {
      edgeID: "edge-1",
      parameterRowID: addedParameterRowID,
      patch: { description: "Plan", key: "plan" },
      type: "updateEdgeParameter",
    });
    const addedSecond = workflowEditorDraftReducer(updated, {
      edgeID: "edge-1",
      type: "addEdgeParameter",
    });
    const secondParameterRowID = expectDefined(
      addedSecond.draft.edges[0]?.parameters[0]?.rowID,
      "Expected second added parameter row ID.",
    );
    const updatedSecond = workflowEditorDraftReducer(addedSecond, {
      edgeID: "edge-1",
      parameterRowID: secondParameterRowID,
      patch: { description: "Notes", key: "notes" },
      type: "updateEdgeParameter",
    });
    const reordered = workflowEditorDraftReducer(updatedSecond, {
      activeRowID: addedParameterRowID,
      edgeID: "edge-1",
      overRowID: secondParameterRowID,
      type: "reorderEdgeParameter",
    });
    const deleted = workflowEditorDraftReducer(reordered, {
      edgeID: "edge-1",
      parameterRowID: secondParameterRowID,
      type: "deleteEdgeParameter",
    });

    expect(workflowEditorDraftGraph(prompted).edges[0]?.promptTemplate).toBe("Review {{.Params.plan}}.");
    expect(workflowEditorDraftGraph(deleted).edges[0]?.parameters).toEqual([
      { description: "Plan", key: "plan" },
    ]);
    expect(deleted.draft.edges[0]?.parameters[0]?.rowID).toBe(addedParameterRowID);
    expect(workflowDefinitionFromDraft(deleted.draft).edges[0]?.parameters[0]).toEqual({
      description: "Plan",
      key: "plan",
    });
    expect(workflowEditorDraftGraph(deleted).edges[0]?.parameters[0]).toEqual({
      description: "Plan",
      key: "plan",
    });
  });

  it("edits fixed node identity without exposing execution fields", () => {
    const source = {
      ...workflowDefinition,
      nodes: [
        fixedWorkflowNode("node-start", "backlog", "Backlog", "start"),
        fixedWorkflowNode("node-terminal", "done", "Done", "terminal"),
      ],
    };
    const renamedStart = workflowEditorDraftReducer(initializeWorkflowEditorDraft(source), {
      nodeID: "node-start",
      patch: { key: "incoming", name: "Incoming" },
      type: "editNodeIdentity",
    });
    const renamedTerminal = workflowEditorDraftReducer(renamedStart, {
      nodeID: "node-terminal",
      patch: { key: "archived", name: "Archived" },
      type: "editNodeIdentity",
    });

    expect(workflowDefinitionFromDraft(renamedTerminal.draft).nodes).toMatchObject([
      { key: "incoming", kind: "start", name: "Incoming", promptTemplate: "", subagentRole: "" },
      { key: "archived", kind: "terminal", name: "Archived", promptTemplate: "", subagentRole: "" },
    ]);
  });

  it("assigns one join provider per input", () => {
    const baseNode = workflowDefinition.nodes[0];
    if (baseNode === undefined) {
      throw new Error("Expected workflow fixture to include a node.");
    }
    const withJoin = {
      ...workflowDefinition,
      nodes: [...workflowDefinition.nodes, { ...baseNode, id: "node-join", kind: "join" }],
    };
    const assigned = workflowEditorDraftReducer(initializeWorkflowEditorDraft(withJoin), {
      inputName: "plan",
      nodeID: "node-join",
      providerEdgeID: "edge-provider",
      type: "assignJoinInputProvider",
    });
    const reassigned = workflowEditorDraftReducer(assigned, {
      inputName: "plan",
      nodeID: "node-join",
      providerEdgeID: "edge-provider-2",
      type: "assignJoinInputProvider",
    });

    expect(workflowDefinitionFromDraft(reassigned.draft).nodes[1]?.joinInputProviders).toEqual([
      { inputName: "plan", providerEdgeID: "edge-provider-2" },
    ]);
  });

  it("treats join provider assignments as stable mappings instead of order-sensitive rows", () => {
    const baseNode = workflowDefinition.nodes[0];
    if (baseNode === undefined) {
      throw new Error("Expected workflow fixture to include a node.");
    }
    const source = {
      ...workflowDefinition,
      nodes: [
        {
          ...baseNode,
          id: "node-join",
          joinInputProviders: [
            { inputName: "first", providerEdgeID: "edge-first" },
            { inputName: "second", providerEdgeID: "edge-second" },
          ],
          kind: "join",
        },
      ],
    };
    const reassigned = workflowEditorDraftReducer(initializeWorkflowEditorDraft(source), {
      inputName: "first",
      nodeID: "node-join",
      providerEdgeID: "edge-first-updated",
      type: "assignJoinInputProvider",
    });

    expect(workflowDefinitionFromDraft(reassigned.draft).nodes[0]?.joinInputProviders).toEqual([
      { inputName: "first", providerEdgeID: "edge-first-updated" },
      { inputName: "second", providerEdgeID: "edge-second" },
    ]);
    const reorderedNode = source.nodes[0];
    if (reorderedNode === undefined) {
      throw new Error("Expected source fixture to include a node.");
    }
    expect(
      workflowEditorDirtyState(
        initializeWorkflowEditorDraft({
          ...source,
          nodes: [{ ...reorderedNode, joinInputProviders: [...reorderedNode.joinInputProviders].reverse() }],
        }),
      ),
    ).toEqual({ dirty: false, graphDirty: false, metadataDirty: false });
  });

  it("cascades selected-node context source references only when the old key is unique", () => {
    const changed = workflowEditorDraftReducer(initializeWorkflowEditorDraft(workflowDefinition), {
      nodeID: "node-agent",
      patch: { key: "implementation" },
      type: "editAgentNode",
    });
    expect(workflowDefinitionFromDraft(changed.draft).edges[0]?.contextSource.nodeKey).toBe("implementation");

    const firstNode = workflowDefinition.nodes[0];
    if (firstNode === undefined) {
      throw new Error("Expected workflow fixture to include an agent node.");
    }
    const duplicateSource = {
      ...workflowDefinition,
      nodes: [...workflowDefinition.nodes, { ...firstNode, id: "node-agent-2" }],
    };
    const duplicateChanged = workflowEditorDraftReducer(initializeWorkflowEditorDraft(duplicateSource), {
      nodeID: "node-agent",
      patch: { key: "implementation" },
      type: "editAgentNode",
    });
    expect(workflowDefinitionFromDraft(duplicateChanged.draft).edges[0]?.contextSource.nodeKey).toBe(
      "implement",
    );
  });

  it("cascades selected-node context source references for fixed node key edits", () => {
    const edge = workflowDefinition.edges[0];
    if (edge === undefined) {
      throw new Error("Expected workflow fixture to include an edge.");
    }
    const terminalSource = {
      ...workflowDefinition,
      nodes: [
        {
          groupID: "",
          groupKey: "",
          id: "node-terminal",
          inputFields: [],
          joinInputProviders: [],
          key: "done",
          kind: "terminal",
          name: "Done",
          completionMode: "",
          outputFields: [],
          promptTemplate: "",
          subagentRole: "",
          workflowID: "workflow-1",
        },
      ],
      edges: [
        {
          ...edge,
          contextSource: { kind: "selected_node", nodeKey: "done" },
        },
      ],
    } satisfies WorkflowDefinition;
    const changed = workflowEditorDraftReducer(initializeWorkflowEditorDraft(terminalSource), {
      nodeID: "node-terminal",
      patch: { key: "archived" },
      type: "editNodeIdentity",
    });

    expect(workflowDefinitionFromDraft(changed.draft).edges[0]?.contextSource.nodeKey).toBe("archived");
  });

  it("acknowledges a dirty remote conflict until another remote revision arrives", () => {
    const dirty = workflowEditorDraftReducer(initializeWorkflowEditorDraft(workflowDefinition), {
      description: "Local",
      name: "Workflow",
      type: "editWorkflowMetadata",
    });
    const conflict = workflowEditorDraftReducer(dirty, {
      source: withVersion(workflowDefinition, 2),
      type: "conflict",
    });
    const kept = workflowEditorDraftReducer(conflict, { type: "keepEditing" });

    expect(kept.conflict).toBeNull();
    expect(kept.acknowledgedConflictVersion).toBe(2);
  });

  it("applies topology mutations through reducer and records mutation metadata", () => {
    const added = workflowEditorDraftReducer(initializeWorkflowEditorDraft(workflowDefinition), {
      input: { id: "workflow-node-review", kind: "agent", name: "Review" },
      type: "addNode",
    });
    const connected = workflowEditorDraftReducer(added, {
      input: {
        edgeID: "workflow-edge-review",
        sourceNodeID: "node-agent",
        targetNodeID: "workflow-node-review",
        transitionGroupID: "workflow-transition-group-review",
      },
      type: "connectNodes",
    });
    const deleted = workflowEditorDraftReducer(connected, {
      edgeID: "workflow-edge-review",
      type: "deleteEdge",
    });

    expect(added.lastTopologyMutation?.nextSelection).toEqual({
      kind: "node",
      nodeID: "workflow-node-review",
    });
    expect(connected.draft.edges.some((edge) => edge.id === "workflow-edge-review")).toBe(true);
    expect(deleted.lastTopologyMutation?.summary).toEqual({
      removedEdgeIDs: ["workflow-edge-review"],
      removedNodeIDs: [],
      removedTransitionGroupIDs: ["workflow-transition-group-review"],
    });
  });

  it("adds a connected node in one graph version increment", () => {
    const initial = initializeWorkflowEditorDraft(workflowDefinition);

    const added = workflowEditorDraftReducer(initial, {
      input: {
        edgeID: "workflow-edge-review",
        kind: "agent",
        nodeID: "workflow-node-review",
        sourceNodeID: "node-agent",
        transitionGroupID: "workflow-transition-group-review",
      },
      type: "addConnectedNode",
    });

    expect(added.version).toBe(initial.version + 1);
    expect(added.graphVersion).toBe(initial.graphVersion + 1);
    expect(added.draft.nodes.some((node) => node.id === "workflow-node-review")).toBe(true);
    expect(added.draft.edges.some((edge) => edge.id === "workflow-edge-review")).toBe(true);
    expect(added.lastTopologyMutation?.nextSelection).toEqual({
      edgeID: "workflow-edge-review",
      kind: "edge",
    });
  });
});

function withVersion(source: WorkflowDefinition, version: number): WorkflowDefinition {
  return { ...source, workflow: { ...source.workflow, version } };
}

function fixedWorkflowNode(id: string, key: string, name: string, kind: "start" | "terminal") {
  const source = workflowDefinition.nodes[0];
  if (source === undefined) throw new Error("Expected workflow fixture to include a node.");
  return {
    ...source,
    completionMode: "",
    id,
    inputFields: [],
    key,
    kind,
    name,
    outputFields: [],
    promptTemplate: "",
    subagentRole: "",
  };
}

function expectDefined<T>(value: T | undefined, message: string): T {
  if (value === undefined) {
    throw new Error(message);
  }
  return value;
}

const workflowDefinition: WorkflowDefinition = {
  workflow: {
    description: "",
    version: 1,
    id: "workflow-1",
    name: "Workflow",
  },
  nodeGroups: [],
  nodes: [
    {
      groupID: "",
      groupKey: "",
      id: "node-agent",
      key: "implement",
      kind: "agent",
      name: "Implement",
      completionMode: "",
      inputFields: [],
      joinInputProviders: [],
      outputFields: [{ description: "Summary", name: "summary" }],
      promptTemplate: "Do work.",
      subagentRole: "coder",
      workflowID: "workflow-1",
    },
  ],
  transitionGroups: [
    {
      description: "",
      id: "group-1",
      name: "Done",
      sourceNodeID: "node-agent",
      transitionID: "done",
      workflowID: "workflow-1",
    },
  ],
  edges: [
    {
      contextMode: "continue_session",
      contextSource: { kind: "selected_node", nodeKey: "implement" },
      id: "edge-1",
      inputBindings: [],
      key: "done",
      outputRequirements: [],
      parameters: [],
      promptTemplate: "",
      requiresApproval: false,
      targetNodeID: "node-agent",
      transitionGroupID: "group-1",
      workflowID: "workflow-1",
    },
  ],
  derivedWiring: emptyWorkflowDerivedWiring,
};
