import { CancelledError } from "@tanstack/react-query";

export async function awaitAllQueryOperations(operations: readonly Promise<unknown>[]): Promise<void> {
  const results = await Promise.allSettled(operations);
  const errors: unknown[] = [];
  for (const result of results) {
    if (result.status === "rejected") {
      const reason: unknown = result.reason;
      errors.push(reason);
    }
  }
  if (errors.length === 0) {
    return;
  }
  if (errors.length === 1) {
    throw errors[0];
  }
  throw new AggregateError(errors, "Multiple query operations failed");
}

export function reportNonCancelledError(error: unknown, report: (error: unknown) => void): void {
  if (error instanceof AggregateError) {
    for (const cause of error.errors) {
      reportNonCancelledError(cause, report);
    }
    return;
  }
  if (error instanceof CancelledError) {
    return;
  }
  report(error);
}
