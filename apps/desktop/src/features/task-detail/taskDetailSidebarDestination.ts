import type { SidebarDestination, TaskDetailInitialFocus } from "@/app-facade";

type TaskDetailSidebarDestination = Extract<SidebarDestination, { kind: "taskDetail" }>;

export function taskDetailSidebarDestination(
  current: TaskDetailSidebarDestination,
  taskID: string,
  initialFocus?: TaskDetailInitialFocus,
): TaskDetailSidebarDestination {
  const sameTask = current.taskID === taskID;
  return {
    kind: "taskDetail",
    taskID,
    ...(current.mode === undefined ? {} : { mode: current.mode }),
    ...(initialFocus === undefined ? {} : { initialFocus }),
    ...(!sameTask || current.onMutated === undefined ? {} : { onMutated: current.onMutated }),
    ...(!sameTask || current.inboxNav === undefined ? {} : { inboxNav: current.inboxNav }),
  };
}
