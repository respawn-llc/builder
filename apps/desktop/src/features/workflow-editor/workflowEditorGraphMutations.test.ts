import { describe, expect, it } from "vitest";

import type { WorkflowDefinition } from "../../api";
import { draftDefinitionFromSource } from "./workflowEditorDraft";
import {
  edgesForTransition,
  groupableWorkflowDefinition,
  joinWorkflowDefinition,
  workflowDefinition,
} from "../../test-support/workflow-editor/workflowEditorGraphMutationFixtures";
import {
  addWorkflowNode,
  addWorkflowNodeToGroup,
  connectWorkflowNodes,
  createWorkflowNodeGroupFromNode,
  deleteWorkflowEdge,
  deleteWorkflowNode,
  deleteWorkflowNodeGroup,
  editWorkflowEdgeRoute,
  extractWorkflowNodeFromGroup,
  reconnectWorkflowEdge,
  removeWorkflowNodeFromGroup,
} from "./workflowEditorGraphMutations";

describe("workflowEditorGraphMutations", () => {
  it("adds executable and terminal nodes", () => {
    const agent = addWorkflowNode(draftDefinitionFromSource(workflowDefinition), {
      id: "workflow-node-agent",
      kind: "agent",
      name: "Review",
    });
    const script = addWorkflowNode(agent.draft, {
      id: "workflow-node-script",
      kind: "script",
      name: "Generate",
    });
    const terminal = addWorkflowNode(script.draft, {
      id: "workflow-node-terminal",
      kind: "terminal",
      name: "Archived",
    });

    expect(agent.draft.nodes.find((node) => node.id === "workflow-node-agent")).toMatchObject({
      key: "review",
      kind: "agent",
      subagentRole: "default",
    });
    expect(script.draft.nodes.find((node) => node.id === "workflow-node-script")).toMatchObject({
      key: "generate",
      kind: "script",
      scriptPath: null,
      subagentRole: "",
    });
    expect(terminal.draft.nodes.find((node) => node.id === "workflow-node-terminal")).toMatchObject({
      key: "archived",
      kind: "terminal",
      subagentRole: "",
    });
  });

  it("connects nodes with a new transition group and edge", () => {
    const connected = connectWorkflowNodes(draftDefinitionFromSource(workflowDefinition), {
      edgeID: "workflow-edge-review",
      sourceNodeID: "node-agent",
      targetNodeID: "node-done",
      transitionGroupID: "workflow-transition-group-review",
    });

    expect(connected.draft.transitionGroups.at(-1)).toMatchObject({
      id: "workflow-transition-group-review",
      sourceNodeID: "node-agent",
      transitionID: "done_2",
    });
    expect(connected.draft.edges.at(-1)).toMatchObject({
      contextMode: "new_session",
      id: "workflow-edge-review",
      key: "done",
      targetNodeID: "node-done",
      transitionGroupID: "workflow-transition-group-review",
    });
  });

  it("reuses a source fan-out transition group when dragging into another branch of the same node group", () => {
    const draft = draftDefinitionFromSource(parallelBranchWorkflowDefinition());

    const connected = connectWorkflowNodes(draft, {
      edgeID: "workflow-edge-review",
      sourceNodeID: "node-source",
      targetNodeID: "node-review",
      transitionGroupID: "workflow-transition-group-review",
    });

    expect(connected.draft.transitionGroups.some((group) => group.id === "workflow-transition-group-review")).toBe(false);
    expect(edgesForTransition(connected.draft, "group-source-agent")).toMatchObject([
      { id: "edge-source-agent", targetNodeID: "node-agent" },
      { id: "workflow-edge-review", key: "review", targetNodeID: "node-review" },
    ]);
  });

  it("moves an existing single-branch transition into the source fan-out group when redragging the branch edge", () => {
    const draft = draftDefinitionFromSource(
      parallelBranchWorkflowDefinition({ separateReviewTransition: true }),
    );

    const connected = connectWorkflowNodes(draft, {
      edgeID: "workflow-edge-review-duplicate",
      sourceNodeID: "node-source",
      targetNodeID: "node-review",
      transitionGroupID: "workflow-transition-group-review-duplicate",
    });

    expect(connected.draft.edges.some((edge) => edge.id === "workflow-edge-review-duplicate")).toBe(false);
    expect(connected.draft.transitionGroups.some((group) => group.id === "group-source-review")).toBe(false);
    expect(connected.summary.removedTransitionGroupIDs).toEqual(["group-source-review"]);
    expect(edgesForTransition(connected.draft, "group-source-agent")).toMatchObject([
      { id: "edge-source-agent", targetNodeID: "node-agent" },
      { id: "edge-source-review", key: "custom_review_edge", targetNodeID: "node-review" },
    ]);
  });

  it("creates a new transition group instead of reusing a partial branch group in larger node groups", () => {
    const draft = draftDefinitionFromSource(parallelBranchWorkflowDefinition({ auditBranch: true }));

    const connected = connectWorkflowNodes(draft, {
      edgeID: "workflow-edge-review",
      sourceNodeID: "node-source",
      targetNodeID: "node-review",
      transitionGroupID: "workflow-transition-group-review",
    });

    expect(connected.draft.transitionGroups.some((group) => group.id === "workflow-transition-group-review")).toBe(true);
    expect(edgesForTransition(connected.draft, "group-source-agent")).toMatchObject([
      { id: "edge-source-agent", targetNodeID: "node-agent" },
    ]);
    expect(edgesForTransition(connected.draft, "workflow-transition-group-review")).toMatchObject([
      { id: "workflow-edge-review", targetNodeID: "node-review" },
    ]);
  });

  it("allows draft edges from start while preserving graph shape for validation", () => {
    const connected = connectWorkflowNodes(draftDefinitionFromSource(workflowDefinition), {
      edgeID: "workflow-edge-start-extra",
      sourceNodeID: "node-start",
      targetNodeID: "node-agent",
      transitionGroupID: "workflow-transition-group-start-extra",
    });

    expect(connected.warnings).toEqual([]);
    expect(connected.draft.transitionGroups.at(-1)?.sourceNodeID).toBe("node-start");
  });

  it("blocks draft edges into start nodes", () => {
    const draft = draftDefinitionFromSource(workflowDefinition);

    const connected = connectWorkflowNodes(draft, {
      edgeID: "workflow-edge-into-start",
      sourceNodeID: "node-agent",
      targetNodeID: "node-start",
      transitionGroupID: "workflow-transition-group-into-start",
    });

    expect(connected.warnings).toEqual(["start nodes cannot have incoming edges"]);
    expect(connected.draft).toBe(draft);
  });

  it("deletes final edge and removes its transition group", () => {
    const deleted = deleteWorkflowEdge(draftDefinitionFromSource(workflowDefinition), "edge-done");

    expect(deleted.draft.edges.some((edge) => edge.id === "edge-done")).toBe(false);
    expect(deleted.draft.transitionGroups.some((group) => group.id === "group-done")).toBe(false);
    expect(deleted.summary).toEqual({
      removedEdgeIDs: ["edge-done"],
      removedNodeIDs: [],
      removedTransitionGroupIDs: ["group-done"],
    });
  });

  it("deletes nodes with incident edges, transition groups, join providers, and selected context sources", () => {
    const deleted = deleteWorkflowNode(draftDefinitionFromSource(joinWorkflowDefinition), "node-branch-a");

    expect(deleted.draft.nodes.some((node) => node.id === "node-branch-a")).toBe(false);
    expect(deleted.draft.edges.map((edge) => edge.id)).not.toContain("edge-branch-a-join");
    expect(deleted.draft.nodes.find((node) => node.id === "node-join")?.joinInputProviders).toEqual([
      { inputName: "risk", providerEdgeID: "edge-branch-b-join" },
    ]);
    expect(deleted.draft.edges.find((edge) => edge.id === "edge-join-done")?.contextSource).toEqual({
      kind: "immediate_source",
      nodeKey: "",
    });
  });

  it("deletes grouped branches by dissolving groups that would become invalid", () => {
    const expanded = inferredTwoBranchGroupDraft();

    const deleted = deleteWorkflowNode(expanded, "node-review");

    expect(deleted.draft.nodes.some((node) => node.id === "node-review")).toBe(false);
    expect(deleted.draft.nodes.some((node) => node.id === "node-join")).toBe(false);
    expect(deleted.draft.nodeGroups.some((group) => group.id === "workflow-node-group-parallel")).toBe(false);
    expect(edgesForTransition(deleted.draft, "group-source-agent")).toMatchObject([
      { targetNodeID: "node-agent" },
    ]);
    expect(deleted.draft.transitionGroups.find((group) => group.id === "group-done")).toMatchObject({
      sourceNodeID: "node-agent",
    });
    expect(deleted.summary.removedNodeIDs).toEqual(["node-review", "node-join"]);
  });

  it("deletes grouped joins by dissolving their node group", () => {
    const expanded = inferredTwoBranchGroupDraft();

    const deleted = deleteWorkflowNode(expanded, "node-join");

    expect(deleted.draft.nodes.some((node) => node.id === "node-join")).toBe(false);
    expect(deleted.draft.nodes.find((node) => node.id === "node-agent")).toMatchObject({
      groupID: "",
      groupKey: "",
    });
    expect(deleted.draft.nodes.find((node) => node.id === "node-review")).toMatchObject({
      groupID: "",
      groupKey: "",
    });
    expect(deleted.draft.nodeGroups.some((group) => group.id === "workflow-node-group-parallel")).toBe(false);
    expect(edgesForTransition(deleted.draft, "group-source-agent")).toMatchObject([
      { targetNodeID: "node-agent" },
    ]);
    expect(deleted.draft.transitionGroups.find((group) => group.id === "group-done")).toMatchObject({
      sourceNodeID: "node-agent",
    });
  });

  it("guards start node and last terminal deletion", () => {
    expect(deleteWorkflowNode(draftDefinitionFromSource(workflowDefinition), "node-start").warnings).toEqual([
      "start node cannot be deleted",
    ]);
    expect(deleteWorkflowNode(draftDefinitionFromSource(workflowDefinition), "node-done").warnings).toEqual([
      "last terminal node cannot be deleted",
    ]);
  });

  it("edits edge route facts while keeping derived wiring outside mutation scope", () => {
    const edited = editWorkflowEdgeRoute(draftDefinitionFromSource(workflowDefinition), {
      contextMode: "continue_session",
      contextSource: { kind: "selected_node", nodeKey: "implement" },
      edgeID: "edge-start",
      edgeKey: "implement_again",
      requiresApproval: true,
      transitionID: "start_work",
      transitionName: "Start work",
    });

    expect(edited.draft.edges.find((edge) => edge.id === "edge-start")).toMatchObject({
      contextMode: "continue_session",
      contextSource: { kind: "selected_node", nodeKey: "implement" },
      key: "implement_again",
      requiresApproval: true,
      targetNodeID: "node-agent",
    });
    expect(edited.draft.transitionGroups.find((group) => group.id === "group-start")).toMatchObject({
      name: "Start work",
      transitionID: "start_work",
    });
  });

  it("reconnects a transition target while preserving transition identity and route config", () => {
    const draft = draftDefinitionFromSource(configuredReviewTargetWorkflowDefinition());

    const reconnected = reconnectWorkflowEdge(draft, {
      edgeID: "edge-done",
      endpoint: "target",
      targetNodeID: "node-review",
    });

    expect(reconnected.draft.edges).toHaveLength(draft.edges.length);
    expect(reconnected.draft.transitionGroups).toHaveLength(draft.transitionGroups.length);
    expect(reconnected.draft.edges.find((edge) => edge.id === "edge-done")).toMatchObject({
      contextMode: "continue_session",
      contextSource: { kind: "selected_node", nodeKey: "implement" },
      id: "edge-done",
      key: "custom_done",
      parameters: [{ description: "Review notes", key: "review_notes" }],
      promptTemplate: "Review the implementation.",
      requiresApproval: true,
      targetNodeID: "node-review",
      transitionGroupID: "group-done",
    });
    expect(reconnected.draft.transitionGroups.find((group) => group.id === "group-done")).toMatchObject({
      name: "Done",
      sourceNodeID: "node-agent",
      transitionID: "done",
    });
    expect(reconnected.nextSelection).toEqual({ edgeID: "edge-done", kind: "edge" });
    expect(reconnected.warnings).toEqual([]);
  });

  it("reconnects a single-branch transition source by moving its transition group", () => {
    const draft = draftDefinitionFromSource(configuredReviewTargetWorkflowDefinition());

    const reconnected = reconnectWorkflowEdge(draft, {
      edgeID: "edge-done",
      endpoint: "source",
      sourceNodeID: "node-review",
    });

    expect(reconnected.draft.edges.find((edge) => edge.id === "edge-done")).toMatchObject({
      contextMode: "continue_session",
      contextSource: { kind: "selected_node", nodeKey: "implement" },
      key: "custom_done",
      parameters: [{ description: "Review notes", key: "review_notes" }],
      promptTemplate: "Review the implementation.",
      requiresApproval: true,
      targetNodeID: "node-done",
      transitionGroupID: "group-done",
    });
    expect(reconnected.draft.transitionGroups.find((group) => group.id === "group-done")).toMatchObject({
      sourceNodeID: "node-review",
      transitionID: "done",
    });
    expect(reconnected.nextSelection).toEqual({ edgeID: "edge-done", kind: "edge" });
    expect(reconnected.warnings).toEqual([]);
  });

  it("rejects source reconnect for fan-out transition branches", () => {
    const draft = inferredTwoBranchGroupDraft();

    const reconnected = reconnectWorkflowEdge(draft, {
      edgeID: "edge-start-review",
      endpoint: "source",
      sourceNodeID: "node-review",
    });

    expect(reconnected.draft).toBe(draft);
    expect(reconnected.warnings).toEqual(["fan-out transition branches cannot reconnect their source"]);
  });

  it("creates node groups with a join and updates membership", () => {
    const created = createWorkflowNodeGroupFromNode(draftDefinitionFromSource(joinWorkflowDefinition), {
      groupID: "workflow-node-group-parallel",
      joinNodeID: "workflow-node-join-parallel",
      nodeID: "node-branch-a",
    });
    const expanded = addWorkflowNodeToGroup(created.draft, {
      groupID: "workflow-node-group-parallel",
      nodeID: "node-branch-b",
    });
    const removed = removeWorkflowNodeFromGroup(expanded.draft, "node-branch-b");

    expect(created.draft.nodeGroups.at(-1)).toMatchObject({
      id: "workflow-node-group-parallel",
      key: "branch_a_parallel",
      nodeIDs: ["node-branch-a", "workflow-node-join-parallel"],
    });
    expect(created.draft.nodes.find((node) => node.id === "workflow-node-join-parallel")).toMatchObject({
      groupID: "workflow-node-group-parallel",
      kind: "join",
    });
    expect(expanded.draft.nodeGroups.find((group) => group.id === "workflow-node-group-parallel")?.nodeIDs).toEqual([
      "node-branch-a",
      "node-branch-b",
      "workflow-node-join-parallel",
    ]);
    expect(removed.draft.nodes.find((node) => node.id === "node-branch-a")?.groupID).toBe("");
    expect(removed.draft.nodes.find((node) => node.id === "node-branch-b")?.groupID).toBe("");
    expect(removed.draft.nodeGroups.some((group) => group.id === "workflow-node-group-parallel")).toBe(false);
  });

  it("safely infers v1 node group fan-out and join topology when adding an unconnected branch", () => {
    const withBranch = addWorkflowNode(draftDefinitionFromSource(groupableWorkflowDefinition), {
      id: "node-review",
      kind: "agent",
      name: "Review",
    });
    const created = createWorkflowNodeGroupFromNode(withBranch.draft, {
      groupID: "workflow-node-group-parallel",
      joinNodeID: "node-join",
      nodeID: "node-agent",
    });
    const expanded = addWorkflowNodeToGroup(created.draft, {
      groupID: "workflow-node-group-parallel",
      inferredTopologyIDs: {
        addedBranchJoinEdgeID: "edge-review-join",
        addedBranchJoinTransitionGroupID: "group-review-join",
        existingBranchJoinEdgeID: "edge-implement-join",
        existingBranchJoinTransitionGroupID: "group-implement-join",
        fanoutEdgeID: "edge-start-review",
      },
      nodeID: "node-review",
    });

    expect(edgesForTransition(expanded.draft, "group-source-agent").map((edge) => edge.targetNodeID).sort()).toEqual([
      "node-agent",
      "node-review",
    ]);
    expect(expanded.draft.transitionGroups.find((group) => group.id === "group-done")?.sourceNodeID).toBe("node-join");
    expect(expanded.draft.edges.find((edge) => edge.id === "edge-implement-join")).toMatchObject({
      targetNodeID: "node-join",
      transitionGroupID: "group-implement-join",
    });
    expect(expanded.draft.edges.find((edge) => edge.id === "edge-review-join")).toMatchObject({
      targetNodeID: "node-join",
      transitionGroupID: "group-review-join",
    });
    expect(expanded.draft.transitionGroups.find((group) => group.id === "group-implement-join")).toMatchObject({
      sourceNodeID: "node-agent",
      transitionID: "implement_parallel_join",
    });
    expect(expanded.draft.transitionGroups.find((group) => group.id === "group-review-join")).toMatchObject({
      sourceNodeID: "node-review",
      transitionID: "implement_parallel_join",
    });
  });

  it("resets rehomed downstream routes to new session when inferring node group topology", () => {
    const draft = draftDefinitionFromSource({
      ...groupableWorkflowDefinition,
      edges: groupableWorkflowDefinition.edges.map((edge) =>
        edge.id === "edge-done"
          ? {
              ...edge,
              contextMode: "continue_session",
              contextSource: { kind: "immediate_source", nodeKey: "" },
            }
          : edge,
      ),
    });
    const withBranch = addWorkflowNode(draft, {
      id: "node-review",
      kind: "agent",
      name: "Review",
    });
    const created = createWorkflowNodeGroupFromNode(withBranch.draft, {
      groupID: "workflow-node-group-parallel",
      joinNodeID: "node-join",
      nodeID: "node-agent",
    });

    const expanded = addWorkflowNodeToGroup(created.draft, {
      groupID: "workflow-node-group-parallel",
      inferredTopologyIDs: {
        addedBranchJoinEdgeID: "edge-review-join",
        addedBranchJoinTransitionGroupID: "group-review-join",
        existingBranchJoinEdgeID: "edge-implement-join",
        existingBranchJoinTransitionGroupID: "group-implement-join",
        fanoutEdgeID: "edge-start-review",
      },
      nodeID: "node-review",
    });

    expect(expanded.draft.transitionGroups.find((group) => group.id === "group-done")).toMatchObject({
      sourceNodeID: "node-join",
    });
    expect(expanded.draft.edges.find((edge) => edge.id === "edge-done")).toMatchObject({
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
    });
  });

  it("adds a branch with a warning when node group topology cannot be inferred safely", () => {
    const withBranch = addWorkflowNode(draftDefinitionFromSource(workflowDefinition), {
      id: "node-review",
      kind: "agent",
      name: "Review",
    });
    const created = createWorkflowNodeGroupFromNode(withBranch.draft, {
      groupID: "workflow-node-group-parallel",
      joinNodeID: "node-join",
      nodeID: "node-agent",
    });

    const expanded = addWorkflowNodeToGroup(created.draft, {
      groupID: "workflow-node-group-parallel",
      inferredTopologyIDs: {
        addedBranchJoinEdgeID: "edge-review-join",
        addedBranchJoinTransitionGroupID: "group-review-join",
        existingBranchJoinEdgeID: "edge-implement-join",
        existingBranchJoinTransitionGroupID: "group-implement-join",
        fanoutEdgeID: "edge-start-review",
      },
      nodeID: "node-review",
    });

    expect(expanded.warnings).toEqual(["node group topology could not be inferred safely"]);
    expect(expanded.draft.nodes.find((node) => node.id === "node-review")).toMatchObject({
      groupID: "workflow-node-group-parallel",
      groupKey: "implement_parallel",
    });
    expect(expanded.draft.nodeGroups.find((group) => group.id === "workflow-node-group-parallel")?.nodeIDs).toEqual([
      "node-agent",
      "node-review",
      "node-join",
    ]);
  });

  it("blocks moving a grouped branch directly into another group", () => {
    const created = createWorkflowNodeGroupFromNode(draftDefinitionFromSource(workflowDefinition), {
      groupID: "workflow-node-group-parallel",
      joinNodeID: "node-join",
      nodeID: "node-agent",
    });
    const otherGroupDraft = {
      ...created.draft,
      nodeGroups: [
        ...created.draft.nodeGroups,
        {
          id: "workflow-node-group-other",
          key: "other",
          name: "Other",
          nodeIDs: [],
          sortOrder: 100,
          workflowID: "workflow-1",
        },
      ],
    };

    const moved = addWorkflowNodeToGroup(otherGroupDraft, {
      groupID: "workflow-node-group-other",
      nodeID: "node-agent",
    });

    expect(moved.warnings).toEqual(["node already belongs to a node group"]);
    expect(moved.draft).toBe(otherGroupDraft);
  });

  it("extracts a branch from a larger node group while preserving valid fan-out topology", () => {
    const expanded = withJoinProvider(
      withSourceTransitionIDConflict(inferredThreeBranchGroupDraft()),
      "node-join",
      "risk",
      "edge-review-join",
    );

    const extracted = extractWorkflowNodeFromGroup(expanded, {
      rehomedIncomingTransitionGroupID: "group-review-extracted",
      nodeID: "node-review",
    });

    expect(extracted.draft.nodes.find((node) => node.id === "node-review")).toMatchObject({
      groupID: "",
      groupKey: "",
    });
    expect(
      [
        ...(extracted.draft.nodeGroups.find((group) => group.id === "workflow-node-group-parallel")?.nodeIDs ?? []),
      ].sort(),
    ).toEqual(["node-agent", "node-audit", "node-join"]);
    expect(edgesForTransition(extracted.draft, "group-source-agent").map((edge) => edge.targetNodeID).sort()).toEqual([
      "node-agent",
      "node-audit",
    ]);
    expect(extracted.draft.edges.find((edge) => edge.id === "edge-start-review")).toMatchObject({
      targetNodeID: "node-review",
      transitionGroupID: "group-review-extracted",
    });
    expect(extracted.draft.transitionGroups.find((group) => group.id === "group-review-extracted")).toMatchObject({
      name: "Review",
      sourceNodeID: "node-source",
      transitionID: "review_2",
    });
    expect(extracted.draft.edges.some((edge) => edge.id === "edge-review-join")).toBe(false);
    expect(extracted.draft.transitionGroups.some((group) => group.id === "group-review-join")).toBe(false);
    expect(extracted.draft.nodes.find((node) => node.id === "node-join")?.joinInputProviders).toEqual([]);
    expect(extracted.summary).toEqual({
      removedEdgeIDs: ["edge-review-join"],
      removedNodeIDs: [],
      removedTransitionGroupIDs: ["group-review-join"],
    });
  });

  it("removes only extracted branch join edges from mixed transition groups", () => {
    const base = inferredThreeBranchGroupDraft();
    const expanded = {
      ...base,
      edges: [
        ...base.edges,
        {
          contextMode: "new_session",
          contextSource: { kind: "immediate_source", nodeKey: "" },
          id: "edge-review-extra",
          inputBindings: [],
          key: "extra",
          outputRequirements: [],
          parameters: [],
          promptTemplate: "",
          requiresApproval: false,
          targetNodeID: "node-done",
          transitionGroupID: "group-review-join",
          workflowID: "workflow-1",
        },
      ],
    };

    const extracted = extractWorkflowNodeFromGroup(expanded, {
      rehomedIncomingTransitionGroupID: "group-review-extracted",
      nodeID: "node-review",
    });

    expect(extracted.draft.edges.some((edge) => edge.id === "edge-review-join")).toBe(false);
    expect(extracted.draft.edges.find((edge) => edge.id === "edge-review-extra")).toMatchObject({
      targetNodeID: "node-done",
      transitionGroupID: "group-review-join",
    });
    expect(extracted.draft.transitionGroups.some((group) => group.id === "group-review-join")).toBe(true);
    expect(extracted.summary.removedTransitionGroupIDs).toEqual([]);
  });

  it("blocks extraction when the incoming fan-out cannot be identified safely", () => {
    const draft = withDuplicateExactBranchFanout(inferredThreeBranchGroupDraft());

    const extracted = extractWorkflowNodeFromGroup(draft, {
      rehomedIncomingTransitionGroupID: "group-review-extracted",
      nodeID: "node-review",
    });

    expect(extracted.draft).toBe(draft);
    expect(extracted.warnings).toEqual(["node group extraction topology could not be inferred safely"]);
    expect(extracted.summary).toEqual({
      removedEdgeIDs: [],
      removedNodeIDs: [],
      removedTransitionGroupIDs: [],
    });
  });

  it("blocks extraction when a fan-out duplicates one branch and omits another branch", () => {
    const draft = withDuplicateMissingBranchFanout(inferredThreeBranchGroupDraft());

    const extracted = extractWorkflowNodeFromGroup(draft, {
      rehomedIncomingTransitionGroupID: "group-review-extracted",
      nodeID: "node-review",
    });

    expect(extracted.draft).toBe(draft);
    expect(extracted.warnings).toEqual(["node group extraction topology could not be inferred safely"]);
    expect(extracted.summary).toEqual({
      removedEdgeIDs: [],
      removedNodeIDs: [],
      removedTransitionGroupIDs: [],
    });
  });

  it("infers v1 node group topology when adding another branch to an existing valid group", () => {
    const withReview = addWorkflowNode(draftDefinitionFromSource(groupableWorkflowDefinition), {
      id: "node-review",
      kind: "agent",
      name: "Review",
    });
    const created = createWorkflowNodeGroupFromNode(withReview.draft, {
      groupID: "workflow-node-group-parallel",
      joinNodeID: "node-join",
      nodeID: "node-agent",
    });
    const withTwoBranches = addWorkflowNodeToGroup(created.draft, {
      groupID: "workflow-node-group-parallel",
      inferredTopologyIDs: {
        addedBranchJoinEdgeID: "edge-review-join",
        addedBranchJoinTransitionGroupID: "group-review-join",
        existingBranchJoinEdgeID: "edge-implement-join",
        existingBranchJoinTransitionGroupID: "group-implement-join",
        fanoutEdgeID: "edge-start-review",
      },
      nodeID: "node-review",
    });
    const withAudit = addWorkflowNode(withTwoBranches.draft, {
      id: "node-audit",
      kind: "agent",
      name: "Audit",
    });

    const withThreeBranches = addWorkflowNodeToGroup(withAudit.draft, {
      groupID: "workflow-node-group-parallel",
      inferredTopologyIDs: {
        addedBranchJoinEdgeID: "edge-audit-join",
        addedBranchJoinTransitionGroupID: "group-audit-join",
        existingBranchJoinEdgeID: "edge-unused-existing-join",
        existingBranchJoinTransitionGroupID: "group-unused-existing-join",
        fanoutEdgeID: "edge-start-audit",
      },
      nodeID: "node-audit",
    });

    expect(edgesForTransition(withThreeBranches.draft, "group-source-agent").map((edge) => edge.targetNodeID).sort()).toEqual([
      "node-agent",
      "node-audit",
      "node-review",
    ]);
    expect(edgesForTransition(withThreeBranches.draft, "group-audit-join")).toMatchObject([
      { id: "edge-audit-join", targetNodeID: "node-join" },
    ]);
    expect(withThreeBranches.draft.transitionGroups.find((group) => group.id === "group-audit-join")).toMatchObject({
      sourceNodeID: "node-audit",
      transitionID: "implement_parallel_join",
    });
    expect(withThreeBranches.draft.edges.some((edge) => edge.id === "edge-unused-existing-join")).toBe(false);
  });

  it("guards join nodes from ungrouping", () => {
    const created = createWorkflowNodeGroupFromNode(draftDefinitionFromSource(workflowDefinition), {
      groupID: "workflow-node-group-parallel",
      joinNodeID: "node-join",
      nodeID: "node-agent",
    });

    const removed = removeWorkflowNodeFromGroup(created.draft, "node-join");

    expect(removed.warnings).toEqual(["node group membership can be changed for agent nodes only"]);
    expect(removed.draft).toBe(created.draft);
  });

  it("deletes node groups by ungrouping branch nodes and cascading owned join rows", () => {
    const created = createWorkflowNodeGroupFromNode(draftDefinitionFromSource(joinWorkflowDefinition), {
      groupID: "workflow-node-group-parallel",
      joinNodeID: "workflow-node-join-parallel",
      nodeID: "node-branch-a",
    });
    const expanded = addWorkflowNodeToGroup(created.draft, {
      groupID: "workflow-node-group-parallel",
      nodeID: "node-branch-b",
    });

    const deleted = deleteWorkflowNodeGroup(expanded.draft, "workflow-node-group-parallel");

    expect(deleted.draft.nodeGroups.some((group) => group.id === "workflow-node-group-parallel")).toBe(false);
    expect(deleted.draft.nodes.find((node) => node.id === "node-branch-a")).toMatchObject({
      groupID: "",
      groupKey: "",
    });
    expect(deleted.draft.nodes.find((node) => node.id === "node-branch-b")).toMatchObject({
      groupID: "",
      groupKey: "",
    });
    expect(deleted.draft.nodes.some((node) => node.id === "workflow-node-join-parallel")).toBe(false);
    expect(deleted.summary.removedNodeIDs).toEqual(["workflow-node-join-parallel"]);
  });

  it("deletes inferred node groups while preserving downstream wiring through one branch", () => {
    const expanded = inferredTwoBranchGroupDraft();

    const deleted = deleteWorkflowNodeGroup(expanded, "workflow-node-group-parallel");

    expect(edgesForTransition(deleted.draft, "group-source-agent")).toMatchObject([
      { targetNodeID: "node-agent" },
    ]);
    expect(deleted.draft.transitionGroups.find((group) => group.id === "group-done")).toMatchObject({
      sourceNodeID: "node-agent",
    });
    expect(deleted.draft.nodes.some((node) => node.id === "node-join")).toBe(false);
    expect(deleted.draft.edges.some((edge) => edge.targetNodeID === "node-join")).toBe(false);
    expect([...deleted.summary.removedEdgeIDs].sort()).toEqual([
      "edge-implement-join",
      "edge-review-join",
      "edge-start-review",
    ]);
  });

  it("deletes inferred node groups with script branches in the fan-out set", () => {
    const expanded = withScriptBranch(inferredTwoBranchGroupDraft(), "node-review");

    const deleted = deleteWorkflowNodeGroup(expanded, "workflow-node-group-parallel");

    expect(edgesForTransition(deleted.draft, "group-source-agent")).toMatchObject([
      { targetNodeID: "node-agent" },
    ]);
    expect(deleted.draft.edges.some((edge) => edge.id === "edge-start-review")).toBe(false);
    expect([...deleted.summary.removedEdgeIDs].sort()).toEqual([
      "edge-implement-join",
      "edge-review-join",
      "edge-start-review",
    ]);
  });

  it("dissolves node groups instead of leaving a single remaining branch", () => {
    const withBranch = addWorkflowNode(draftDefinitionFromSource(groupableWorkflowDefinition), {
      id: "node-review",
      kind: "agent",
      name: "Review",
    });
    const created = createWorkflowNodeGroupFromNode(withBranch.draft, {
      groupID: "workflow-node-group-parallel",
      joinNodeID: "node-join",
      nodeID: "node-agent",
    });
    const expanded = addWorkflowNodeToGroup(created.draft, {
      groupID: "workflow-node-group-parallel",
      inferredTopologyIDs: {
        addedBranchJoinEdgeID: "edge-review-join",
        addedBranchJoinTransitionGroupID: "group-review-join",
        existingBranchJoinEdgeID: "edge-implement-join",
        existingBranchJoinTransitionGroupID: "group-implement-join",
        fanoutEdgeID: "edge-start-review",
      },
      nodeID: "node-review",
    });

    const removed = removeWorkflowNodeFromGroup(expanded.draft, "node-review");

    expect(removed.draft.nodeGroups.some((group) => group.id === "workflow-node-group-parallel")).toBe(false);
    expect(removed.draft.nodes.find((node) => node.id === "node-agent")).toMatchObject({
      groupID: "",
      groupKey: "",
    });
    expect(removed.draft.nodes.find((node) => node.id === "node-review")).toMatchObject({
      groupID: "",
      groupKey: "",
    });
    expect(removed.draft.nodes.some((node) => node.id === "node-join")).toBe(false);
    expect(edgesForTransition(removed.draft, "group-source-agent")).toMatchObject([
      { targetNodeID: "node-agent" },
    ]);
    expect(removed.draft.transitionGroups.find((group) => group.id === "group-done")).toMatchObject({
      sourceNodeID: "node-agent",
    });
    expect(removed.summary.removedNodeIDs).toEqual(["node-join"]);
  });

  it("extracts from two-branch groups without losing the extracted branch incoming edge", () => {
    const expanded = inferredTwoBranchGroupDraft();

    const extracted = extractWorkflowNodeFromGroup(expanded, {
      rehomedIncomingTransitionGroupID: "group-review-extracted",
      nodeID: "node-review",
    });

    expect(extracted.draft.nodeGroups.some((group) => group.id === "workflow-node-group-parallel")).toBe(false);
    expect(extracted.draft.nodes.find((node) => node.id === "node-review")).toMatchObject({
      groupID: "",
      groupKey: "",
    });
    expect(extracted.draft.edges.find((edge) => edge.id === "edge-start-review")).toMatchObject({
      targetNodeID: "node-review",
      transitionGroupID: "group-review-extracted",
    });
    expect(extracted.draft.transitionGroups.find((group) => group.id === "group-review-extracted")).toMatchObject({
      sourceNodeID: "node-source",
      transitionID: "review",
    });
    expect(edgesForTransition(extracted.draft, "group-source-agent")).toMatchObject([
      { targetNodeID: "node-agent" },
    ]);
    expect(extracted.draft.nodes.some((node) => node.id === "node-join")).toBe(false);
    expect(extracted.summary.removedNodeIDs).toEqual(["node-join"]);
  });
});

