import { useMemo, type ReactNode } from "react";

import { TaskLabelAssignmentContext } from "./taskLabelAssignmentContext";
import { useManagedTaskLabelAssignment } from "./taskLabelAssignmentData";
import { useProjectLabelData } from "./projectLabelContext";

export function TaskLabelAssignmentProvider({
  children,
  enabled = true,
  taskID,
}: Readonly<{
  children: ReactNode;
  enabled?: boolean | undefined;
  taskID: string;
}>) {
  const { catalog, effects, projectID } = useProjectLabelData();
  const availableLabelIDs = useMemo(
    () => catalog.data?.labels.map((label) => label.id) ?? [],
    [catalog.data],
  );
  const assignment = useManagedTaskLabelAssignment({
    availableLabelIDs,
    enabled,
    projectID,
    scheduleCatalogRefresh: effects.scheduleCatalogRefresh,
    scheduleTaskAssignmentRefresh: effects.scheduleTaskAssignmentRefresh,
    taskID,
  });
  return (
    <TaskLabelAssignmentContext.Provider value={assignment}>{children}</TaskLabelAssignmentContext.Provider>
  );
}
