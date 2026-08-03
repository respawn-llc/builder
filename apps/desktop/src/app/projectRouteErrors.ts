export class LegacyEmptyTaskSelectorError extends Error {
  readonly kind = "legacy_empty_task_selector";
}

export function isLegacyEmptyTaskSelectorError(error: Error): error is LegacyEmptyTaskSelectorError {
  return error instanceof LegacyEmptyTaskSelectorError;
}
