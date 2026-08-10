import { describe, expect, it } from "vitest";
import type { QuestionAttentionItem } from "@/api";
import { parsedQuestionAttention } from "@/test-support/task-detail";
import { PromptAnswerCoordinator } from "./PromptAnswerCoordinator";
import { emptyPromptAnswerState, promptAnswerKey, samePromptAnswerKey } from "./PromptAnswerState";
import { taskDetailAttentionRowKey } from "./TaskDetailAttentionRowKey";
import { emptyQuestionSelection, withQuestionCommentary } from "./TaskDetailQuestionState";
type CoordinatorOptions = ConstructorParameters<typeof PromptAnswerCoordinator>[0];
describe("Task Detail prompt answer reconciliation", () => {
  it("masks until invalidation and a fresh exact-key read settle", async () => {
    const attention = question("step-1", "prompt-1");
    const read = deferred<readonly QuestionAttentionItem[]>();
    let invalidations = 0;
    const harness = coordinatorHarness([[attention, draft("draft")]], {
      invalidateAttention: async () => {
        invalidations += 1;
      },
      readAttention: async () => read.promise,
    });
    const attempt = submit(harness.coordinator, attention, "draft", async () => undefined);
    expect(harness.state.isMasked(promptAnswerKey(attention))).toBe(true);
    read.resolve([]);
    await attempt;
    expect(invalidations).toBe(1);
    expect(harness.state.selection(promptAnswerKey(attention))).toBeUndefined();
  });
  it.each(["delivery", "reconciliation"] as const)(
    "restores a frozen draft after %s failure",
    async (kind) => {
      const attention = question("step-1", "prompt-1");
      const harness = coordinatorHarness([[attention, draft("retry")]], {
        readAttention: async () => {
          if (kind === "reconciliation") throw new Error("read failed");
          return [attention];
        },
      });
      await submit(harness.coordinator, attention, "retry", async () => {
        if (kind === "delivery") throw new Error("delivery failed");
      });
      const key = promptAnswerKey(attention);
      expect(harness.state.isMasked(key)).toBe(false);
      expect(harness.state.selection(key)?.answer).toBe("retry");
      expect(harness.failures).toEqual([expect.objectContaining({ kind, taskShortID: "TASK-1" })]);
    },
  );
  it.each([
    ["step-2", "session-1"],
    ["step-1", "session-2"],
  ] as const)("isolates %s/%s identity collisions", async (stepID, sessionID) => {
    const first = question("step-1", "shared");
    const second = question(stepID, "shared", sessionID);
    const [firstRead, secondRead] = [
      deferred<readonly QuestionAttentionItem[]>(),
      deferred<readonly QuestionAttentionItem[]>(),
    ];
    const reads = [firstRead, secondRead];
    expect(Object.isFrozen(promptAnswerKey(first))).toBe(true);
    expect(samePromptAnswerKey(promptAnswerKey(first), promptAnswerKey(second))).toBe(false);
    expect(taskDetailAttentionRowKey(first)).not.toBe(taskDetailAttentionRowKey(second));
    const harness = coordinatorHarness(
      [
        [first, draft("first")],
        [second, draft("second")],
      ],
      { readAttention: async () => reads.shift()?.promise ?? Promise.reject(new Error("unexpected read")) },
    );
    const firstAttempt = submit(harness.coordinator, first, "first", async () => undefined);
    const secondAttempt = submit(harness.coordinator, second, "second", async () => undefined);
    secondRead.resolve([second]);
    await secondAttempt;
    expect(harness.state.isMasked(promptAnswerKey(first))).toBe(true);
    firstRead.resolve([]);
    await firstAttempt;
    expect(harness.state.selection(promptAnswerKey(first))).toBeUndefined();
    expect(harness.state.selection(promptAnswerKey(second))?.answer).toBe("second");
  });
  it("discards state after unmount and identifies the Task on failure", async () => {
    const attention = question("step-1", "prompt-1");
    const answer = deferred<undefined>();
    let mounted = true;
    const harness = coordinatorHarness([[attention, draft("discard")]], {
      isMounted: () => mounted,
      readAttention: async () => [attention],
      task: { id: "task-1", shortID: "TASK-1", title: "Task title" },
    });
    const attempt = submit(harness.coordinator, attention, "discard", async () => answer.promise);
    const maskedState = harness.state;
    mounted = false;
    answer.reject(new Error("offline"));
    await attempt;
    expect(harness.state).toBe(maskedState);
    expect(harness.failures).toEqual([
      expect.objectContaining({ taskID: "task-1", taskShortID: "TASK-1", taskTitle: "Task title" }),
    ]);
  });
});
function coordinatorHarness(
  selections: readonly (readonly [QuestionAttentionItem, ReturnType<typeof draft>])[],
  options: Pick<CoordinatorOptions, "readAttention"> & Partial<CoordinatorOptions>,
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
const draft = (answer: string) => withQuestionCommentary(emptyQuestionSelection(), answer);
async function submit(
  coordinator: PromptAnswerCoordinator,
  attention: QuestionAttentionItem,
  answer: string,
  send: () => Promise<void>,
) {
  return coordinator.submit({ attention, selection: draft(answer), send });
}
const baseQuestion = parsedQuestionAttention();
const question = (stepID: string, promptID: string, sessionID = "session-1"): QuestionAttentionItem => ({
  ...baseQuestion,
  question: { ...baseQuestion.question, promptID, sessionID, stepID },
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    [resolve, reject] = [nextResolve, nextReject];
  });
  return { promise, reject, resolve };
}
