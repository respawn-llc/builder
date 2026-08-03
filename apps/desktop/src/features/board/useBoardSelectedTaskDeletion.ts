import { useCallback, useRef } from "react";

import {
  useAppNavigation,
  useSidebar,
  type SidebarRouteChangeExpectation,
} from "@/app-facade";
import { sidebarEntryTokenForDeletedTask } from "./boardSidebarDeletion";

type SelectedTaskDeletionRun = Readonly<{
  selectedTaskID: string | undefined;
  runID: symbol;
  promise: Promise<void>;
}>;
type SelectedTaskDeletionOutcome = "completed" | "failed";

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
  const cleanupRef = useRef<SelectedTaskDeletionRun | null>(null);
  const {
    activeToken,
    clearSidebarRouteChangePreservation,
    preserveSidebarOnNextRouteChange,
    removeSidebarEntry,
    stackDestinations,
    stackEntryTokens,
  } = useSidebar();
  return useCallback(async (): Promise<void> => {
    const existingRun = cleanupRef.current;
    if (existingRun !== null && existingRun.selectedTaskID === selectedTaskId) {
      return existingRun.promise;
    }
    cleanupRef.current = null;
    const runID = Symbol("selected-task-deletion");

    const operation = (async (): Promise<SelectedTaskDeletionOutcome> => {
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
        return "completed";
      } catch (error: unknown) {
        if (preservationToken !== null) {
          clearSidebarRouteChangePreservation(preservationToken);
        }
        onNavigationError(error);
        return "failed";
      }
    })();
    const completion = operation.then(
      (outcome) => {
        if (outcome === "failed" && cleanupRef.current?.runID === runID) {
          cleanupRef.current = null;
        }
      },
      (error: unknown) => {
        if (cleanupRef.current?.runID === runID) {
          cleanupRef.current = null;
        }
        throw error;
      },
    );
    cleanupRef.current = { runID, selectedTaskID: selectedTaskId, promise: completion };
    return completion;
  }, [
    cleanupRef,
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
