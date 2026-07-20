import { useMemo, type ReactNode } from "react";

import type { ProjectLabelCatalog } from "@/api";
import { TaskLabelAssignmentContext } from "./taskLabelAssignmentContext";
import { useManagedTaskLabelAssignment } from "./taskLabelAssignmentData";

export function TaskLabelAssignmentProvider({
  catalog,
  children,
  taskID,
}: Readonly<{
  catalog: ProjectLabelCatalog | null;
  children: ReactNode;
  taskID: string;
}>) {
  const availableLabelIDs = useMemo(() => catalog?.labels.map((label) => label.id) ?? [], [catalog]);
  const assignment = useManagedTaskLabelAssignment(taskID, availableLabelIDs, catalog !== null);
  return (
    <TaskLabelAssignmentContext.Provider value={assignment}>{children}</TaskLabelAssignmentContext.Provider>
  );
}
