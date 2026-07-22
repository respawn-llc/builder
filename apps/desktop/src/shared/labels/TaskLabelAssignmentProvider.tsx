import { useMemo, type ReactNode } from "react";

import type { ProjectLabelCatalog } from "@/api";
import { TaskLabelAssignmentContext } from "./taskLabelAssignmentContext";
import { useManagedTaskLabelAssignment } from "./taskLabelAssignmentData";

export function TaskLabelAssignmentProvider({
  catalog,
  children,
  taskID,
  workflowID,
}: Readonly<{
  catalog: ProjectLabelCatalog | null;
  children: ReactNode;
  taskID: string;
  workflowID: string;
}>) {
  const availableLabelIDs = useMemo(() => catalog?.labels.map((label) => label.id) ?? [], [catalog]);
  const assignment = useManagedTaskLabelAssignment({
    availableLabelIDs,
    enabled: catalog !== null,
    projectID: catalog?.projectID ?? "",
    taskID,
    workflowID,
  });
  return (
    <TaskLabelAssignmentContext.Provider value={assignment}>{children}</TaskLabelAssignmentContext.Provider>
  );
}
