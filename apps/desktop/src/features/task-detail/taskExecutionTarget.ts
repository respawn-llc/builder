import type { TaskDetail } from "@/api";

export function taskExecutionRoot(detail: TaskDetail): string {
  return detail.worktreePath ?? detail.sourceWorkspace.rootPath;
}
