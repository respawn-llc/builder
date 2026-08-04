import { useCallback } from "react";

import { useAppNavigation, useSidebar } from "@/app-facade";
import type { BoardTaskDeletionAttempt } from "./boardTaskDeletionCause";

export function useBoardSelectedTaskDeletion({
  onNavigationError,
  onSelectedTaskDeletionNavigationSucceeded,
  onSelectedTaskDeletionNavigationFailed,
  onSelectedTaskDeleted,
  projectId,
  selectedTaskId,
  workflowId,
}: Readonly<{
  onNavigationError(error: unknown): void;
  onSelectedTaskDeletionNavigationFailed?(attempt: BoardTaskDeletionAttempt): void;
  onSelectedTaskDeletionNavigationSucceeded?(attempt: BoardTaskDeletionAttempt): void;
  onSelectedTaskDeleted?(attempt: BoardTaskDeletionAttempt): void;
  projectId: string;
  selectedTaskId: string;
  workflowId: string | undefined;
}>) {
  const navigation = useAppNavigation();
  const { invalidateSidebar } = useSidebar();
  return useCallback(() => {
    const attempt = { taskID: selectedTaskId };
    onSelectedTaskDeleted?.(attempt);
    invalidateSidebar({ kind: "task", taskID: selectedTaskId });
    void navigation.closeProjectTask(projectId, workflowId).then((result) => {
      if (result.status === "completed") {
        onSelectedTaskDeletionNavigationSucceeded?.(attempt);
        return;
      }
      onSelectedTaskDeletionNavigationFailed?.(attempt);
      onNavigationError(result.error);
    }, (error: unknown) => {
      onSelectedTaskDeletionNavigationFailed?.(attempt);
      onNavigationError(error);
    });
  }, [
    invalidateSidebar,
    navigation,
    onNavigationError,
    onSelectedTaskDeletionNavigationSucceeded,
    onSelectedTaskDeletionNavigationFailed,
    onSelectedTaskDeleted,
    projectId,
    selectedTaskId,
    workflowId,
  ]);
}
