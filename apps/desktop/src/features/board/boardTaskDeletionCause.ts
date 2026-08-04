export type BoardTaskDeletionAttempt = Readonly<{ taskID: string }>;

export type BoardTaskDeletionCause = Readonly<{
  taskID: string;
  pending: ReadonlySet<BoardTaskDeletionAttempt>;
  succeeded: boolean;
}>;

export function recordBoardTaskDeletionAttempt(
  cause: BoardTaskDeletionCause | null,
  attempt: BoardTaskDeletionAttempt,
): BoardTaskDeletionCause {
  const pending = new Set(
    cause?.taskID === attempt.taskID ? cause.pending : undefined,
  );
  pending.add(attempt);
  return {
    pending,
    succeeded: cause?.taskID === attempt.taskID ? cause.succeeded : false,
    taskID: attempt.taskID,
  };
}

export function settleBoardTaskDeletionAttempt(
  cause: BoardTaskDeletionCause | null,
  attempt: BoardTaskDeletionAttempt,
  outcome: "failed" | "succeeded",
): BoardTaskDeletionCause | null {
  if (cause?.taskID !== attempt.taskID || !cause.pending.has(attempt)) {
    return cause;
  }
  const pending = new Set(cause.pending);
  pending.delete(attempt);
  if (outcome === "succeeded") {
    return { pending, succeeded: true, taskID: cause.taskID };
  }
  if (cause.succeeded || pending.size > 0) {
    return { pending, succeeded: cause.succeeded, taskID: cause.taskID };
  }
  return null;
}

export function boardTaskDeletionCauseMatches(
  cause: BoardTaskDeletionCause | null,
  previousTaskID: string | null,
  nextTaskID: string | null,
): boolean {
  return cause?.succeeded === true && cause.taskID === previousTaskID && nextTaskID === null;
}
