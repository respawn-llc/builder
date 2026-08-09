import { expect, it } from "vitest";

import type { WorkflowGraphSaveImpact } from "@/api";
import { confirmationFromImpact } from "./workflowEditorGraphMutationPlanning";

it("includes removed Node Groups in graph-save confirmation counts", () => {
  const impact: WorkflowGraphSaveImpact = {
    activeCurrentNodeCount: 0,
    edgeTaskReferenceCount: 2,
    lastTerminalChangeCount: 0,
    nodeTaskReferenceCount: 1,
    pendingApprovalCount: 0,
    removedEdgeCount: 3,
    removedEntities: [
      { entityID: "group-1", entityType: "node_group" },
      { entityID: "edge-1", entityType: "edge" },
    ],
    removedNodeCount: 4,
    removedNodeGroupCount: 5,
    removedTransitionGroupCount: 6,
    startNodeChangeCount: 0,
    taskReferencedNodeKindChangeCount: 0,
  };

  expect(confirmationFromImpact(impact)).toEqual({
    expectedEdgeTaskReferenceCount: 2,
    expectedNodeTaskReferenceCount: 1,
    expectedRemovedEdgeCount: 3,
    expectedRemovedNodeCount: 4,
    expectedRemovedNodeGroupCount: 5,
    expectedRemovedTransitionGroupCount: 6,
  });
});
