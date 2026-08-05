import { useCallback, useRef } from "react";

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
  const activeAttemptRef = useRef<BoardTaskDeletionAttempt | null>(null);
  return useCallback(() => {
    if (activeAttemptRef.current?.taskID === selectedTaskId) {
      return;
    }
    const attempt = { taskID: selectedTaskId };
    activeAttemptRef.current = attempt;
    onSelectedTaskDeleted?.(attempt);
    invalidateSidebar({ kind: "task", taskID: selectedTaskId });
    void navigation.closeProjectTask(projectId, workflowId).then((result) => {
      if (result.status === "completed") {
        onSelectedTaskDeletionNavigationSucceeded?.(attempt);
        return;
      }
      activeAttemptRef.current = null;
      onSelectedTaskDeletionNavigationFailed?.(attempt);
      onNavigationError(result.error);
    }, (error: unknown) => {
      activeAttemptRef.current = null;
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
