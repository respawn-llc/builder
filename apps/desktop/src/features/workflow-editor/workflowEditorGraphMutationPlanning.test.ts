import { describe, expect, it, vi } from "vitest";

import type { WorkflowEditorDraftAction } from "./workflowEditorDraft";
import {
  cascadeRowCount,
  cascadeSummaryEquals,
  confirmationOperation,
  dispatchGraphDeletion,
  dispatchPendingGraphMutation,
  type PendingGraphMutation,
  type PendingGraphMutationAction,
} from "./workflowEditorGraphMutationPlanning";
import type { WorkflowEditorCascadeSummary } from "./workflowEditorGraphMutationTypes";

function summary(parts: Partial<WorkflowEditorCascadeSummary> = {}): WorkflowEditorCascadeSummary {
  return {
    removedNodeIDs: [],
    removedEdgeIDs: [],
    removedTransitionGroupIDs: [],
    ...parts,
  };
}

function pendingMutation(action: PendingGraphMutationAction): PendingGraphMutation {
  return {
    action,
    counts: { nodeCount: 0, edgeCount: 0, promptCount: 0, transitionGroupCount: 0 },
    summary: summary(),
  };
}

describe("cascadeRowCount", () => {
  it("totals removed nodes, edges, and transition groups", () => {
    expect(
      cascadeRowCount(
        summary({
          removedNodeIDs: ["node-1", "node-2"],
          removedEdgeIDs: ["edge-1"],
          removedTransitionGroupIDs: ["group-1", "group-2", "group-3"],
        }),
      ),
    ).toBe(6);
  });

  it("is zero for an empty cascade", () => {
    expect(cascadeRowCount(summary())).toBe(0);
  });
});

describe("cascadeSummaryEquals", () => {
  it("treats identical cascades as equal", () => {
    const left = summary({ removedNodeIDs: ["node-1"], removedEdgeIDs: ["edge-1"] });
    const right = summary({ removedNodeIDs: ["node-1"], removedEdgeIDs: ["edge-1"] });
    expect(cascadeSummaryEquals(left, right)).toBe(true);
  });

  it("is order sensitive within each list", () => {
    const left = summary({ removedNodeIDs: ["node-1", "node-2"] });
    const right = summary({ removedNodeIDs: ["node-2", "node-1"] });
    expect(cascadeSummaryEquals(left, right)).toBe(false);
  });

  it("distinguishes cascades of different size", () => {
    const left = summary({ removedEdgeIDs: ["edge-1"] });
    const right = summary({ removedEdgeIDs: ["edge-1", "edge-2"] });
    expect(cascadeSummaryEquals(left, right)).toBe(false);
  });
});

describe("confirmationOperation", () => {
  it("classifies extraction and deletion requests", () => {
    expect(
      confirmationOperation(
        pendingMutation({
          kind: "extract",
          graphVersion: 1,
          input: { nodeID: "node-1", rehomedIncomingTransitionGroupID: "group-1" },
        }),
      ),
    ).toBe("extract");
    expect(
      confirmationOperation(
        pendingMutation({ kind: "delete", selection: { kind: "node", nodeID: "node-1" } }),
      ),
    ).toBe("delete");
  });
});

describe("dispatchGraphDeletion", () => {
  it("maps each selection kind to its draft action", () => {
    const dispatch = vi.fn<(action: WorkflowEditorDraftAction) => void>();

    dispatchGraphDeletion({ kind: "edge", edgeID: "edge-1" }, dispatch);
    dispatchGraphDeletion({ kind: "node", nodeID: "node-1" }, dispatch);
    dispatchGraphDeletion({ kind: "group", groupID: "group-1" }, dispatch);

    expect(dispatch.mock.calls.map(([action]) => action)).toEqual([
      { type: "deleteEdge", edgeID: "edge-1" },
      { type: "deleteNode", nodeID: "node-1" },
      { type: "deleteNodeGroup", groupID: "group-1" },
    ]);
  });
});

describe("dispatchPendingGraphMutation", () => {
  it("delegates delete requests to the selection deletion action", () => {
    const dispatch = vi.fn<(action: WorkflowEditorDraftAction) => void>();

    dispatchPendingGraphMutation(
      pendingMutation({ kind: "delete", selection: { kind: "edge", edgeID: "edge-1" } }),
      dispatch,
    );

    expect(dispatch).toHaveBeenCalledExactlyOnceWith({ type: "deleteEdge", edgeID: "edge-1" });
  });

  it("dispatches the extraction action with its input", () => {
    const dispatch = vi.fn<(action: WorkflowEditorDraftAction) => void>();
    const input = { nodeID: "node-1", rehomedIncomingTransitionGroupID: "group-1" };

    dispatchPendingGraphMutation(pendingMutation({ kind: "extract", graphVersion: 3, input }), dispatch);

    expect(dispatch).toHaveBeenCalledExactlyOnceWith({ type: "extractNodeFromGroup", input });
  });
});
