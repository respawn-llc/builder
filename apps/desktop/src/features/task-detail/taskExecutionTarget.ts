import type { TaskDetail } from "../../api";

export function taskExecutionRoot(detail: TaskDetail): string | null {
  if (detail.executionTarget === null) {
    return detail.sourceWorkspace.rootPath;
  }
  return detail.executionTarget.effectiveRoot;
}
