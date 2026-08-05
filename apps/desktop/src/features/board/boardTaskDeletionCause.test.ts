import {
  boardTaskDeletionCauseHasActiveAttempt,
  boardTaskDeletionCauseMatches,
  boardTaskDeletionCauseShouldDefer,
  recordBoardTaskDeletionAttempt,
  settleBoardTaskDeletionAttempt,
  type BoardTaskDeletionCause,
} from "./boardTaskDeletionCause";

describe("board task deletion causes", () => {
  it("keeps the cause when an older attempt fails and a newer attempt succeeds", () => {
    const first = { taskID: "task-1" };
    const second = { taskID: "task-1" };
    let cause: BoardTaskDeletionCause | null = recordBoardTaskDeletionAttempt(null, first);
    cause = recordBoardTaskDeletionAttempt(cause, second);

    const afterFirst = settleBoardTaskDeletionAttempt(cause, first, "failed");
    if (afterFirst === null) throw new Error("The second deletion attempt must remain pending.");
    const afterSecond = settleBoardTaskDeletionAttempt(afterFirst, second, "succeeded");
    if (afterSecond === null) throw new Error("A successful deletion attempt must retain its cause.");
    cause = afterSecond;

    expect(cause.succeeded).toBe(true);
    expect(boardTaskDeletionCauseMatches(cause, "task-1", null)).toBe(true);
  });

  it("keeps the cause when a newer attempt fails after an older attempt succeeds", () => {
    const first = { taskID: "task-1" };
    const second = { taskID: "task-1" };
    let cause: BoardTaskDeletionCause | null = recordBoardTaskDeletionAttempt(null, first);
    cause = recordBoardTaskDeletionAttempt(cause, second);

    const afterFirst = settleBoardTaskDeletionAttempt(cause, first, "succeeded");
    if (afterFirst === null) throw new Error("A successful deletion attempt must retain its cause.");
    const afterSecond = settleBoardTaskDeletionAttempt(afterFirst, second, "failed");
    if (afterSecond === null) throw new Error("A successful deletion attempt must retain its cause.");
    cause = afterSecond;

    expect(cause.succeeded).toBe(true);
    expect(boardTaskDeletionCauseMatches(cause, "task-1", null)).toBe(true);
  });

  it("clears the cause only after every attempt fails", () => {
    const first = { taskID: "task-1" };
    const second = { taskID: "task-1" };
    let cause: BoardTaskDeletionCause | null = recordBoardTaskDeletionAttempt(null, first);
    cause = recordBoardTaskDeletionAttempt(cause, second);

    cause = settleBoardTaskDeletionAttempt(cause, first, "failed");
    expect(cause).not.toBeNull();
    cause = settleBoardTaskDeletionAttempt(cause, second, "failed");

    expect(cause).toBeNull();
  });

  it("does not preserve an ordinary absence while deletion navigation is pending", () => {
    const pending = recordBoardTaskDeletionAttempt(null, { taskID: "task-1" });

    expect(boardTaskDeletionCauseMatches(pending, "task-1", null)).toBe(false);
  });

  it("does not preserve an ordinary absence after the pending navigation fails", () => {
    const attempt = { taskID: "task-1" };
    const pending = recordBoardTaskDeletionAttempt(null, attempt);
    const failed = settleBoardTaskDeletionAttempt(pending, attempt, "failed");

    expect(failed).toBeNull();
    expect(boardTaskDeletionCauseMatches(failed, "task-1", null)).toBe(false);
  });

  it("defers a committed selector absence until the shared navigation settles", () => {
    const attempt = { taskID: "task-1" };
    const pending = recordBoardTaskDeletionAttempt(null, attempt);

    expect(boardTaskDeletionCauseHasActiveAttempt(pending, "task-1")).toBe(true);
    expect(boardTaskDeletionCauseShouldDefer(pending, "task-1", null)).toBe(true);

    const succeeded = settleBoardTaskDeletionAttempt(pending, attempt, "succeeded");
    expect(boardTaskDeletionCauseShouldDefer(succeeded, "task-1", null)).toBe(false);
    expect(boardTaskDeletionCauseMatches(succeeded, "task-1", null)).toBe(true);
  });
});
