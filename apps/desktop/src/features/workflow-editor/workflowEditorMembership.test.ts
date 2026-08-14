import { describe, expect, it } from "vitest";

import {
  draftDefinitionFromSource,
  initializeWorkflowEditorDraft,
  workflowEditorDraftGraph,
} from "./workflowEditorDraft";
import {
  addWorkflowNode,
  createWorkflowNodeGroupFromNode,
  removeWorkflowNodeFromGroup,
} from "./workflowEditorGraphNodeMutations";
import { groupableWorkflowDefinition } from "./workflowEditorGraphMutationFixtures";

describe("workflow editor Node Group membership", () => {
  it("represents absent membership as null through creation, removal, and Draft projection", () => {
    const source = draftDefinitionFromSource(groupableWorkflowDefinition);
    const addedNodeID = "10000000-0000-4000-8000-000000000005";
    const groupID = "20000000-0000-4000-8000-000000000001";
    const joinNodeID = "10000000-0000-4000-8000-000000000006";

    const added = addWorkflowNode(source, { id: addedNodeID, kind: "agent" });
    expect(added.draft.nodes.find((node) => node.id === addedNodeID)?.groupID).toBeNull();

    const grouped = createWorkflowNodeGroupFromNode(source, {
      groupID,
      joinNodeID,
      nodeID: "node-agent",
    });
    expect(grouped.draft.nodes.find((node) => node.id === "node-agent")?.groupID).toBe(groupID);

    const removed = removeWorkflowNodeFromGroup(grouped.draft, "node-agent");
    expect(removed.draft.nodes.find((node) => node.id === "node-agent")?.groupID).toBeNull();

    const projected = workflowEditorDraftGraph(initializeWorkflowEditorDraft(groupableWorkflowDefinition));
    expect(projected.nodes.every((node) => node.groupID === null)).toBe(true);
  });
});
