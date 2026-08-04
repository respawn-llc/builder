import { useCallback } from "react";

import { useAppNavigation, useSidebar } from "@/app-facade";
import { sidebarEntryTokenForDeletedTask } from "./boardSidebarDeletion";

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
  const { preserveSidebarOnNextRouteChange, removeSidebarEntry, stackDestinations, stackEntryTokens } =
    useSidebar();
  return useCallback(() => {
    const deletedToken = sidebarEntryTokenForDeletedTask(
      stackDestinations,
      stackEntryTokens,
      selectedTaskId,
    );
    if (deletedToken !== undefined) {
      preserveSidebarOnNextRouteChange();
      removeSidebarEntry(deletedToken);
    }
    void navigation.closeProjectTask(projectId, workflowId).catch(onNavigationError);
  }, [
    navigation,
    onNavigationError,
    preserveSidebarOnNextRouteChange,
    projectId,
    selectedTaskId,
    stackDestinations,
    stackEntryTokens,
    removeSidebarEntry,
    workflowId,
  ]);
}
