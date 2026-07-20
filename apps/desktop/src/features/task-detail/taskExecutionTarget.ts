import type { TaskDetail } from "@/api";

export function taskExecutionRoot(detail: TaskDetail): string | null {
  return detail.worktreePath ?? detail.sourceWorkspace.rootPath;
}
