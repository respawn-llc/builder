import { parseSetupOperationID, type InterruptedCurrentNodeAttentionItem } from "@/api";
import {
  sameTaskDetailInitialFocus,
  taskDetailInitialFocusFromAttentionItem,
  taskDetailInitialFocusRequestKey,
} from "./taskDetailInitialFocus";

it("keeps exact Current Node and Setup Operation identity through attention-item navigation", () => {
  const setupOperationID = parseSetupOperationID("66666666-6666-4666-8666-666666666666");
  const focus = taskDetailInitialFocusFromAttentionItem({
    id: "canonical-attention",
    kind: "interrupted_current_node",
    projectID: "project-1",
    workflowID: "11111111-1111-4111-8111-111111111111",
    taskID: "task-1",
    taskShortID: "T-1",
    taskTitle: "Recover setup",
    occurredAt: 1,
    currentNode: {
      nodeID: "canonical-node",
      transitionBranchKey: "branch-2",
      sessionID: null,
      effectiveAssignee: null,
      effectiveThinking: null,
    },
    sessionID: null,
    detailJSON: "{}",
    message: null,
    setupOperationID,
    setupRecovery: null,
  } satisfies InterruptedCurrentNodeAttentionItem);
  if (focus?.kind !== "interrupted_current_node") {
    throw new Error("Expected interrupted Current Node focus.");
  }
  expect(focus).toMatchObject({
    currentNodeID: "canonical-node",
    currentNodeBranchKey: "branch-2",
    setupOperationID,
  });

  const later = {
    ...focus,
    setupOperationID: parseSetupOperationID("77777777-7777-4777-8777-777777777777"),
  };
  expect(sameTaskDetailInitialFocus(focus, later)).toBe(false);
  expect(taskDetailInitialFocusRequestKey("task-1", focus)).not.toBe(
    taskDetailInitialFocusRequestKey("task-1", later),
  );
  expect(
    taskDetailInitialFocusRequestKey("task-1", {
      ...focus,
      currentNodeBranchKey: null,
      setupOperationID: null,
    }),
  ).not.toBe(
    taskDetailInitialFocusRequestKey("task-1", {
      ...focus,
      currentNodeBranchKey: "serial",
      setupOperationID: null,
    }),
  );
});