function inferredTwoBranchGroupDraft() {
  const withBranch = addWorkflowNode(draftDefinitionFromSource(groupableWorkflowDefinition), {
    id: "node-review",
    kind: "agent",
    name: "Review",
  });
  const created = createWorkflowNodeGroupFromNode(withBranch.draft, {
    groupID: "workflow-node-group-parallel",
    joinNodeID: "node-join",
    nodeID: "node-agent",
  });
  return addWorkflowNodeToGroup(created.draft, {
    groupID: "workflow-node-group-parallel",
    inferredTopologyIDs: {
      addedBranchJoinEdgeID: "edge-review-join",
      addedBranchJoinTransitionGroupID: "group-review-join",
      existingBranchJoinEdgeID: "edge-implement-join",
      existingBranchJoinTransitionGroupID: "group-implement-join",
      fanoutEdgeID: "edge-start-review",
    },
    nodeID: "node-review",
  }).draft;
}

function inferredThreeBranchGroupDraft() {
  const withAudit = addWorkflowNode(inferredTwoBranchGroupDraft(), {
    id: "node-audit",
    kind: "agent",
    name: "Audit",
  });
  return addWorkflowNodeToGroup(withAudit.draft, {
    groupID: "workflow-node-group-parallel",
    inferredTopologyIDs: {
      addedBranchJoinEdgeID: "edge-audit-join",
      addedBranchJoinTransitionGroupID: "group-audit-join",
      existingBranchJoinEdgeID: "edge-unused-existing-join",
      existingBranchJoinTransitionGroupID: "group-unused-existing-join",
      fanoutEdgeID: "edge-start-audit",
    },
    nodeID: "node-audit",
  }).draft;
}

