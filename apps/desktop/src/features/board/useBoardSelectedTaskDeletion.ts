import { useCallback, useReducer, useRef } from "react";

import { useAppNavigation, useSidebar } from "@/app-facade";
import {
  boardTaskDeletionCauseHasActiveAttempt,
  recordBoardTaskDeletionAttempt,
  settleBoardTaskDeletionAttempt,
  type BoardTaskDeletionCause,
} from "./boardTaskDeletionCause";

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
  const deletionCauseRef = useRef<BoardTaskDeletionCause | null>(null);
  const [deletionCauseVersion, refreshDeletionCause] = useReducer((version: number) => version + 1, 0);
  const request = useCallback(() => {
    const attempt = { taskID: selectedTaskId };
    if (boardTaskDeletionCauseHasActiveAttempt(deletionCauseRef.current, selectedTaskId)) {
      return;
    }
    deletionCauseRef.current = recordBoardTaskDeletionAttempt(deletionCauseRef.current, attempt);
    refreshDeletionCause();
    invalidateSidebar({ kind: "task", taskID: selectedTaskId });
    void navigation.closeProjectTask(projectId, workflowId).then((result) => {
      if (result.status === "completed") {
        deletionCauseRef.current = settleBoardTaskDeletionAttempt(
          deletionCauseRef.current,
          attempt,
          "succeeded",
        );
        refreshDeletionCause();
        return;
      }
      deletionCauseRef.current = settleBoardTaskDeletionAttempt(
        deletionCauseRef.current,
        attempt,
        "failed",
      );
      refreshDeletionCause();
      onNavigationError(result.error);
    }, (error: unknown) => {
      deletionCauseRef.current = settleBoardTaskDeletionAttempt(
        deletionCauseRef.current,
        attempt,
        "failed",
      );
      refreshDeletionCause();
      onNavigationError(error);
    });
  }, [
    deletionCauseRef,
    invalidateSidebar,
    navigation,
    onNavigationError,
    projectId,
    selectedTaskId,
    workflowId,
  ]);
  return { deletionCauseRef, deletionCauseVersion, request };
}
