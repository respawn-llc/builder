import { describe, expect, it } from "vitest";

import type { QuestionAttentionItem } from "@/api";
import {
  emptyPromptAnswerState,
  promptAnswerKey,
  samePromptAnswerKey,
  type QuestionSelectionState,
} from "./PromptAnswerState";

describe("Task Detail prompt answer state", () => {
  it("compares the complete Session, Step, and prompt identity structurally", () => {
    const key = promptAnswerKey(questionAttention("session-1", "step-1", "prompt-1"));

    expect(samePromptAnswerKey(key, { ...key })).toBe(true);
    expect(samePromptAnswerKey(key, { ...key, sessionID: "session-2" })).toBe(false);
    expect(samePromptAnswerKey(key, { ...key, stepID: "step-2" })).toBe(false);
    expect(samePromptAnswerKey(key, { ...key, promptID: "prompt-2" })).toBe(false);
  });

  it("keeps colliding prompt IDs independent across drafts, masks, responses, and cleanup", () => {
    const sessionOne = questionAttention("session-1", "step-1", "shared-prompt");
    const sessionTwo = questionAttention("session-2", "step-1", "shared-prompt");
    const stepTwo = questionAttention("session-1", "step-2", "shared-prompt");
    const sessionOneKey = promptAnswerKey(sessionOne);
    const sessionTwoKey = promptAnswerKey(sessionTwo);
    const stepTwoKey = promptAnswerKey(stepTwo);

    let state = emptyPromptAnswerState()
      .withSelection(sessionOneKey, selection("session one"))
      .withSelection(sessionTwoKey, selection("session two"))
      .withSelection(stepTwoKey, selection("step two"))
      .beginSubmission(sessionOneKey, sessionOne)
      .beginSubmission(sessionTwoKey, sessionTwo)
      .beginSubmission(stepTwoKey, stepTwo);

    expect(state.selection(sessionOneKey)?.answer).toBe("session one");
    expect(state.selection(sessionTwoKey)?.answer).toBe("session two");
    expect(state.selection(stepTwoKey)?.answer).toBe("step two");
    expect(state.isMasked(sessionOneKey)).toBe(true);
    expect(state.isMasked(sessionTwoKey)).toBe(true);
    expect(state.isMasked(stepTwoKey)).toBe(true);

    state = state.restoreSubmission(sessionTwoKey);
    expect(state.isMasked(sessionOneKey)).toBe(true);
    expect(state.isMasked(sessionTwoKey)).toBe(false);
    expect(state.isMasked(stepTwoKey)).toBe(true);
    expect(state.selection(sessionTwoKey)?.answer).toBe("session two");

    state = state.discardSubmission(sessionOneKey);
    expect(state.selection(sessionOneKey)).toBeUndefined();
    expect(state.isMasked(sessionOneKey)).toBe(false);
    expect(state.selection(sessionTwoKey)?.answer).toBe("session two");
    expect(state.isMasked(stepTwoKey)).toBe(true);

    state = state.restoreSubmission(stepTwoKey);
    expect(state.selection(stepTwoKey)?.answer).toBe("step two");
    expect(state.isMasked(stepTwoKey)).toBe(false);
  });
});

function selection(answer: string): QuestionSelectionState {
  return {
    answer,
    approvalDecision: null,
    provenance: "explicit",
    selectedOption: 1,
  };
}

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
