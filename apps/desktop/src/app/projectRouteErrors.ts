const legacyEmptyTaskSelectorMessage =
  "This legacy project URL contains an empty task selector. Remove '?taskId=' from the URL and reload the project.";

export class LegacyEmptyTaskSelectorError extends Error {
  readonly kind = "legacy_empty_task_selector";

  constructor() {
    super(legacyEmptyTaskSelectorMessage);
  }
}

export function isLegacyEmptyTaskSelectorError(error: Error): error is LegacyEmptyTaskSelectorError {
  return error instanceof LegacyEmptyTaskSelectorError;
}
