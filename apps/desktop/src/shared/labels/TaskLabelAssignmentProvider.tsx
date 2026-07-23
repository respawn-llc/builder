import { useMemo, type ReactNode } from "react";

import type { ProjectLabelCatalog, TaskLabelAssignment } from "@/api";
import { TaskLabelAssignmentContext } from "./taskLabelAssignmentContext";
import { useManagedTaskLabelAssignment } from "./taskLabelAssignmentData";

export function TaskLabelAssignmentProvider({
  catalog,
  children,
  initialAssignment,
  taskID,
  workflowID,
}: Readonly<{
  catalog: ProjectLabelCatalog | null;
  children: ReactNode;
  initialAssignment: TaskLabelAssignment;
  taskID: string;
  workflowID: string;
}>) {
  const availableLabelIDs = useMemo(() => catalog?.labels.map((label) => label.id) ?? [], [catalog]);
  const assignment = useManagedTaskLabelAssignment({
    availableLabelIDs,
    enabled: catalog !== null,
    initialAssignment,
    projectID: catalog?.projectID ?? "",
    taskID,
    workflowID,
  });
  return (
    <TaskLabelAssignmentContext.Provider value={assignment}>{children}</TaskLabelAssignmentContext.Provider>
  );
}
