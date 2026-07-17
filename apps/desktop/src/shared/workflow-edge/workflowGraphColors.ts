export function workflowEdgeColor(contextMode: string, hasError: boolean): string {
  if (hasError) {
    return "var(--color-error)";
  }
  if (contextMode === "new_session") {
    return "var(--color-primary)";
  }
  if (contextMode === "compact_and_continue_session") {
    return "var(--color-secondary)";
  }
  return "var(--color-outline)";
}