function withJoinProvider(
  draft: ReturnType<typeof draftDefinitionFromSource>,
  joinNodeID: string,
  inputName: string,
  providerEdgeID: string,
) {
  return {
    ...draft,
    nodes: draft.nodes.map((node) =>
      node.id === joinNodeID
        ? { ...node, joinInputProviders: [{ inputName, providerEdgeID }] }
        : node,
    ),
  };
}

function withSourceTransitionIDConflict(draft: ReturnType<typeof draftDefinitionFromSource>) {
  return {
    ...draft,
    edges: [
      ...draft.edges,
      {
        contextMode: "new_session",
        contextSource: { kind: "immediate_source", nodeKey: "" },
        id: "edge-source-review-conflict",
        inputBindings: [],
        key: "review_conflict",
        outputRequirements: [],
        parameters: [],
        promptTemplate: "",
        requiresApproval: false,
        targetNodeID: "node-done",
        transitionGroupID: "group-source-review-conflict",
        workflowID: "workflow-1",
      },
    ],
    transitionGroups: [
      ...draft.transitionGroups,
      {
        description: "",
        id: "group-source-review-conflict",
        name: "Review conflict",
        sourceNodeID: "node-source",
        transitionID: "review",
        workflowID: "workflow-1",
      },
    ],
  };
}

