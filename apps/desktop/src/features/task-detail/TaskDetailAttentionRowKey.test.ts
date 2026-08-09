import { describe, expect, it } from "vitest";

import type { QuestionAttentionItem } from "@/api";
import { taskDetailAttentionRowKey } from "./TaskDetailAttentionRowKey";

describe("taskDetailAttentionRowKey", () => {
  it("preserves Questions with the same prompt ID from different Steps", () => {
    const first = question("step-1");
    const second = question("step-2");

    expect(taskDetailAttentionRowKey(first)).not.toBe(taskDetailAttentionRowKey(second));
  });
});

function question(stepID: string): QuestionAttentionItem {
  return {
    id: "shared-server-row",
    kind: "question",
    currentNode: {
      effectiveAssignee: null,
      effectiveThinking: null,
      nodeID: "node-1",
      sessionID: null,
      transitionBranchKey: null,
    },
    message: "Choose",
    occurredAt: 1,
    projectID: "project-1",
    question: {
      kind: "ordinary",
      promptID: "prompt-1",
      recommendedOptionIndex: null,
      sessionID: "session-1",
      stepID,
      suggestions: ["one"],
    },
    sessionName: null,
    taskID: "task-1",
    taskShortID: "TASK-1",
    taskTitle: "Task",
    workflowID: "workflow-1",
  };
}
