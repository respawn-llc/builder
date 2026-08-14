import { createContext, useContext } from "react";

import type { TaskLabelAssignmentData } from "./taskLabelAssignmentData";

export const TaskLabelAssignmentContext = createContext<TaskLabelAssignmentData | null>(null);

export function useTaskLabelAssignment(): TaskLabelAssignmentData {
  const value = useTaskLabelAssignmentOptional();
  if (value === null) {
    throw new Error("TaskLabelAssignmentProvider is required");
  }
  return value;
}

export function useTaskLabelAssignmentOptional(): TaskLabelAssignmentData | null {
  return useContext(TaskLabelAssignmentContext);
}
