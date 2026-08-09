import { describe, expect, it } from "vitest";

import type { AttentionItem, QuestionAttentionItem } from "@/api";
import { promptAnswerKey } from "./PromptAnswerState";
import { promptSubmissionHandoff } from "./PromptSubmissionHandoff";

describe("Task Detail prompt submission presentation handoff", () => {
  it("preserves the pixel offset and requests the next prompt's primary control", () => {
    const first = questionAttention("session-1", "step-1", "prompt-1");
    const second = questionAttention("session-2", "step-2", "prompt-2");

    const handoff = promptSubmissionHandoff({
      attentionItems: [first, nonQuestionAttention(), second],
      requestID: 7,
      scrollOffsetPx: 241,
      submittedKey: promptAnswerKey(first),
    });

    expect(handoff.pixelOffsetRequest).toEqual({ key: "prompt-answer:7", offsetPx: 241 });
    expect(handoff.primaryFocusRequest).toEqual({
      key: promptAnswerKey(second),
      requestID: 7,
    });
  });

  it("does not fall back to another focus target when no later prompt exists", () => {
    const prompt = questionAttention("session-1", "step-1", "prompt-1");

    expect(
      promptSubmissionHandoff({
        attentionItems: [prompt],
        requestID: 8,
        scrollOffsetPx: 0,
        submittedKey: promptAnswerKey(prompt),
      }).primaryFocusRequest,
    ).toBeUndefined();
  });
});

function questionAttention(sessionID: string, stepID: string, promptID: string): QuestionAttentionItem {
  return {
    id: `${sessionID}:${stepID}`,
    kind: "question",
    message: "Question",
    occurredAt: 0,
    projectID: "project-1",
    question: {
      kind: "ordinary",
      promptID,
      recommendedOptionIndex: 1,
      sessionID,
      stepID,
      suggestions: ["One"],
    },
    currentNode: {
      effectiveAssignee: null,
      effectiveThinking: null,
      nodeID: "node-1",
      sessionID: null,
      transitionBranchKey: null,
    },
    sessionName: "Session",
    taskID: "task-1",
    taskShortID: "TASK-1",
    taskTitle: "Task",
    workflowID: "workflow-1",
  };
}

function nonQuestionAttention(): AttentionItem {
  return {
    approvalID: "approval-1",
    approvalSnapshot: {
      commentary: "",
      outputValues: {},
      sourceNodeName: "Node",
      targets: [{ displayName: "Target" }],
      version: 1,
    },
    id: "approval-attention",
    kind: "approval",
    message: null,
    occurredAt: 0,
    projectID: "project-1",
    taskID: "task-1",
    taskShortID: "TASK-1",
    taskTitle: "Task",
    workflowID: "workflow-1",
  };
}
