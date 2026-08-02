import { useCallback } from "react";

import {
  useAppNavigation,
  useSidebar,
  type SidebarRouteChangeExpectation,
} from "@/app-facade";
import { sidebarEntryTokenForDeletedTask } from "./boardSidebarDeletion";

export function useBoardSelectedTaskDeletion({
  onNavigationError,
  projectId,
  selectedTaskId,
  workflowId,
}: Readonly<{
  onNavigationError(error: unknown): void;
  projectId: string;
  selectedTaskId: string | undefined;
  workflowId: string | undefined;
}>) {
  const navigation = useAppNavigation();
  const {
    clearSidebarRouteChangePreservation,
    preserveSidebarOnNextRouteChange,
    removeSidebarEntry,
    stackDestinations,
    stackEntryTokens,
  } = useSidebar();
  return useCallback(() => {
    const deletedToken =
      selectedTaskId === undefined
        ? undefined
        : sidebarEntryTokenForDeletedTask(stackDestinations, stackEntryTokens, selectedTaskId);
    if (deletedToken !== undefined) {
      const expectation: SidebarRouteChangeExpectation = {
        kind: "projectTaskCleared",
        projectID: projectId,
        workflowID: workflowId,
      };
      preserveSidebarOnNextRouteChange(deletedToken, expectation);
      removeSidebarEntry(deletedToken);
    }
    void navigation.closeProjectTask(projectId, workflowId).catch((error: unknown) => {
      if (deletedToken !== undefined) {
        clearSidebarRouteChangePreservation(deletedToken);
      }
      onNavigationError(error);
    });
  }, [
    clearSidebarRouteChangePreservation,
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
