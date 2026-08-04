import { useCallback } from "react";

import { useAppNavigation, useSidebar } from "@/app-facade";

export function useBoardSelectedTaskDeletion({
  onNavigationError,
  onSelectedTaskDeletionNavigationFailed,
  onSelectedTaskDeleted,
  projectId,
  selectedTaskId,
  workflowId,
}: Readonly<{
  onNavigationError(error: unknown): void;
  onSelectedTaskDeletionNavigationFailed?(): void;
  onSelectedTaskDeleted?(): void;
  projectId: string;
  selectedTaskId: string;
  workflowId: string | undefined;
}>) {
  const navigation = useAppNavigation();
  const { invalidateSidebar } = useSidebar();
  return useCallback(() => {
    onSelectedTaskDeleted?.();
    invalidateSidebar({ kind: "task", taskID: selectedTaskId });
    void navigation.closeProjectTask(projectId, workflowId).catch((error: unknown) => {
      onSelectedTaskDeletionNavigationFailed?.();
      onNavigationError(error);
    });
  }, [
    invalidateSidebar,
    navigation,
    onNavigationError,
    onSelectedTaskDeletionNavigationFailed,
    onSelectedTaskDeleted,
    projectId,
    selectedTaskId,
    workflowId,
  ]);
}
