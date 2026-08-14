import type { SidebarDestinationPolicy } from "@/app-facade";
import { decodeNewTaskRetainedState } from "@/features/tasks";
import { insertPreparedTaskDependency } from "@/shared/task-dependencies";

export const sidebarDestinationPolicy: SidebarDestinationPolicy = {
  applyBackResult: (destination, state, result) => {
    if (destination.kind !== "newTask") {
      return state;
    }
    const retainedState = decodeNewTaskRetainedState(state);
    if (retainedState === undefined) {
      return state;
    }
    return {
      ...retainedState,
      preparedDependencies: insertPreparedTaskDependency(retainedState.preparedDependencies, {
        direction: result.direction,
        taskID: result.task.id,
        shortID: result.task.shortID,
        title: result.task.title,
        workflowID: result.task.workflowID,
        status: result.task.status,
      }),
    };
  },
  equals: (left, right) =>
    left.kind === "taskDetail" && right.kind === "taskDetail" && left.taskID === right.taskID,
  retainedState: (_destination, state) => state,
};