function withDuplicateExactBranchFanout(draft: ReturnType<typeof draftDefinitionFromSource>) {
  return {
    ...draft,
    edges: [
      ...draft.edges,
      duplicateFanoutEdge("edge-source-agent-duplicate", "agent_duplicate", "node-agent"),
      duplicateFanoutEdge("edge-source-review-duplicate", "review_duplicate", "node-review"),
      duplicateFanoutEdge("edge-source-audit-duplicate", "audit_duplicate", "node-audit"),
    ],
    transitionGroups: [
      ...draft.transitionGroups,
      {
        description: "",
        id: "group-source-duplicate",
        name: "Duplicate fan-out",
        sourceNodeID: "node-source",
        transitionID: "duplicate",
        workflowID: "workflow-1",
      },
    ],
  };
}

function withDuplicateMissingBranchFanout(draft: ReturnType<typeof draftDefinitionFromSource>) {
  return {
    ...draft,
    edges: draft.edges.map((edge) =>
      edge.id === "edge-start-audit" ? { ...edge, targetNodeID: "node-review" } : edge,
    ),
  };
}

function withScriptBranch(draft: ReturnType<typeof draftDefinitionFromSource>, nodeID: string) {
  return {
    ...draft,
    nodes: draft.nodes.map((node) =>
      node.id === nodeID ? { ...node, kind: "script" as const, scriptPath: "scripts/run" } : node,
    ),
  };
}

