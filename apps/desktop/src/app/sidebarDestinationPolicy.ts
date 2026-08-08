import type { SidebarDestinationPolicy } from "@/app-facade";

export const sidebarDestinationPolicy: SidebarDestinationPolicy = {
  equals: (left, right) =>
    left.kind === "taskDetail" && right.kind === "taskDetail" && left.taskID === right.taskID,
  retainedState: (_destination, state) => state,
};
