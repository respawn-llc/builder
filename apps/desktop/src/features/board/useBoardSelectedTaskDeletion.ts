import { useCallback } from "react";

import { useAppNavigation, useSidebar } from "@/app-facade";

export function useBoardSelectedTaskDeletion({
  onNavigationError,
  projectId,
  selectedTaskId,
  workflowId,
}: Readonly<{
  onNavigationError(error: unknown): void;
  projectId: string;
  selectedTaskId: string;
  workflowId: string | undefined;
}>) {
  const navigation = useAppNavigation();
  const { invalidateSidebar } = useSidebar();
  return useCallback(() => {
    invalidateSidebar({ kind: "task", taskID: selectedTaskId });
    void navigation.closeProjectTask(projectId, workflowId).catch(onNavigationError);
  }, [
    invalidateSidebar,
    navigation,
    onNavigationError,
    projectId,
    selectedTaskId,
    workflowId,
  ]);
}
