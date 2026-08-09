import { describe, expect, it } from "vitest";

import type { QuestionAttentionItem } from "@/api";
import { attentionItemSchema } from "@/api/schemas/common";
import { questionAttention as questionFixture } from "@/test-support/task-detail";
import { PromptAnswerCoordinator } from "./PromptAnswerCoordinator";
import { emptyPromptAnswerState, promptAnswerKey, samePromptAnswerKey } from "./PromptAnswerState";
import { taskDetailAttentionRowKey } from "./TaskDetailAttentionRowKey";
import {
  emptyQuestionSelection,
  withQuestionCommentary,
  type QuestionSelectionState,
} from "./TaskDetailQuestionState";

describe("Task Detail prompt answer reconciliation", () => {
  it("masks immediately and waits for invalidation plus a fresh exact-key read", async () => {
    const attention = question("session-1", "step-1", "prompt-1");
    const selection = draft("draft");
    const answer = deferred<void>();
    const read = deferred<readonly QuestionAttentionItem[]>();
    const readStarted = deferred<void>();
    let invalidations = 0;
    const harness = coordinatorHarness([[attention, selection]], {
      invalidateAttention: async () => {
        invalidations += 1;
      },
      readAttention: async () => {
        readStarted.resolve();
        return read.promise;
      },
    });

    const attempt = harness.coordinator.submit({
      attention,
      selection,
      send: () => answer.promise,
    });
    expect(harness.state.isMasked(promptAnswerKey(attention))).toBe(true);

    answer.resolve();
    await readStarted.promise;
    expect(invalidations).toBe(1);
    expect(harness.state.isMasked(promptAnswerKey(attention))).toBe(true);

    read.resolve([]);
    await attempt;
    expect(harness.state.selection(promptAnswerKey(attention))).toBeUndefined();
  });

  it.each(["delivery", "reconciliation"] as const)(
    "restores the frozen draft after a %s failure",
    async (failureKind) => {
      const attention = question("session-1", "step-1", "prompt-1");
      const selection = draft("retry draft");
      const harness = coordinatorHarness([[attention, selection]], {
        readAttention: async () => {
          if (failureKind === "reconciliation") {
            throw new Error("read failed");
          }
          return [attention];
        },
      });

      await harness.coordinator.submit({
        attention,
        selection,
        send: async () => {
          if (failureKind === "delivery") {
            throw new Error("delivery failed");
          }
        },
      });

      const key = promptAnswerKey(attention);
      expect(harness.state.isMasked(key)).toBe(false);
      expect(harness.state.frozenSubmission(key)).toBeUndefined();
      expect(harness.state.selection(key)?.answer).toBe("retry draft");
      expect(harness.failures).toEqual([
        expect.objectContaining({ kind: failureKind, taskShortID: "TASK-1" }),
      ]);
    },
  );

  it("reconciles full-key collisions independently when reads finish out of order", async () => {
    const first = question("session-1", "step-1", "shared");
    const second = question("session-1", "step-2", "shared");
    const firstRead = deferred<readonly QuestionAttentionItem[]>();
    const secondRead = deferred<readonly QuestionAttentionItem[]>();
    const reads = [firstRead, secondRead];
    expect(Object.isFrozen(promptAnswerKey(first))).toBe(true);
    expect(samePromptAnswerKey(promptAnswerKey(first), promptAnswerKey(second))).toBe(false);
    expect(taskDetailAttentionRowKey(first)).not.toBe(taskDetailAttentionRowKey(second));
    const harness = coordinatorHarness(
      [
        [first, draft("first")],
        [second, draft("second")],
      ],
      {
        readAttention: () => reads.shift()?.promise ?? Promise.reject(new Error("unexpected read")),
      },
    );

    const firstAttempt = harness.coordinator.submit({
      attention: first,
      selection: draft("first"),
      send: async () => undefined,
    });
    const secondAttempt = harness.coordinator.submit({
      attention: second,
      selection: draft("second"),
      send: async () => undefined,
    });

    secondRead.resolve([second]);
    await secondAttempt;
    expect(harness.state.isMasked(promptAnswerKey(first))).toBe(true);
    expect(harness.state.selection(promptAnswerKey(second))?.answer).toBe("second");

    firstRead.resolve([]);
    await firstAttempt;
    expect(harness.state.selection(promptAnswerKey(first))).toBeUndefined();
    expect(harness.state.selection(promptAnswerKey(second))?.answer).toBe("second");
  });

  it("does not restore after unmount and identifies the Task on delivery failure", async () => {
    const attention = question("session-1", "step-1", "prompt-1");
    const selection = draft("discard me");
    const answer = deferred<void>();
    let mounted = true;
    const harness = coordinatorHarness([[attention, selection]], {
      isMounted: () => mounted,
      readAttention: async () => [attention],
      task: { id: "task-1", shortID: "TASK-1", title: "Task title" },
    });
    const attempt = harness.coordinator.submit({ attention, selection, send: () => answer.promise });
    const maskedState = harness.state;

    mounted = false;
    answer.reject(new Error("offline"));
    await attempt;

    expect(harness.state).toBe(maskedState);
    expect(harness.failures).toEqual([
      expect.objectContaining({
        kind: "delivery",
        taskID: "task-1",
        taskShortID: "TASK-1",
        taskTitle: "Task title",
      }),
    ]);
  });
});

function coordinatorHarness(
  selections: readonly (readonly [QuestionAttentionItem, QuestionSelectionState])[],
  options: Readonly<{
    invalidateAttention?: () => Promise<void>;
    isMounted?: () => boolean;
    readAttention: () => Promise<readonly QuestionAttentionItem[]>;
    task?: Readonly<{ id: string; shortID: string; title: string }>;
  }>,
) {
  let state = selections.reduce(
    (current, [attention, selection]) => current.withSelection(promptAnswerKey(attention), selection),
    emptyPromptAnswerState(),
  );
  const failures: unknown[] = [];
  return {
    coordinator: new PromptAnswerCoordinator({
      invalidateAttention: options.invalidateAttention ?? (async () => undefined),
      isMounted: options.isMounted ?? (() => true),
      notifyFailure: (failure) => failures.push(failure),
      readAttention: options.readAttention,
      task: options.task ?? { id: "task-1", shortID: "TASK-1", title: "Task" },
      updateState: (update) => {
        state = update(state);
      },
    }),
    failures,
    get state() {
      return state;
    },
  };
}

function draft(answer: string): QuestionSelectionState {
  return withQuestionCommentary(emptyQuestionSelection(), answer);
}

const baseQuestion = attentionItemSchema.parse(questionFixture);

function question(sessionID: string, stepID: string, promptID: string): QuestionAttentionItem {
  if (baseQuestion.kind !== "question") {
    throw new Error("expected Question attention fixture");
  }
  return {
    ...baseQuestion,
    id: `${sessionID}:${stepID}`,
    question: { ...baseQuestion.question, promptID, sessionID, stepID },
  };
}

function deferred<T>() {
  let resolve: (value: T) => void = () => undefined;
  let reject: (error: unknown) => void = () => undefined;
  return {
    promise: new Promise<T>((nextResolve, nextReject) => {
      resolve = nextResolve;
      reject = nextReject;
    }),
    reject,
    resolve,
  };
}
