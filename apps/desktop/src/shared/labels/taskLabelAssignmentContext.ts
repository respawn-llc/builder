import { createContext, useContext } from "react";

import type { TaskLabelAssignmentData } from "./taskLabelAssignmentData";

export const TaskLabelAssignmentContext = createContext<TaskLabelAssignmentData | null>(null);

export function useTaskLabelAssignment(): TaskLabelAssignmentData {
  const value = useContext(TaskLabelAssignmentContext);
  if (value === null) {
    throw new Error("TaskLabelAssignmentProvider is required");
  }
  return value;
}
