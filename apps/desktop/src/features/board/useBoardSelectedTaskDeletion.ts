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
    activeToken,
    clearSidebarRouteChangePreservation,
    preserveSidebarOnNextRouteChange,
    removeSidebarEntry,
    stackDestinations,
    stackEntryTokens,
  } = useSidebar();
  return useCallback(async () => {
    const deletedToken =
      selectedTaskId === undefined
        ? undefined
        : sidebarEntryTokenForDeletedTask(stackDestinations, stackEntryTokens, selectedTaskId);
    const preservationToken = selectedTaskId === undefined ? null : (deletedToken ?? activeToken);
    if (preservationToken !== null) {
      const expectation: SidebarRouteChangeExpectation = {
        kind: "projectTaskCleared",
        projectID: projectId,
        workflowID: workflowId,
      };
      preserveSidebarOnNextRouteChange(preservationToken, expectation);
      if (deletedToken !== undefined) {
        removeSidebarEntry(deletedToken);
      }
    }
    try {
      await navigation.closeProjectTask(projectId, workflowId);
    } catch (error: unknown) {
      if (preservationToken !== null) {
        clearSidebarRouteChangePreservation(preservationToken);
      }
      onNavigationError(error);
    }
  }, [
    activeToken,
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
