import { useCallback, useLayoutEffect, useRef } from "react";

import { useAppNavigation, useSidebar } from "@/app-facade";
import { taskDetailRouteShouldClose } from "./taskDetailRouteLifecycle";

export function useBoardSelectedTaskDeletion({
  enabled,
  onNavigationError,
  projectId,
  selectedTaskId,
  selectedWorkflowID,
}: Readonly<{
  enabled: boolean;
  onNavigationError(error: unknown): void;
  projectId: string;
  selectedTaskId: string | undefined;
  selectedWorkflowID: string | undefined;
}>) {
  const navigation = useAppNavigation();
  const { invalidateSidebar, recordTaskDeletion, settleTaskDeletion, openSidebar } = useSidebar();
  const previousTaskIDRef = useRef<string | undefined>(undefined);

  const request = useCallback(() => {
    if (!enabled || selectedTaskId === undefined || selectedWorkflowID === undefined) {
      return;
    }
    recordTaskDeletion(selectedTaskId);
    invalidateSidebar({ kind: "task", taskID: selectedTaskId });
    void navigation.closeProjectTask(projectId, selectedWorkflowID).then((result) => {
      settleTaskDeletion(selectedTaskId, result.status);
      if (result.status === "failed") {
        onNavigationError(result.error);
      }
    }, (error: unknown) => {
      settleTaskDeletion(selectedTaskId, "failed");
      onNavigationError(error);
    });
  }, [
    enabled,
    invalidateSidebar,
    navigation,
    onNavigationError,
    projectId,
    selectedTaskId,
    selectedWorkflowID,
    recordTaskDeletion,
    settleTaskDeletion,
  ]);

  useLayoutEffect(() => {
    if (!enabled || selectedWorkflowID === undefined || previousTaskIDRef.current === selectedTaskId) {
      return;
    }
    previousTaskIDRef.current = selectedTaskId;
    if (selectedTaskId === undefined) return;
    void openSidebar({
      kind: "taskDetail",
      mode: "overlay",
      projectID: projectId,
      taskID: selectedTaskId,
    }).then((result) => {
      if (taskDetailRouteShouldClose(result)) {
        void navigation.closeProjectTask(projectId, selectedWorkflowID).then((result) => {
          if (result.status === "failed") onNavigationError(result.error);
        });
      }
    });
  }, [
    enabled,
    navigation,
    onNavigationError,
    openSidebar,
    projectId,
    selectedTaskId,
    selectedWorkflowID,
  ]);

  return { request };
}
