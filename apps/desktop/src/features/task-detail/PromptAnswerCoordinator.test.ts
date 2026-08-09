import { describe, expect, it } from "vitest";

import type { QuestionAttentionItem } from "@/api";
import { PromptAnswerCoordinator } from "./PromptAnswerCoordinator";
import { emptyPromptAnswerState, promptAnswerKey } from "./PromptAnswerState";
import { emptyQuestionSelection, withQuestionCommentary } from "./TaskDetailQuestionState";

describe("Task Detail prompt answer reconciliation", () => {
  it("masks immediately, then awaits invalidation and a fresh exact-key read before discarding state", async () => {
    const attention = questionAttention("session-1", "step-1", "prompt-1");
    const key = promptAnswerKey(attention);
    const selection = withQuestionCommentary(emptyQuestionSelection(), "draft");
    const answer = deferred<undefined>();
    const read = deferred<readonly QuestionAttentionItem[]>();
    const readStarted = deferred<undefined>();
    const events: string[] = [];
    let state = emptyPromptAnswerState().withSelection(key, selection);
    const coordinator = new PromptAnswerCoordinator({
      invalidateAttention: async () => {
        events.push("invalidated");
      },
      isMounted: () => true,
      notifyFailure: () => {
        throw new Error("unexpected failure notice");
      },
      readAttention: async () => {
        events.push("read");
        readStarted.resolve(undefined);
        return read.promise;
      },
      task: { id: "task-1", shortID: "TASK-1", title: "Task" },
      updateState: (update) => {
        state = update(state);
      },
    });

    const submission = coordinator.submit({
      attention,
      selection,
      send: async () => answer.promise,
    });

    expect(state.isMasked(key)).toBe(true);
    expect(events).toEqual([]);

    answer.resolve(undefined);
    await readStarted.promise;
    expect(events).toEqual(["invalidated", "read"]);
    expect(state.isMasked(key)).toBe(true);

    read.resolve([]);
    await submission;
    expect(state.isMasked(key)).toBe(false);
    expect(state.selection(key)).toBeUndefined();
  });

  it("restores the frozen draft and reports delivery failure only when the fresh read still has the exact key", async () => {
    const attention = questionAttention("session-1", "step-1", "prompt-1");
    const key = promptAnswerKey(attention);
    const selection = withQuestionCommentary(emptyQuestionSelection(), "frozen draft");
    const failures: unknown[] = [];
    let state = emptyPromptAnswerState().withSelection(key, selection);
    const coordinator = new PromptAnswerCoordinator({
      invalidateAttention: async () => undefined,
      isMounted: () => true,
      notifyFailure: (failure) => failures.push(failure),
      readAttention: async () => [attention],
      task: { id: "task-1", shortID: "TASK-1", title: "Task" },
      updateState: (update) => {
        state = update(state);
      },
    });

    await coordinator.submit({
      attention,
      selection,
      send: async () => {
        throw new Error("delivery failed");
      },
    });

    expect(state.isMasked(key)).toBe(false);
    expect(state.selection(key)?.answer).toBe("frozen draft");
    expect(failures).toEqual([
      expect.objectContaining({
        kind: "delivery",
        taskID: "task-1",
        taskShortID: "TASK-1",
        taskTitle: "Task",
      }),
    ]);
  });

  it("uses the cached prompt only as a mounted fallback when the authoritative reconciliation read fails", async () => {
    const attention = questionAttention("session-1", "step-1", "prompt-1");
    const key = promptAnswerKey(attention);
    const selection = withQuestionCommentary(emptyQuestionSelection(), "retry draft");
    const failures: unknown[] = [];
    let state = emptyPromptAnswerState().withSelection(key, selection);
    const coordinator = new PromptAnswerCoordinator({
      invalidateAttention: async () => undefined,
      isMounted: () => true,
      notifyFailure: (failure) => failures.push(failure),
      readAttention: async () => {
        throw new Error("read failed");
      },
      task: { id: "task-1", shortID: "TASK-1", title: "Task" },
      updateState: (update) => {
        state = update(state);
      },
    });

    await coordinator.submit({ attention, selection, send: async () => undefined });

    expect(state.isMasked(key)).toBe(false);
    expect(state.frozenSubmission(key)).toBeUndefined();
    expect(state.selection(key)?.answer).toBe("retry draft");
    expect(failures).toEqual([
      expect.objectContaining({
        kind: "reconciliation",
        taskShortID: "TASK-1",
      }),
    ]);
  });

  it("reconciles overlapping attempts independently when their reads finish out of order", async () => {
    const first = questionAttention("session-1", "step-1", "shared");
    const second = questionAttention("session-2", "step-1", "shared");
    const firstKey = promptAnswerKey(first);
    const secondKey = promptAnswerKey(second);
    const firstRead = deferred<readonly QuestionAttentionItem[]>();
    const secondRead = deferred<readonly QuestionAttentionItem[]>();
    const reads = [firstRead, secondRead];
    let state = emptyPromptAnswerState()
      .withSelection(firstKey, withQuestionCommentary(emptyQuestionSelection(), "first"))
      .withSelection(secondKey, withQuestionCommentary(emptyQuestionSelection(), "second"));
    const coordinator = new PromptAnswerCoordinator({
      invalidateAttention: async () => undefined,
      isMounted: () => true,
      notifyFailure: () => {
        throw new Error("unexpected failure notice");
      },
      readAttention: async () => {
        const next = reads.shift();
        if (next === undefined) {
          throw new Error("unexpected read");
        }
        return next.promise;
      },
      task: { id: "task-1", shortID: "TASK-1", title: "Task" },
      updateState: (update) => {
        state = update(state);
      },
    });

    const firstAttempt = coordinator.submit({
      attention: first,
      selection: state.selection(firstKey) ?? emptyQuestionSelection(),
      send: async () => undefined,
    });
    const secondAttempt = coordinator.submit({
      attention: second,
      selection: state.selection(secondKey) ?? emptyQuestionSelection(),
      send: async () => undefined,
    });

    secondRead.resolve([second]);
    await secondAttempt;
    expect(state.isMasked(firstKey)).toBe(true);
    expect(state.isMasked(secondKey)).toBe(false);
    expect(state.selection(secondKey)?.answer).toBe("second");

    firstRead.resolve([]);
    await firstAttempt;
    expect(state.selection(firstKey)).toBeUndefined();
    expect(state.selection(secondKey)?.answer).toBe("second");
  });

  it("does not restore local state after unmount and identifies the Task when delivery failed", async () => {
    const attention = questionAttention("session-1", "step-1", "prompt-1");
    const key = promptAnswerKey(attention);
    const selection = withQuestionCommentary(emptyQuestionSelection(), "discard me");
    const failures: unknown[] = [];
    let mounted = true;
    let state = emptyPromptAnswerState().withSelection(key, selection);
    let updateCount = 0;
    const coordinator = new PromptAnswerCoordinator({
      invalidateAttention: async () => undefined,
      isMounted: () => mounted,
      notifyFailure: (failure) => failures.push(failure),
      readAttention: async () => [attention],
      task: { id: "task-1", shortID: "TASK-1", title: "Task title" },
      updateState: (update) => {
        updateCount += 1;
        state = update(state);
      },
    });
    const answer = deferred<undefined>();

    const attempt = coordinator.submit({
      attention,
      selection,
      send: async () => answer.promise,
    });
    expect(updateCount).toBe(1);
    mounted = false;
    answer.reject(new Error("offline"));
    await attempt;

    expect(updateCount).toBe(1);
    expect(state.isMasked(key)).toBe(true);
    expect(failures).toEqual([
      expect.objectContaining({
        kind: "delivery",
        taskID: "task-1",
        taskShortID: "TASK-1",
        taskTitle: "Task title",
      }),
    ]);
  });
});

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  reject(error: unknown): void;
  resolve(value: T): void;
}> {
  let resolve: ((value: T) => void) | undefined;
  let reject: ((error: unknown) => void) | undefined;
  return {
    promise: new Promise<T>((nextResolve, nextReject) => {
      resolve = nextResolve;
      reject = nextReject;
    }),
    reject(error) {
      reject?.(error);
    },
    resolve(value) {
      resolve?.(value);
    },
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