function duplicateFanoutEdge(id: string, key: string, targetNodeID: string) {
  return {
    contextMode: "new_session",
    contextSource: { kind: "immediate_source", nodeKey: "" },
    id,
    inputBindings: [],
    key,
    outputRequirements: [],
    parameters: [],
    promptTemplate: "",
    requiresApproval: false,
    targetNodeID,
    transitionGroupID: "group-source-duplicate",
    workflowID: "workflow-1",
  };
}

function parallelBranchWorkflowDefinition(
  options: Readonly<{ auditBranch?: boolean; separateReviewTransition?: boolean }> = {},
): WorkflowDefinition {
  const templateBranch = groupableWorkflowDefinition.nodes.find((node) => node.id === "node-agent");
  if (templateBranch === undefined) {
    throw new Error("groupable workflow fixture must include node-agent");
  }
  const branchNodeIDs =
    options.auditBranch === true
      ? ["node-agent", "node-review", "node-audit"]
      : ["node-agent", "node-review"];
  const auditBranch =
    options.auditBranch === true
      ? [
          {
            ...templateBranch,
            groupID: "group-parallel",
            groupKey: "parallel",
            id: "node-audit",
            key: "audit",
            name: "Audit",
          },
        ]
      : [];
  const base: WorkflowDefinition = {
    ...groupableWorkflowDefinition,
    nodeGroups: [
      {
        id: "group-parallel",
        key: "parallel",
        name: "Parallel",
        nodeIDs: branchNodeIDs,
        sortOrder: 0,
        workflowID: "workflow-1",
      },
    ],
    nodes: [
      ...groupableWorkflowDefinition.nodes.map((node) =>
        node.id === "node-agent"
          ? { ...node, groupID: "group-parallel", groupKey: "parallel" }
          : node,
      ),
      {
        ...templateBranch,
        groupID: "group-parallel",
        groupKey: "parallel",
        id: "node-review",
        key: "review",
        name: "Review",
      },
      ...auditBranch,
    ],
  };
  if (options.separateReviewTransition !== true) {
    return base;
  }
  return {
    ...base,
    edges: [
      ...base.edges,
      {
        contextMode: "new_session",
        contextSource: { kind: "immediate_source", nodeKey: "" },
        id: "edge-source-review",
        inputBindings: [],
        key: "custom_review_edge",
        outputRequirements: [],
        parameters: [],
        promptTemplate: "",
        requiresApproval: false,
        targetNodeID: "node-review",
        transitionGroupID: "group-source-review",
        workflowID: "workflow-1",
      },
    ],
    transitionGroups: [
      ...base.transitionGroups,
      {
        description: "",
        id: "group-source-review",
        name: "Review",
        sourceNodeID: "node-source",
        transitionID: "review",
        workflowID: "workflow-1",
      },
    ],
  };
}

function configuredReviewTargetWorkflowDefinition(): WorkflowDefinition {
  const agent = workflowDefinition.nodes.find((node) => node.id === "node-agent");
  if (agent === undefined) {
    throw new Error("workflow fixture must include node-agent");
  }
  return {
    ...workflowDefinition,
    edges: workflowDefinition.edges.map((edge) =>
      edge.id === "edge-done"
        ? {
            ...edge,
            contextMode: "continue_session",
            contextSource: { kind: "selected_node", nodeKey: "implement" },
            key: "custom_done",
            parameters: [{ description: "Review notes", key: "review_notes" }],
            promptTemplate: "Review the implementation.",
            requiresApproval: true,
          }
        : edge,
    ),
    nodes: [
      ...workflowDefinition.nodes,
      {
        ...agent,
        id: "node-review",
        key: "review",
        name: "Review",
      },
    ],
  };
}
